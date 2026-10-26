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
// At startup the kernel closes the gap: every still-"started" run whose
// journal has no terminal event and whose workflow no longer exists in
// Temporal is marked failed and gets an honest run.failed journal event, so
// the UI stops showing a ghost and the verdict trail stays auditable.

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
	RunTerminalStatuses(ctx context.Context, runIDs []string) (map[string]string, error)
	RunSourceID(ctx context.Context, project, runID string) (string, error)
	UpdateRunStatus(ctx context.Context, id, status, answer string, turns int, runErr string) error
}

// sweepOrphanedRuns marks lost runs failed. Best-effort end to end: a sweep
// error is logged and never blocks kernel startup.
func sweepOrphanedRuns(ctx context.Context, store orphanStore, liveness clientSweeper, journalURL, token, sourceID string, log *slog.Logger) {
	runs, err := store.ListStartedRuns(ctx, orphanRunCap)
	if err != nil {
		log.Warn("orphan sweep: list started runs", "error", err)
		return
	}
	if len(runs) == 0 {
		return
	}
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	terminal, err := store.RunTerminalStatuses(ctx, ids)
	if err != nil {
		log.Warn("orphan sweep: terminal statuses", "error", err)
		return
	}

	var closed []string
	for _, run := range runs {
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
		running, err := liveness.IsWorkflowRunning(ctx, runSource, run.ProjectID, run.ID)
		if err != nil {
			// A Temporal hiccup must not fail healthy runs — skip the sweep
			// round for this run and let the next restart decide.
			log.Warn("orphan sweep: liveness probe", "run", run.ID, "error", err)
			continue
		}
		if running {
			continue
		}
		reason := "kernel or Temporal restart removed the run's workflow before it finished"
		if err := store.UpdateRunStatus(ctx, run.ID, "failed", "", 0, reason); err != nil {
			log.Warn("orphan sweep: close run row", "run", run.ID, "error", err)
			continue
		}
		emitOrphanFailedEvent(ctx, journalURL, token, sourceID, run, reason, log)
		closed = append(closed, run.ID)
	}
	if len(closed) > 0 {
		log.Info("orphan sweep: marked lost runs failed", "runs", closed)
	}
}

// emitOrphanFailedEvent journals run.failed so the trace shows why the run
// ended — the same ingest path RecordReconciliation uses (best-effort).
func emitOrphanFailedEvent(ctx context.Context, journalURL, token, sourceID string, run workspace.Run, reason string, log *slog.Logger) {
	if journalURL == "" {
		return
	}
	event := observation.Event{
		Schema:     "temporality.event/1",
		EventID:    fmt.Sprintf("orphan-sweep/%s/%d", run.ID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: sourceID, Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: run.ProjectID,
			Run:     run.ID,
			Actor:   observation.Actor{ID: sourceID, Type: "system"},
		},
		Type: "run.failed",
		Data: map[string]any{
			"error":    reason,
			"orphaned": true,
			"turns":    run.Turns,
			"derived":  true,
		},
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
	IsWorkflowRunning(ctx context.Context, sourceID, project, runID string) (bool, error)
}

var _ clientSweeper = (*temporalLiveness)(nil)
