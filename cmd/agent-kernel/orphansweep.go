package main

// Orphaned-run sweep (cmd startup, docs/builtin-skills.md §3 neighborhood —
// run-lifecycle resilience): rebuilding the stack while runs are in flight
// kills the kernel worker mid-activity, and a Temporal server without durable
// persistence (the dev stack ran start-dev in-memory) loses the workflow
// entirely. The journal freezes mid-turn, workspace_run stays "started" and
// the UI shows an eternal running turn — the "hangs until timeout" incident
// of 2026-10-26. Reconciliation cannot help: it only records a verdict, it
// never touches a workflow.
//
// At startup the kernel closes the gap for every run whose journal never
// recorded an outcome: the workflow's own result decides the verdict. A
// finished workflow (a turn-limit continuation chain returns its outcome
// through its child run, so its head used to journal no terminal event at
// all) is journaled with its honest outcome; only a workflow that is truly
// gone is marked failed as lost. Candidates come from both sides: workspace
// rows still "started" and journal runs without a terminal event.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// orphanRunCap bounds one sweep pass — a startup job must not hammer Temporal
// with thousands of describes. Anything beyond the cap waits for the next
// restart (rare by construction).
const orphanRunCap = 500

// orphanStore is the store face the sweep needs (a *workspace.Store subset).
type orphanStore interface {
	ListStartedRuns(ctx context.Context, limit int) ([]workspace.Run, error)
	RunsMissingTerminal(ctx context.Context, limit int) ([]workspace.RunRef, error)
	RunTerminalStatuses(ctx context.Context, runIDs []string) (map[string]string, error)
	RunSourceID(ctx context.Context, project, runID string) (string, error)
	UpdateRunStatus(ctx context.Context, id, status, answer string, turns int, runErr string) error
}

// runOutcome is the honest terminal truth of a run's workflow, resolved from
// Temporal: Found=false means the workflow is gone entirely (a lost run),
// Running means it is still in flight.
type runOutcome struct {
	Found   bool
	Running bool
	Status  string // completed / failed / cancelled (RunResult.Status mapped)
	Answer  string
	Turns   int
	Tokens  int
	CostUSD float64
	Error   string
}

// sweepOrphanedRuns repairs runs whose journal never recorded an outcome.
// Best-effort end to end: a sweep error is logged and never blocks kernel
// startup.
func sweepOrphanedRuns(ctx context.Context, store orphanStore, liveness clientSweeper, journalURL, token, sourceID string, log *slog.Logger) {
	runs, err := store.ListStartedRuns(ctx, orphanRunCap)
	if err != nil {
		log.Warn("orphan sweep: list started runs", "error", err)
		return
	}
	missing, err := store.RunsMissingTerminal(ctx, orphanRunCap)
	if err != nil {
		log.Warn("orphan sweep: runs missing terminal", "error", err)
		return
	}
	seen := map[string]bool{}
	candidates := make([]workspace.Run, 0, len(runs)+len(missing))
	for _, run := range runs {
		seen[run.ID] = true
		candidates = append(candidates, run)
	}
	for _, ref := range missing {
		if seen[ref.RunID] {
			continue
		}
		seen[ref.RunID] = true
		candidates = append(candidates, workspace.Run{ID: ref.RunID, ProjectID: ref.ProjectID})
	}
	if len(candidates) == 0 {
		return
	}
	ids := make([]string, 0, len(candidates))
	for _, run := range candidates {
		ids = append(ids, run.ID)
	}
	terminal, err := store.RunTerminalStatuses(ctx, ids)
	if err != nil {
		log.Warn("orphan sweep: terminal statuses", "error", err)
		return
	}

	var closed []string
	for _, run := range candidates {
		if terminal[run.ID] != "" {
			continue // the journal already knows how this run ended
		}
		// The workflow identity follows the run's own journal source: external
		// agents (docs/external-agents.md) may run under a source other than
		// the kernel's — a probe with the wrong id would flag a live run lost.
		runSource, err := store.RunSourceID(ctx, run.ProjectID, run.ID)
		if err != nil {
			continue // no journal events at all — nothing to probe honestly
		}
		outcome, err := liveness.ResolveRunOutcome(ctx, runSource, run.ProjectID, run.ID)
		if err != nil {
			// A Temporal hiccup must not fail healthy runs — skip the sweep
			// round for this run and let the next restart decide.
			log.Warn("orphan sweep: outcome resolve", "run", run.ID, "error", err)
			continue
		}
		if outcome.Running {
			continue
		}
		if !outcome.Found {
			// The workflow is truly gone: the old honest verdict — failed with
			// an explanatory reason.
			reason := "kernel or Temporal restart removed the run's workflow before it finished"
			if err := store.UpdateRunStatus(ctx, run.ID, "failed", "", 0, reason); err != nil {
				log.Warn("orphan sweep: close run row", "run", run.ID, "error", err)
				continue
			}
			emitSweepEvent(ctx, journalURL, token, sourceID, run, "run.failed", map[string]any{
				"error": reason, "orphaned": true, "turns": run.Turns, "derived": true,
			}, log)
			closed = append(closed, run.ID)
			continue
		}
		// The workflow FINISHED — journal its honest outcome (the turn-limit
		// continuation gap, or a final emit lost to a journal outage). A
		// declared-lost verdict here would fail a run that actually completed.
		if err := store.UpdateRunStatus(ctx, run.ID, outcome.Status, outcome.Answer, outcome.Turns, outcome.Error); err != nil {
			log.Warn("orphan sweep: close run row", "run", run.ID, "error", err)
			continue
		}
		kind, data := resolvedTerminalEvent(outcome)
		emitSweepEvent(ctx, journalURL, token, sourceID, run, kind, data, log)
		closed = append(closed, run.ID)
	}
	if len(closed) > 0 {
		log.Info("orphan sweep: repaired runs without a journal verdict", "runs", closed)
	}
}

// resolvedTerminalEvent maps a resolved workflow outcome onto the journal's
// terminal vocabulary — the same shape the workflow's own emits use.
func resolvedTerminalEvent(outcome runOutcome) (string, map[string]any) {
	switch outcome.Status {
	case "failed":
		detail := strings.TrimSpace(outcome.Error)
		if detail == "" {
			detail = "the run's workflow failed"
		}
		return "run.failed", map[string]any{"error": detail, "turns": outcome.Turns, "derived": true}
	case "cancelled":
		return "run.cancelled", map[string]any{"turns": outcome.Turns, "derived": true}
	default:
		// completed, or a forced-finale limit status (turn_limit & co) that
		// still delivered an answer.
		return "run.completed", map[string]any{
			"turns": outcome.Turns, "total_tokens": outcome.Tokens, "cost_usd": outcome.CostUSD,
			"status": outcome.Status, "derived": true,
		}
	}
}

// emitSweepEvent journals one derived lifecycle verdict so the trace shows
// why the run ended — the same ingest path RecordReconciliation uses
// (best-effort).
func emitSweepEvent(ctx context.Context, journalURL, token, sourceID string, run workspace.Run, kind string, data map[string]any, log *slog.Logger) {
	if journalURL == "" {
		return
	}
	event := observation.Event{
		Schema:     "temporality.event/1",
		EventID:    fmt.Sprintf("lifecycle-sweep/%s/%d", run.ID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: sourceID, Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: run.ProjectID,
			Run:     run.ID,
			Actor:   observation.Actor{ID: sourceID, Type: "system"},
		},
		Type: kind,
		Data: data,
	}
	if err := event.Validate(); err != nil {
		log.Warn("orphan sweep: event validate", "run", run.ID, "error", err)
		return
	}
	payload, err := json.Marshal(map[string]any{"events": []observation.Event{event}})
	if err != nil {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(journalURL, "/")+"/v1/observations/events", bytes.NewReader(payload))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		log.Warn("orphan sweep: journal ingest", "run", run.ID, "error", err)
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
}

// clientSweeper is the liveness face the sweep needs — satisfied by
// temporalLiveness (the same Describe-based probe the operations listing
// uses, R1-wrapped with the RPC deadline).
type clientSweeper interface {
	ResolveRunOutcome(ctx context.Context, sourceID, project, runID string) (runOutcome, error)
}

var _ clientSweeper = (*temporalLiveness)(nil)
