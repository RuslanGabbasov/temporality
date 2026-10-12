package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Agent evaluation models (docs/plan-evaluable-agent.md §2.2 stage 8): a suite
// of cases stored per agent; a run records per-case outcomes for a tested
// definition version. Mirrors skill evaluations (skillevals.go) with one key
// difference: a run is pinned to the definition version whose compiled prompt
// snapshot was tested — comparing versions compares definitions, not the
// drifting current prompt.

// AgentEvaluationSuite is the stored suite of an agent (one per agent, MVP).
type AgentEvaluationSuite struct {
	AgentID   string           `json:"agent_id"`
	Cases     []EvaluationCase `json:"cases"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// AgentEvaluationRun is one executed suite pass over an agent definition
// version. The verdict fields (specs/quality-loop.md §5.2, §6) are computed
// server-side by RecordAgentEvaluationRun — a client, and with it the
// tested model, never supplies them. Legacy rows keep verdict "unknown"
// and stay out of the gate.
type AgentEvaluationRun struct {
	ID           int64                  `json:"id"`
	AgentID      string                 `json:"agent_id"`
	AgentVersion int                    `json:"agent_version"`
	Passed       int                    `json:"passed"`
	Failed       int                    `json:"failed"`
	Cases        []EvaluationCaseResult `json:"cases"`
	// Unified verdict (§5.2): pass | fail | error | unknown (legacy rows).
	Verdict string `json:"verdict,omitempty"`
	// Cause explains an error/fail verdict: judge_not_configured |
	// judge_invalid_json | judge_error | model_timeout | model_error |
	// suite_empty | suite_too_small | pass_rate_below_min | pass_regression.
	Cause string `json:"cause,omitempty"`
	// Threshold is the snapshot of the thresholds that governed the verdict
	// (§5.1): min pass rate, regression tolerance, per-case judge thresholds.
	Threshold map[string]any `json:"threshold,omitempty"`
	// Judge provenance (§6.2-6.4): the installation-wide judge model, whether
	// it equals the tested model (self-bias marker, §11.1) and whether its
	// latest calibration is degraded below the 0.7 agreement floor.
	JudgeModel     string `json:"judge_model,omitempty"`
	JudgeSameModel bool   `json:"judge_same_model,omitempty"`
	JudgeDegraded  bool   `json:"judge_degraded,omitempty"`
	// Trigger records what started the run: manual | gate | schedule (§5.1).
	Trigger string `json:"trigger,omitempty"`
	// TestedModel is request-only provenance: the model whose answers the
	// cases carry. The server compares it with the judge model; it is never
	// stored (runs already pin agent_version, the definition under test).
	TestedModel string    `json:"tested_model,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// GetAgentEvaluationSuite returns the stored suite; an empty suite (no row) is
// not an error — callers decide whether an empty suite is runnable.
func (s *Store) GetAgentEvaluationSuite(ctx context.Context, agentID string) (AgentEvaluationSuite, error) {
	suite := AgentEvaluationSuite{AgentID: agentID, Cases: []EvaluationCase{}}
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT cases, updated_at FROM workspace_agent_evaluation_suite WHERE agent_id = $1`, agentID).
		Scan(&raw, &suite.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return suite, nil
	}
	if err != nil {
		return suite, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &suite.Cases); err != nil {
			return suite, err
		}
	}
	return suite, nil
}

// SaveAgentEvaluationSuite replaces the stored suite.
func (s *Store) SaveAgentEvaluationSuite(ctx context.Context, agentID string, cases []EvaluationCase) error {
	if cases == nil {
		cases = []EvaluationCase{}
	}
	raw, err := json.Marshal(cases)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO workspace_agent_evaluation_suite (agent_id, cases, updated_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (agent_id) DO UPDATE SET cases = EXCLUDED.cases, updated_at = now()`,
		agentID, raw)
	return err
}

// RecordAgentEvaluationRun stores one executed suite pass and finalises its
// verdict (specs/quality-loop.md §5.2, §6): pass/fail/error are recomputed
// here from the recorded case outcomes, the judge configuration and the
// effective thresholds — never taken from the request, so the tested model
// cannot decide its own run's status. Legacy callers get verdict "unknown"
// only for rows written before migration 000056; new rows always carry a
// computed verdict.
func (s *Store) RecordAgentEvaluationRun(ctx context.Context, run *AgentEvaluationRun) error {
	switch run.Trigger {
	case "":
		run.Trigger = "manual"
	case TriggerManual, TriggerGate, TriggerSchedule:
	default:
		return fmt.Errorf("unknown evaluation run trigger %q", run.Trigger)
	}
	if err := s.finalizeAgentEvaluationRun(ctx, run); err != nil {
		return err
	}
	raw, err := json.Marshal(run.Cases)
	if err != nil {
		return err
	}
	threshold, err := json.Marshal(run.Threshold)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_agent_evaluation_run
		   (agent_id, agent_version, passed, failed, cases,
		    verdict, cause, threshold, judge_model, judge_same_model, judge_degraded, trigger)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id, created_at`,
		run.AgentID, run.AgentVersion, run.Passed, run.Failed, raw,
		run.Verdict, run.Cause, threshold, run.JudgeModel, run.JudgeSameModel, run.JudgeDegraded, run.Trigger).
		Scan(&run.ID, &run.CreatedAt)
}

// ListAgentEvaluationRuns returns recent runs for an agent.
func (s *Store) ListAgentEvaluationRuns(ctx context.Context, agentID string, limit int) ([]AgentEvaluationRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, agent_id, agent_version, passed, failed, cases,
		        verdict, cause, threshold, judge_model, judge_same_model, judge_degraded, trigger, created_at
		 FROM workspace_agent_evaluation_run WHERE agent_id = $1 ORDER BY created_at DESC LIMIT $2`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AgentEvaluationRun
	for rows.Next() {
		var run AgentEvaluationRun
		var raw, threshold []byte
		if err := rows.Scan(&run.ID, &run.AgentID, &run.AgentVersion, &run.Passed, &run.Failed, &raw,
			&run.Verdict, &run.Cause, &threshold, &run.JudgeModel, &run.JudgeSameModel, &run.JudgeDegraded, &run.Trigger, &run.CreatedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &run.Cases); err != nil {
				return nil, err
			}
		}
		if len(threshold) > 0 {
			_ = json.Unmarshal(threshold, &run.Threshold)
		}
		result = append(result, run)
	}
	return result, rows.Err()
}
