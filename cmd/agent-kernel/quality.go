package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// qualityStore is the storage surface the M5 quality endpoints need;
// *workspace.Store satisfies it and tests provide fakes.
type qualityStore interface {
	GetAgent(ctx context.Context, id string) (workspace.Agent, error)
	AgentQualityReport(ctx context.Context, agentID string) (workspace.QualityReport, error)
	AgentVersionsDiff(ctx context.Context, agentID string, from, to int) (workspace.VersionDiff, error)
}

// qualityOutbox is the durable-outbox surface agent.quality.dashboard_viewed
// needs (§9); *outbox.Outbox satisfies it and tests provide fakes.
type qualityOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// RegisterQualityRoutes mounts the quality dashboard of specs/quality-loop.md §7:
// per-version quality metrics and the structural + compiled-prompt diff of
// two definition snapshots. RBAC mirrors the evaluation endpoints (§10.3):
// the /quality aggregate is writer/owner territory, the diff reads at reader
// like GET /versions and GET /evaluations.
func RegisterQualityRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws qualityStore, events qualityOutbox, visibleUnits func(*http.Request) []string) {
	// GET /quality (§7.1, §8.4): runs, confirmed/corrected verdict outcomes,
	// best and last suite evaluation, goldens and time-to-verified-improvement
	// per definition version. A version without data reports has_data=false
	// with null metrics instead of a fake 0% (§7.4).
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/quality", func(w http.ResponseWriter, r *http.Request) {
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
		report, err := ws.AgentQualityReport(r.Context(), agentID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		// Product metric (§9): every served dashboard view goes through the
		// durable outbox; the report rows stay the source of truth.
		emitQualityDashboardView(r.Context(), events, a, actorFromRequest(r))
		writeJSON(w, 200, report)
	})

	// GET /versions/diff (§7.2, §8.5): structural definition changes plus the
	// line diff of the compiled prompt snapshots (000030/000031 lineage).
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/versions/diff", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		from, to, ok := qualityVersionRange(w, r)
		if !ok {
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
		diff, err := ws.AgentVersionsDiff(r.Context(), agentID, from, to)
		if err != nil {
			if errors.Is(err, workspace.ErrVersionSnapshotMissing) {
				writeError(w, 422, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, diff)
	})
}

// qualityVersionRange parses ?from=&to= (§8.5): both parameters are required
// non-negative integers and must name different versions — invalid input is
// a 400 before any storage is touched.
func qualityVersionRange(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	fromParam := strings.TrimSpace(r.URL.Query().Get("from"))
	toParam := strings.TrimSpace(r.URL.Query().Get("to"))
	if fromParam == "" || toParam == "" {
		writeError(w, 400, errors.New("from and to query parameters are required"))
		return 0, 0, false
	}
	parse := func(name, value string) (int, bool) {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeError(w, 400, fmt.Errorf("invalid %s version %q", name, value))
			return 0, false
		}
		return parsed, true
	}
	from, ok := parse("from", fromParam)
	if !ok {
		return 0, 0, false
	}
	to, ok := parse("to", toParam)
	if !ok {
		return 0, 0, false
	}
	if from == to {
		writeError(w, 400, errors.New("from and to must be different versions"))
		return 0, 0, false
	}
	return from, to, true
}

// emitQualityDashboardView records agent.quality.dashboard_viewed (§9) on
// every served dashboard view through the durable outbox, like
// agent.evaluation.completed. Best-effort: a journal outage must not fail the
// read. Event ids include UnixNano (§8 idempotency note), so repeated views
// never collide in the journal.
func emitQualityDashboardView(ctx context.Context, events qualityOutbox, a workspace.Agent, actor observation.Actor) {
	if events == nil {
		return
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("agent-quality/%s/%d", a.ID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Project: a.ProjectID, Actor: actor},
		Type:       "agent.quality.dashboard_viewed",
		Data:       map[string]any{"agent_id": a.ID},
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit quality dashboard view", "agent", a.ID, "error", err)
	}
}
