package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// LLM-judge endpoints (specs/quality-loop.md §6, §8.6, M4): the installation
// judge configuration the kernel evaluator reads, and the per-agent
// calibration pass that proves the judge agrees with known verdicts before
// it may gate anything. main wires the real store, kernel activities and
// outbox through this one call; migration 000056 ships with the startup
// chain in main.

// judgeOutbox is the durable-outbox surface the calibration event needs
// (§9); *outbox.Outbox satisfies it and tests provide fakes.
type judgeOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// judgeStore is the storage surface the judge endpoints need;
// *workspace.Store satisfies it and tests provide fakes.
type judgeStore interface {
	GetAgent(ctx context.Context, id string) (workspace.Agent, error)
	JudgeConfig(ctx context.Context) (workspace.JudgeConfig, error)
	AgentJudgeCalibrationSet(ctx context.Context, agentID string, limit int) ([]workspace.JudgeCalibrationCase, error)
	SaveJudgeCalibration(ctx context.Context, calibration *workspace.JudgeCalibration) error
}

// judgeActivities is the kernel surface calibration needs; *agent.Activities
// satisfies it and tests provide fakes.
type judgeActivities interface {
	CalibrateJudge(ctx context.Context, cases []agent.JudgeCalibrationCase) (agent.JudgeCalibrationOutcome, error)
}

// RegisterM4Routes mounts the judge configuration and calibration endpoints.
func RegisterM4Routes(mux *http.ServeMux, gate *controlplane.Gate, ws judgeStore, activities judgeActivities, events judgeOutbox, visibleUnits func(*http.Request) []string, log *slog.Logger) {
	// §6.2: the judge model resolution the kernel evaluator reads before
	// scoring judge cases; configured=false turns the judge mode off.
	mux.HandleFunc("GET /v1/workspace/evaluation-judge", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		config, err := ws.JudgeConfig(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, config)
	})

	// §6.4 calibration: judge the fixed set of historically labelled answers
	// and record agreement. Below the 0.7 floor the judge is stored degraded —
	// the gate then refuses its runs as red with cause judge_degraded (§6.4).
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/evaluation-judge/calibrate", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		agentID := r.PathValue("agentID")
		a, err := ws.GetAgent(r.Context(), agentID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("agent not found"))
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(a.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		config, err := ws.JudgeConfig(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !config.Configured {
			writeError(w, 422, agent.ErrJudgeNotConfigured)
			return
		}
		set, err := ws.AgentJudgeCalibrationSet(r.Context(), agentID, 0)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if len(set) == 0 {
			writeError(w, 422, errors.New("no calibration cases with recorded answers yet — run the suite first"))
			return
		}
		cases := make([]agent.JudgeCalibrationCase, 0, len(set))
		for _, entry := range set {
			cases = append(cases, agent.JudgeCalibrationCase{
				Name: entry.Name, Input: entry.Input, Answer: entry.Answer,
				Label: entry.Label, LabelSource: entry.LabelSource, JudgePrompt: entry.JudgePrompt,
			})
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		outcome, err := activities.CalibrateJudge(ctx, cases)
		if err != nil {
			if errors.Is(err, agent.ErrJudgeNotConfigured) {
				writeError(w, 422, err)
				return
			}
			writeError(w, 422, err)
			return
		}
		// The workspace is the single source of truth for the stored flag
		// (§6.4): recompute degradation from the stored agreement, never
		// trust the runner's copy.
		snapshot := make([]workspace.JudgeCalibrationScore, 0, len(outcome.Scores))
		for _, score := range outcome.Scores {
			snapshot = append(snapshot, workspace.JudgeCalibrationScore{
				Name: score.Name, Label: score.Label, Score: score.Score, Match: score.Match,
			})
		}
		agreement, meanAbsDeviation, histogram := workspace.ComputeCalibrationScores(snapshot)
		degraded := workspace.JudgeCalibrationDegraded(agreement)
		calibration := workspace.JudgeCalibration{
			AgentID: agentID, JudgeModel: outcome.JudgeModel,
			AgreementRate: agreement, MeanAbsDeviation: meanAbsDeviation,
			Histogram: histogram, Degraded: degraded,
			CasesChecked: outcome.CasesChecked, CasesSnapshot: snapshot,
			CreatedBy: requestSubject(r),
		}
		if err := ws.SaveJudgeCalibration(r.Context(), &calibration); err != nil {
			writeError(w, 500, err)
			return
		}
		emitJudgeCalibrated(r.Context(), events, agentID, outcome.JudgeModel, agreement, degraded)
		writeJSON(w, 200, map[string]any{
			"judge_model":        outcome.JudgeModel,
			"agreement_rate":     agreement,
			"mean_abs_deviation": meanAbsDeviation,
			"degraded":           degraded,
			"cases_checked":      outcome.CasesChecked,
			"cases_skipped":      outcome.CasesSkipped,
		})
	})
}

// emitJudgeCalibrated records agent.evaluation.judge.calibrated (§9) with the
// agreement_rate in the payload. Best-effort: the stored calibration row is
// the source of truth.
func emitJudgeCalibrated(ctx context.Context, events judgeOutbox, agentID, judgeModel string, agreement float64, degraded bool) {
	if events == nil {
		return
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    "judge-calibrated/" + agentID + "/" + time.Now().UTC().Format("20060102T150405.000000000"),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: "workspace", Type: "system"}},
		Type:       "agent.evaluation.judge.calibrated",
		Data: map[string]any{
			"agent_id":       agentID,
			"judge_model":    judgeModel,
			"agreement_rate": agreement,
			"degraded":       degraded,
		},
	}
	if err := item.Validate(); err != nil {
		return
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit judge calibrated event", "agent", agentID, "error", err)
	}
}
