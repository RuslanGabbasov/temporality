package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// Golden eval-case candidates (specs/quality-loop.md §4): review endpoints
// for harvested candidates (§4.4, §8.2) plus the background job that turns
// finished production runs into candidates (§4.2) and reaps rejected rows
// after 90 days (§10.2). main registers the whole module through this one
// call; the migration ships here too so main stays a single line.

const (
	// evalCandidateTick is the harvesting cadence; evalCandidatePurgeInterval
	// paces the §10.2 retention sweep.
	evalCandidateTick          = 5 * time.Minute
	evalCandidatePurgeInterval = time.Hour
)

// candidateOutbox is the durable-outbox surface candidate events need;
// *outbox.Outbox satisfies it and tests provide fakes.
type candidateOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// RegisterEvalCandidateRoutes wires the eval-case candidate review endpoints
// and the harvesting job (which also refreshes terminal run rows from
// Temporal — see refreshFinishedRuns). The store schema arrives with the
// boot-time workspace migration pass (migrations/workspace).
func RegisterEvalCandidateRoutes(ctx context.Context, mux *http.ServeMux, ws *workspace.Store, gate *controlplane.Gate, events candidateOutbox, temporalClient client.Client, sourceID string, log *slog.Logger) {

	// Review queue of harvested candidates (§4.4). Writer/owner access, like
	// the evaluation endpoints — candidates carry production text (§10.3).
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/eval-case-candidates", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		agentID := r.PathValue("agentID")
		if !resolveCandidateAgent(w, r, ws, agentID) {
			return
		}
		candidates, err := ws.ListEvalCaseCandidates(r.Context(), agentID, r.URL.Query().Get("verdict"), 0)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"candidates": candidates, "count": len(candidates)})
	})

	// Accept (§8.2): the candidate becomes a suite case named
	// golden/<run_id-prefix>; the suite limit (16) and the 70 % rule answer
	// 422, a decided candidate 409 — repeat calls never double-insert (§8).
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/eval-case-candidates/{id}/accept", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		agentID := r.PathValue("agentID")
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, 400, errors.New("invalid candidate id"))
			return
		}
		if !resolveCandidateAgent(w, r, ws, agentID) {
			return
		}
		candidate, suite, acceptErr := ws.AcceptEvalCaseCandidate(r.Context(), agentID, id, requestUserID(r))
		switch {
		case errors.Is(acceptErr, workspace.ErrNotFound):
			writeError(w, 404, errors.New("candidate not found"))
		case errors.Is(acceptErr, workspace.ErrCandidateDecided):
			writeError(w, 409, fmt.Errorf("candidate %d already decided", id))
		case errors.Is(acceptErr, workspace.ErrSuiteFull), errors.Is(acceptErr, workspace.ErrProjectShare):
			writeError(w, 422, acceptErr)
		case acceptErr != nil:
			writeError(w, 500, acceptErr)
		default:
			emitEvalCaseCandidateDecided(r.Context(), events, candidate)
			writeJSON(w, 201, map[string]any{
				"case":       suite.Cases[len(suite.Cases)-1],
				"suite_size": len(suite.Cases),
			})
		}
	})

	// Reject (§8.2): the candidate stays in history (reaped after 90 days,
	// §10.2); repeating the decision is a conflict, not a second verdict.
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/eval-case-candidates/{id}/reject", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		agentID := r.PathValue("agentID")
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, 400, errors.New("invalid candidate id"))
			return
		}
		if !resolveCandidateAgent(w, r, ws, agentID) {
			return
		}
		candidate, rejectErr := ws.RejectEvalCaseCandidate(r.Context(), agentID, id, requestUserID(r))
		switch {
		case errors.Is(rejectErr, workspace.ErrNotFound):
			writeError(w, 404, errors.New("candidate not found"))
		case errors.Is(rejectErr, workspace.ErrCandidateDecided):
			writeError(w, 409, fmt.Errorf("candidate %d already decided", id))
		case rejectErr != nil:
			writeError(w, 500, rejectErr)
		default:
			emitEvalCaseCandidateDecided(r.Context(), events, candidate)
			writeJSON(w, 200, map[string]any{"candidate_id": candidate.ID, "verdict": candidate.Verdict})
		}
	})

	go runEvalCandidateJob(ctx, ws, events, temporalClient, sourceID, log)
}

// resolveCandidateAgent resolves the agent behind a candidate endpoint, writing
// the §8.2 error response itself. Org-hidden agents read as absent — the
// same visibility rule the evaluation and proposal endpoints apply.
func resolveCandidateAgent(w http.ResponseWriter, r *http.Request, ws *workspace.Store, agentID string) bool {
	a, err := ws.GetAgent(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			writeError(w, 404, errors.New("agent not found"))
			return false
		}
		writeError(w, 500, err)
		return false
	}
	if !workspace.OrgVisible(a.OrgUnitID, candidateVisibleUnits(r)) {
		writeError(w, 404, workspace.ErrNotFound)
		return false
	}
	return true
}

// candidateVisibleUnits is the org-chain projection main's handlers use (nil means
// unfiltered: admins, service tokens and unassigned users).
func candidateVisibleUnits(r *http.Request) []string {
	principal, ok := controlplane.FromContext(r.Context())
	if !ok || principal.Role >= controlplane.RoleAdmin {
		return nil
	}
	return principal.VisibleUnits
}

// runEvalCandidateJob is the §4/§10.2 loop: refresh finished run rows from
// Temporal, harvest them into redacted candidates and reap rejected
// candidates past retention. Best-effort by design: failures log and the
// next tick retries, the unique (run_id) index keeps repeats idempotent.
func runEvalCandidateJob(ctx context.Context, ws *workspace.Store, events candidateOutbox, temporalClient client.Client, sourceID string, log *slog.Logger) {
	ticker := time.NewTicker(evalCandidateTick)
	defer ticker.Stop()
	lastPurge := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		refreshFinishedRuns(ctx, ws, temporalClient, sourceID, log)
		created, harvestErr := ws.HarvestEvalCaseCandidates(ctx)
		for i := range created {
			emitEvalCaseCandidateCreated(ctx, events, created[i])
		}
		if harvestErr != nil {
			log.Warn("harvest eval case candidates", "error", harvestErr)
		}
		if lastPurge.IsZero() || time.Since(lastPurge) >= evalCandidatePurgeInterval {
			purged, purgeErr := ws.PurgeRejectedEvalCaseCandidates(ctx)
			if purgeErr != nil {
				log.Warn("reap rejected eval case candidates", "error", purgeErr)
			} else {
				if purged > 0 {
					log.Info("reaped rejected eval case candidates", "count", purged)
				}
				lastPurge = time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// refreshFinishedRuns mirrors the enrichment GET /v1/workspace/runs/{runID}
// performs on demand: a workspace_run row stays 'started' until someone
// polls it, and the harvester needs the terminal status plus the answer that
// becomes the golden's expected_output. Only Temporal-completed workflows
// persist their business status — exactly what the GET handler writes;
// undescribable or still-running workflows wait for a later tick and age out
// of the 7-day window.
func refreshFinishedRuns(ctx context.Context, ws *workspace.Store, temporalClient client.Client, sourceID string, log *slog.Logger) {
	runs, err := ws.PendingRunRefresh(ctx, 100)
	if err != nil {
		log.Warn("list runs to refresh", "error", err)
		return
	}
	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		workflowID := workflowIDFor(sourceID, run.ProjectID, run.RunID)
		response, describeErr := temporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
		if describeErr != nil {
			continue // no workflow (or transient API error): retried until the row ages out
		}
		if response.WorkflowExecutionInfo == nil ||
			response.WorkflowExecutionInfo.Status != enums.WORKFLOW_EXECUTION_STATUS_COMPLETED {
			continue
		}
		var result agent.RunResult
		if getErr := temporalClient.GetWorkflow(ctx, workflowID, "").Get(ctx, &result); getErr != nil {
			continue
		}
		if result.Status == "" || result.Status == "running" {
			continue // defensive: never persist an unsettled business status
		}
		if updateErr := ws.UpdateRunStatus(ctx, run.ID, result.Status, result.Answer, result.Turns, ""); updateErr != nil {
			log.Warn("record finished run", "run", run.RunID, "error", updateErr)
		}
	}
}

// emitEvalCaseCandidateCreated records eval.case_candidate.created (§9) for
// every newly inserted candidate, through the durable outbox. Best-effort: the
// candidate row stays the source of truth. Event ids include UnixNano (§8), so
// the journal never sees duplicates.
func emitEvalCaseCandidateCreated(ctx context.Context, events candidateOutbox, c workspace.EvalCaseCandidate) {
	if events == nil {
		return
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("eval-case-candidate-created/%s/%d", c.RunID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: c.ProjectID, Run: c.RunID,
			Actor: observation.Actor{ID: "system", Type: "agent"},
		},
		Type: "eval.case_candidate.created",
		Data: map[string]any{
			"agent_id":          c.AgentID,
			"run_id":            c.RunID,
			"source":            c.Source,
			"redaction_applied": c.RedactionApplied,
		},
	}
	if err := item.Validate(); err != nil {
		return
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit eval case candidate created", "run", c.RunID, "error", err)
	}
}

// emitEvalCaseCandidateDecided records eval.case_candidate.decided (§9) when
// a human accepts or rejects a candidate. Best-effort, id like above.
func emitEvalCaseCandidateDecided(ctx context.Context, events candidateOutbox, c workspace.EvalCaseCandidate) {
	if events == nil {
		return
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("eval-case-candidate-decided/%d/%d", c.ID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: c.ProjectID, Run: c.RunID,
			Actor: observation.Actor{ID: c.ReviewedBy, Type: "human"},
		},
		Type: "eval.case_candidate.decided",
		Data: map[string]any{
			"candidate_id": c.ID,
			"verdict":      c.Verdict,
			"reviewed_by":  c.ReviewedBy,
		},
	}
	if err := item.Validate(); err != nil {
		return
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit eval case candidate decided", "candidate", c.ID, "error", err)
	}
}
