package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// Scheduled quality runs (docs/agent-quality-loop.md §15): trigger=schedule
// re-runs every agent's evaluation suite on a cadence the operator sets in the
// UI. The loop is anchored on the newest scheduled run row, so restarts and
// interval changes do not fire duplicate cycles, and 0 disables the schedule.

// qualityScheduleStore is the storage surface the schedule endpoints and the
// loop need; *workspace.Store satisfies it and tests provide fakes.
type qualityScheduleStore interface {
	EvalScheduleInterval(ctx context.Context) (time.Duration, error)
	SetSetting(ctx context.Context, key, value string) error
	LatestScheduledEvalRun(ctx context.Context) (time.Time, error)
	ListEvalSuiteAgentIDs(ctx context.Context) ([]string, error)
}

// qualityScheduleRunner executes one agent's evaluation; *agent.Activities
// satisfies it and tests provide fakes.
type qualityScheduleRunner interface {
	RunAgentEvaluationMeta(ctx context.Context, agentID string, version int, meta agent.EvalRunMeta) (string, error)
}

// registerQualityScheduleRoutes mounts GET/PUT /v1/workspace/quality-schedule.
func registerQualityScheduleRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws qualityScheduleStore, events judgeOutbox) {
	mux.HandleFunc("GET /v1/workspace/quality-schedule", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		writeJSON(w, 200, qualityScheduleStatus(r.Context(), ws))
	})
	mux.HandleFunc("PUT /v1/workspace/quality-schedule", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			IntervalHours int `json:"interval_hours"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if req.IntervalHours < 0 {
			writeError(w, 422, errors.New("interval_hours must be >= 0 (0 disables the schedule)"))
			return
		}
		value := "0"
		if req.IntervalHours > 0 {
			value = fmt.Sprintf("%dh", req.IntervalHours)
		}
		if err := ws.SetSetting(r.Context(), workspace.SettingEvalScheduleInterval, value); err != nil {
			writeError(w, 500, err)
			return
		}
		emitQualityScheduleChanged(r.Context(), events, req.IntervalHours, requestSubject(r))
		writeJSON(w, 200, qualityScheduleStatus(r.Context(), ws))
	})
}

// qualityScheduleStatus shapes the schedule answer: the interval plus the
// approximate next fire time (last scheduled run + interval). Errors reading
// the anchor degrade to a zero next_run_at.
func qualityScheduleStatus(ctx context.Context, ws qualityScheduleStore) map[string]any {
	interval, err := ws.EvalScheduleInterval(ctx)
	if err != nil {
		interval = 0
	}
	status := map[string]any{"interval_hours": int(interval.Hours()), "enabled": interval > 0}
	if interval > 0 {
		if last, err := ws.LatestScheduledEvalRun(ctx); err == nil && !last.IsZero() {
			status["last_run_at"] = last.UTC().Format(time.RFC3339)
			status["next_run_at"] = last.Add(interval).UTC().Format(time.RFC3339)
		}
	}
	return status
}

// scheduleDue decides whether a cycle should fire: a zero anchor means
// "never ran" and fires immediately; otherwise the interval must have
// elapsed since the last scheduled run.
func scheduleDue(last time.Time, interval time.Duration, now time.Time) bool {
	if interval <= 0 {
		return false
	}
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= interval
}

// runQualityScheduleJob is the trigger=schedule loop. A short tick re-reads
// the setting, so UI changes apply without a restart; the actual work fires
// only when last scheduled run + interval has passed.
func runQualityScheduleJob(ctx context.Context, ws qualityScheduleStore, runner qualityScheduleRunner, log *slog.Logger) {
	const tick = time.Minute
	timer := time.NewTimer(tick)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		interval, err := ws.EvalScheduleInterval(ctx)
		if err != nil {
			log.Warn("quality schedule interval", "error", err)
			continue
		}
		if interval <= 0 {
			continue
		}
		last, err := ws.LatestScheduledEvalRun(ctx)
		if err != nil {
			log.Warn("quality schedule anchor", "error", err)
			continue
		}
		if !scheduleDue(last, interval, time.Now()) {
			continue
		}
		runScheduledEvaluations(ctx, ws, runner, log)
	}
}

// runScheduledEvaluations runs every suite-bearing agent once, sequentially:
// each run is a batch of model calls, so parallel fan-out would multiply load
// for no benefit. One agent's failure never stops the rest.
func runScheduledEvaluations(ctx context.Context, ws qualityScheduleStore, runner qualityScheduleRunner, log *slog.Logger) {
	ids, err := ws.ListEvalSuiteAgentIDs(ctx)
	if err != nil {
		log.Warn("quality schedule agents", "error", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	log.Info("quality schedule cycle started", "agents", len(ids))
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		if _, err := runner.RunAgentEvaluationMeta(runCtx, id, 0, agent.EvalRunMeta{Trigger: agent.EvalTriggerSchedule}); err != nil {
			log.Warn("quality schedule run", "agent", id, "error", err)
		}
		cancel()
	}
	log.Info("quality schedule cycle finished", "agents", len(ids))
}

// emitQualityScheduleChanged records the operator's schedule change; the
// stored setting row stays the source of truth.
func emitQualityScheduleChanged(ctx context.Context, events judgeOutbox, intervalHours int, subject string) {
	if events == nil {
		return
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    "quality-schedule-changed/" + time.Now().UTC().Format("20060102T150405.000000000"),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: subject, Type: "user"}},
		Type:       "agent.evaluation.schedule.changed",
		Data:       map[string]any{"interval_hours": intervalHours},
	}
	if err := item.Validate(); err != nil {
		return
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit quality schedule changed event", "error", err)
	}
}
