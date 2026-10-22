package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
	"go.temporal.io/api/serviceerror"
)

// nudgeStore is the workspace surface POST /nudge needs: the authoritative
// run → source resolution, the terminal-state lookup and the guarded status
// settle of the heal path.
// *workspace.Store satisfies it; tests provide fakes.
type nudgeStore interface {
	RunSourceID(ctx context.Context, project, runID string) (string, error)
	RunTerminalStatuses(ctx context.Context, runIDs []string) (map[string]string, error)
	SettleRunCancelled(ctx context.Context, id string) error
}

// nudgeJournal records the terminal event of the heal path — the same durable
// journal POST /terminate writes its already-gone cancellation to.
type nudgeJournal interface {
	Enqueue(ctx context.Context, event observation.Event) error
}

// nudgeSignaler delivers kernel.nudge to the run's workflow; the temporal
// client.Client satisfies it.
type nudgeSignaler interface {
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg any) error
}

// nudgeDeps are the route's collaborators, faked in tests the same way
// runListDeps fakes the run list.
type nudgeDeps struct {
	gate     *controlplane.Gate
	store    nudgeStore
	journal  nudgeJournal
	signaler nudgeSignaler
	log      *slog.Logger
}

// registerNudgeRoute mounts POST /v1/agent/runs/{runID}/nudge — the escape
// hatch for a stuck provider call (cancel it and retry the turn).
//
// Workflow identity is resolved from the journal, never from the client's
// source_id query parameter: that hint frequently misses a child run's own
// run.started (the event sits outside the window the UI loaded), and a
// hint-derived digest addresses a workflow Temporal never started — the 409
// "workflow not found for ID: agent-run/…" this route used to return verbatim.
// A workflow that is genuinely gone is healed exactly like the not-found
// branch of POST /terminate, and every other signal failure answers with a
// stable human-readable error instead of raw temporal text.
func registerNudgeRoute(mux *http.ServeMux, deps nudgeDeps) {
	mux.HandleFunc("POST /v1/agent/runs/{runID}/nudge", func(w http.ResponseWriter, r *http.Request) {
		var nudge agent.Nudge
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&nudge); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, 400, err)
			return
		}
		if nudge.ActorID == "" {
			if principal, ok := controlplane.FromContext(r.Context()); ok && principal.UserID != "" {
				nudge.ActorID = principal.UserID
			}
		}
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		if !deps.gate.Allow(w, r, controlplane.RoleWriter, project) {
			return
		}
		runID := r.PathValue("runID")
		sourceID, err := deps.store.RunSourceID(r.Context(), project, runID)
		if errors.Is(err, workspace.ErrNotFound) {
			writeError(w, http.StatusNotFound, fmt.Errorf("run %s is not known to this kernel — nothing to nudge", runID))
			return
		}
		if err != nil {
			deps.log.Warn("resolve nudge source from the journal", "run", runID, "project", project, "error", err)
			writeError(w, http.StatusInternalServerError, errors.New("could not resolve the run in the journal"))
			return
		}
		// The source_id query parameter stays accepted for older clients, but
		// it never decides identity — a disagreement with the journal is only
		// worth a log line, the journal's source wins.
		if hint := querySourceID(r, ""); hint != "" && hint != sourceID {
			deps.log.Info("nudge source_id hint ignored; the journal's source wins", "run", runID, "hint", hint, "source", sourceID)
		}
		workflowID := workflowIDFor(sourceID, project, runID)
		// Turn 0 means "any in-flight model call" — the workflow selector
		// matches it against every live turn.
		if err := deps.signaler.SignalWorkflow(r.Context(), workflowID, "", agent.NudgeSignal, nudge); err != nil {
			if !isWorkflowGone(err) {
				// The raw temporal text stays in the kernel log; the client
				// gets a stable, human-readable failure — never verbatim RPC.
				deps.log.Warn("nudge signal", "run", runID, "workflow", workflowID, "error", err)
				writeJSON(w, http.StatusConflict, map[string]string{
					"run_id": runID,
					"error":  "the nudge could not reach the run's workflow — the run may already be finished, refresh it and try again",
				})
				return
			}
			// A workflow Temporal does not know can never take the nudge — the
			// common case being a stand whose Temporal volume was rebuilt while
			// the journal kept the run. Heal like POST /terminate: journal the
			// terminal event, settle the workspace status and answer the
			// structured already_gone payload instead of a raw 409.
			healGoneRun(w, r, deps, sourceID, project, runID)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	})
}

// healGoneRun settles a run whose workflow is gone from the orchestrator:
// terminal run.cancelled in the journal plus the workspace status, then the
// 200 {run_id, status:"already_gone"} answer shared with POST /terminate.
// The heal is idempotent: a run the journal already closed gets the same
// structured answer with no second terminal event and no status rewrite; the
// event id is deterministic so even a repeat that races the outbox flush
// dedupes on the journal's (source_id, event_id) primary key, and the status
// settle is guarded in the store (terminal statuses never roll back).
// (A gone workflow cannot be nudged — there is no call left to cancel or
// retry — so reporting the settled state is the honest outcome.)
func healGoneRun(w http.ResponseWriter, r *http.Request, deps nudgeDeps, sourceID, project, runID string) {
	terminal, err := deps.store.RunTerminalStatuses(r.Context(), []string{runID})
	if err != nil {
		deps.log.Warn("check the run's terminal state before healing", "run", runID, "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("could not verify the run's state in the journal"))
		return
	}
	if status, done := terminal[runID]; done && status != "" {
		writeJSON(w, http.StatusOK, map[string]string{"run_id": runID, "status": "already_gone"})
		return
	}
	// The terminal event's actor is the authenticated principal (requestUserID),
	// exactly like the POST /terminate heal branch — the request body's
	// actor_id rides the signal only and never stamps the audit trail.
	actor := requestUserID(r)
	goneEvent := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("run-nudge/%s/gone", runID),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: sourceID, Integration: "temporality-agent-kernel", Version: "1"},
		Context:    observation.Context{Project: project, Run: runID, Actor: observation.Actor{ID: actor, Type: "human"}},
		Type:       "run.cancelled",
		Data:       map[string]any{"reason": "gone", "detail": "workflow not found in the orchestrator (already gone)", "actor_id": actor},
	}
	if err := deps.journal.Enqueue(r.Context(), goneEvent); err != nil {
		deps.log.Warn("journal the already-gone cancellation", "run", runID, "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("could not journal the run's cancellation"))
		return
	}
	// A missing workspace row (delegated child runs) or an already-terminal
	// status is nothing to settle — only real store failures are worth a log.
	if err := deps.store.SettleRunCancelled(r.Context(), runID); err != nil && !errors.Is(err, workspace.ErrNotFound) {
		deps.log.Warn("settle the healed run's status", "run", runID, "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"run_id": runID, "status": "already_gone"})
}

// isWorkflowGone reports that Temporal does not know the workflow at all
// (rebuilt volume, expired retention) as opposed to any other signal failure.
// Same detection as the POST /terminate not-found branch: a typed NotFound,
// with the message fallback for wrapped transport errors.
func isWorkflowGone(err error) bool {
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "not found") || strings.Contains(message, "NotFound")
}
