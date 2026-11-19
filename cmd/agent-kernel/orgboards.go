package main

// Organizational Operations HTTP API (docs/temporality-organizational-
// operations-spec.md §15, §9-13). Every read passes the server-side org
// check: gate.Allow plus the org_unit visibility of the requested node —
// UI-side filtering is never the protection (spec §16). Run-scope details
// (request_related_run_details) re-uses the run-visibility resolver so a
// notification never widens RBAC (spec §3, §7.1).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// orgRoutesDeps carries the wiring main.go already owns.
type orgRoutesDeps struct {
	mux            *http.ServeMux
	gate           *controlplane.Gate
	ws             *workspace.Store
	visibleUnits   func(r *http.Request) []string
	observationURL string
	journalToken   func() string
	log            *slog.Logger
}

// orgUnitNodeScope resolves the request's node scope: nil ids = unfiltered
// (admin/unassigned), a concrete org_node must be visible (404 like every
// org-scoped miss), scope=unit pins the node itself, scope=subtree (default)
// adds its descendants.
func orgUnitNodeScope(units []workspace.OrgUnit, visible []string, orgNode, scope string) ([]string, bool) {
	if orgNode == "" {
		if visible == nil {
			return nil, true
		}
		// The viewer's own chain: their unit and its ancestors only.
		return visible, true
	}
	if !workspace.OrgVisible(orgNode, visible) {
		return nil, false
	}
	if scope == "unit" {
		return []string{orgNode}, true
	}
	// subtree: the node plus everything below it (org_unit.path prefix).
	selfPath := ""
	for _, unit := range units {
		if unit.ID == orgNode {
			selfPath = unit.SelfPath()
			break
		}
	}
	ids := []string{orgNode}
	if selfPath != "" {
		for _, unit := range units {
			if unit.ID != orgNode && (unit.Path == selfPath || strings.HasPrefix(unit.Path, selfPath+".")) {
				ids = append(ids, unit.ID)
			}
		}
	}
	return ids, true
}

func orgWriteNotFound(w http.ResponseWriter) {
	writeError(w, 404, workspace.ErrNotFound)
}

func orgParsePeriod(r *http.Request) (*time.Time, *time.Time, error) {
	var from, to *time.Time
	if raw := r.URL.Query().Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, nil, errors.New("from must be RFC3339")
		}
		from = &parsed
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, nil, errors.New("to must be RFC3339")
		}
		to = &parsed
	}
	return from, to, nil
}

func orgPaging(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func orgCSV(r *http.Request, key string) []string {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// orgUsageMonths parses the months window (default 6, max 24) and returns the
// 'YYYY-MM' labels newest-first, current month included.
func orgUsageMonths(raw string) []string {
	count, _ := strconv.Atoi(strings.TrimSpace(raw))
	if count <= 0 {
		count = 6
	}
	if count > 24 {
		count = 24
	}
	now := time.Now().UTC()
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, now.AddDate(0, -i, 0).Format("2006-01"))
	}
	return out
}

// RegisterOrgOperationsRoutes mounts the §15 API surface and starts the
// background loops (fold projection, supervisor batches, steer delivery).
func RegisterOrgOperationsRoutes(ctx context.Context, deps orgRoutesDeps, activities *agent.Activities, signaler orgNoteSignaler, modelClient func(ctx context.Context) (*llm.Client, error)) {
	mux, gate, ws := deps.mux, deps.gate, deps.ws

	// --- §4/§9: activity feed ---

	mux.HandleFunc("GET /v1/org/activity", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		units, err := ws.ListOrgUnits(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		scope, ok := orgUnitNodeScope(units, deps.visibleUnits(r), r.URL.Query().Get("org_node"), r.URL.Query().Get("scope"))
		if !ok {
			orgWriteNotFound(w)
			return
		}
		from, to, err := orgParsePeriod(r)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		limit, offset := orgPaging(r)
		events, count, err := ws.ListOrgActivity(r.Context(), workspace.OrgActivityFilter{
			OrgNodeIDs: scope,
			EventTypes: orgCSV(r, "type"),
			Category:   r.URL.Query().Get("category"),
			RunID:      r.URL.Query().Get("run"),
			TaskID:     r.URL.Query().Get("task"),
			ActorID:    r.URL.Query().Get("actor"),
			ProjectID:  r.URL.Query().Get("project"),
			From:       from, To: to, Limit: limit, Offset: offset,
		})
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"events": events, "count": count})
	})

	mux.HandleFunc("GET /v1/org/activity/event", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		sourceID := r.URL.Query().Get("source_id")
		eventID := r.URL.Query().Get("event_id")
		if sourceID == "" || eventID == "" {
			writeError(w, 422, errors.New("source_id and event_id are required"))
			return
		}
		event, err := ws.GetOrgActivityEvent(r.Context(), sourceID, eventID)
		if errors.Is(err, workspace.ErrNotFound) {
			orgWriteNotFound(w)
			return
		}
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(event.OrgNodeID, deps.visibleUnits(r)) {
			orgWriteNotFound(w)
			return
		}
		writeJSON(w, 200, event)
	})

	mux.HandleFunc("GET /v1/org/active-runs", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		units, err := ws.ListOrgUnits(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		scope, ok := orgUnitNodeScope(units, deps.visibleUnits(r), r.URL.Query().Get("org_node"), r.URL.Query().Get("scope"))
		if !ok {
			orgWriteNotFound(w)
			return
		}
		limit, offset := orgPaging(r)
		runs, count, err := ws.ListOrgActiveRuns(r.Context(), workspace.OrgActiveRunFilter{
			OrgNodeIDs: scope,
			Status:     r.URL.Query().Get("status"),
			TaskID:     r.URL.Query().Get("task"),
			ActorID:    r.URL.Query().Get("actor"),
			ProjectID:  r.URL.Query().Get("project"),
			Limit:      limit, Offset: offset,
		})
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"runs": runs, "count": count})
	})

	// --- §6: supervisor batches ---

	mux.HandleFunc("GET /v1/org/activity-batches", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		units, err := ws.ListOrgUnits(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		scope, ok := orgUnitNodeScope(units, deps.visibleUnits(r), r.URL.Query().Get("org_node"), r.URL.Query().Get("scope"))
		if !ok {
			orgWriteNotFound(w)
			return
		}
		limit, offset := orgPaging(r)
		batches, count, err := ws.ListOrgActivityBatches(r.Context(), scope, limit, offset)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"batches": batches, "count": count})
	})

	mux.HandleFunc("GET /v1/org/activity-batches/{batchID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		batch, err := ws.GetOrgActivityBatch(r.Context(), r.PathValue("batchID"))
		if errors.Is(err, workspace.ErrNotFound) {
			orgWriteNotFound(w)
			return
		}
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(batch.OrgNodeID, deps.visibleUnits(r)) {
			orgWriteNotFound(w)
			return
		}
		writeJSON(w, 200, batch)
	})

	// create_activity_batch (§15): a manual batch — the loop picks it up on
	// its next tick and answers 202; processing is never request-blocking.
	mux.HandleFunc("POST /v1/org/activity-batches", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			OrgNodeID string `json:"org_node_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		req.OrgNodeID = strings.TrimSpace(req.OrgNodeID)
		if req.OrgNodeID == "" {
			writeError(w, 422, errors.New("org_node_id is required"))
			return
		}
		if !workspace.OrgVisible(req.OrgNodeID, deps.visibleUnits(r)) {
			orgWriteNotFound(w)
			return
		}
		result, err := ws.EffectiveOrgActivitySettings(r.Context(), req.OrgNodeID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !result.Settings.AgentAwarenessEnabled {
			writeError(w, 409, errors.New("agent awareness is disabled for this node"))
			return
		}
		now := time.Now().UTC()
		batchID := fmt.Sprintf("org-batch-%d-manual", now.UnixNano())
		batch := workspace.OrgActivityBatch{
			ID: batchID, OrgNodeID: req.OrgNodeID,
			Status: workspace.OrgBatchPending, Trigger: "manual",
		}
		if err := ws.CreateOrgActivityBatch(r.Context(), batch); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 202, map[string]any{"batch_id": batchID, "status": batch.Status})
	})

	mux.HandleFunc("GET /v1/org/activity-batches/{batchID}/decisions", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		batchID := r.PathValue("batchID")
		batch, err := ws.GetOrgActivityBatch(r.Context(), batchID)
		if errors.Is(err, workspace.ErrNotFound) {
			orgWriteNotFound(w)
			return
		}
		if !workspace.OrgVisible(batch.OrgNodeID, deps.visibleUnits(r)) {
			orgWriteNotFound(w)
			return
		}
		limit, offset := orgPaging(r)
		decisions, count, err := ws.ListOrgAwarenessDecisions(r.Context(), batchID, "", r.URL.Query().Get("decision"), limit, offset)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"decisions": decisions, "count": count})
	})

	// --- §7/§8: awareness deliveries ---

	mux.HandleFunc("GET /v1/org/awareness/deliveries", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		units, err := ws.ListOrgUnits(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		scope, ok := orgUnitNodeScope(units, deps.visibleUnits(r), r.URL.Query().Get("org_node"), r.URL.Query().Get("scope"))
		if !ok {
			orgWriteNotFound(w)
			return
		}
		from, to, err := orgParsePeriod(r)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		limit, offset := orgPaging(r)
		deliveries, count, err := ws.ListOrgAwarenessDeliveries(r.Context(), workspace.OrgDeliveryFilter{
			OrgNodeIDs:    scope,
			BatchID:       r.URL.Query().Get("batch_id"),
			SourceRunID:   r.URL.Query().Get("source_run"),
			TargetRunID:   r.URL.Query().Get("target_run"),
			SourceEventID: r.URL.Query().Get("source_event"),
			Decision:      r.URL.Query().Get("decision"),
			Status:        r.URL.Query().Get("status"),
			ActorID:       r.URL.Query().Get("actor"),
			From:          from, To: to, Limit: limit, Offset: offset,
		})
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"deliveries": deliveries, "count": count})
	})

	mux.HandleFunc("GET /v1/org/awareness/deliveries/{deliveryID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		delivery, err := ws.GetOrgAwarenessDelivery(r.Context(), r.PathValue("deliveryID"))
		if errors.Is(err, workspace.ErrNotFound) {
			orgWriteNotFound(w)
			return
		}
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(delivery.OrgNodeID, deps.visibleUnits(r)) {
			orgWriteNotFound(w)
			return
		}
		writeJSON(w, 200, delivery)
	})

	// record_awareness_follow_up (§15): an explicitly correlated action ref —
	// temporal proximity never marks acted_upon (spec §8).
	mux.HandleFunc("POST /v1/org/awareness/follow-up", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			DeliveryID string `json:"delivery_id"`
			ActionRef  string `json:"action_ref"`
			Status     string `json:"status"` // follow_up_requested | acted_upon
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		req.DeliveryID = strings.TrimSpace(req.DeliveryID)
		req.ActionRef = strings.TrimSpace(req.ActionRef)
		if req.DeliveryID == "" || req.ActionRef == "" {
			writeError(w, 422, errors.New("delivery_id and action_ref are required"))
			return
		}
		status := strings.TrimSpace(req.Status)
		if status == "" {
			status = workspace.OrgDeliveryActedUpon
		}
		if status != workspace.OrgDeliveryFollowUpRequested && status != workspace.OrgDeliveryActedUpon {
			writeError(w, 422, errors.New("status must be follow_up_requested or acted_upon"))
			return
		}
		delivery, err := ws.GetOrgAwarenessDelivery(r.Context(), req.DeliveryID)
		if errors.Is(err, workspace.ErrNotFound) {
			orgWriteNotFound(w)
			return
		}
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(delivery.OrgNodeID, deps.visibleUnits(r)) {
			orgWriteNotFound(w)
			return
		}
		updated, err := ws.AppendOrgAwarenessDeliveryFollowUp(r.Context(), req.DeliveryID, req.ActionRef, status)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), "org_awareness.follow_up", "run", delivery.TargetRunID,
			map[string]any{"delivery_id": req.DeliveryID, "action_ref": req.ActionRef, "status": status})
		writeJSON(w, 200, updated)
	})

	mux.HandleFunc("POST /v1/org/awareness/dismiss", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			DeliveryID string `json:"delivery_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		req.DeliveryID = strings.TrimSpace(req.DeliveryID)
		if req.DeliveryID == "" {
			writeError(w, 422, errors.New("delivery_id is required"))
			return
		}
		delivery, err := ws.GetOrgAwarenessDelivery(r.Context(), req.DeliveryID)
		if errors.Is(err, workspace.ErrNotFound) {
			orgWriteNotFound(w)
			return
		}
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(delivery.OrgNodeID, deps.visibleUnits(r)) {
			orgWriteNotFound(w)
			return
		}
		updated, err := ws.DismissOrgAwarenessDelivery(r.Context(), req.DeliveryID)
		if errors.Is(err, workspace.ErrConflict) {
			writeError(w, 409, errors.New("delivery already closed"))
			return
		}
		if err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), "org_awareness.dismiss", "run", delivery.TargetRunID,
			map[string]any{"delivery_id": req.DeliveryID})
		writeJSON(w, 200, updated)
	})

	// request_related_run_details (§7.1): the notification never widens RBAC
	// — the source run must be inside the caller's run-visibility scope, and
	// trace discovery additionally requires the node's allow_trace_discovery.
	mux.HandleFunc("POST /v1/org/awareness/details", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		var req struct {
			SourceRunID string `json:"source_run_id"`
			DetailLevel string `json:"detail_level"`
			DeliveryID  string `json:"delivery_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		req.SourceRunID = strings.TrimSpace(req.SourceRunID)
		req.DetailLevel = strings.ToLower(strings.TrimSpace(req.DetailLevel))
		if req.SourceRunID == "" {
			writeError(w, 422, errors.New("source_run_id is required"))
			return
		}
		switch req.DetailLevel {
		case "summary", "findings", "decisions", "artifacts", "trace":
		default:
			writeError(w, 422, errors.New("detail_level must be summary, findings, decisions, artifacts or trace"))
			return
		}
		// Run-visibility check: same contract as the journal's resolver; a
		// row-less child run resolves through its initiating actor.
		allowed, scoped := runVisibilityActors(r.Context(), ws, deps.log, r)
		if scoped && !childRunVisible(r.Context(), allowed, deps.observationURL, deps.journalToken(), req.SourceRunID) {
			orgWriteNotFound(w)
			return
		}
		project, err := ws.RunProjectID(r.Context(), req.SourceRunID)
		if err != nil {
			orgWriteNotFound(w)
			return
		}
		if req.DetailLevel == "trace" {
			// Trace discovery is a node-level grant (§5), checked per request.
			node := ""
			if row, err := ws.GetOrgActiveRun(r.Context(), project, req.SourceRunID); err == nil {
				node = row.OrgNodeID
			}
			result, err := ws.EffectiveOrgActivitySettings(r.Context(), node)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			if !result.Settings.AllowTraceDiscovery {
				writeError(w, 403, errors.New("trace discovery is disabled for this organizational node"))
				return
			}
		}
		details := orgRunDetails(r.Context(), ws, req.SourceRunID, req.DetailLevel)
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), "org_awareness.details", "run", req.SourceRunID,
			map[string]any{"level": req.DetailLevel, "delivery_id": req.DeliveryID})
		if req.DeliveryID != "" {
			if _, err := ws.AppendOrgAwarenessDeliveryFollowUp(r.Context(), req.DeliveryID,
				"details:"+req.DetailLevel, workspace.OrgDeliveryDetailsDelivered); err != nil {
				deps.log.Warn("org awareness details: mark delivery", "delivery", req.DeliveryID, "error", err)
			}
		}
		writeJSON(w, 200, map[string]any{
			"run_id": req.SourceRunID, "level": req.DetailLevel, "details": details,
		})
	})

	// --- §12: awareness flow ---

	mux.HandleFunc("GET /v1/org/awareness-flow", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		units, err := ws.ListOrgUnits(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		scope, ok := orgUnitNodeScope(units, deps.visibleUnits(r), r.URL.Query().Get("org_node"), r.URL.Query().Get("scope"))
		if !ok {
			orgWriteNotFound(w)
			return
		}
		from, to, err := orgParsePeriod(r)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		limit, offset := orgPaging(r)
		deliveries, count, err := ws.ListOrgAwarenessDeliveries(r.Context(), workspace.OrgDeliveryFilter{
			OrgNodeIDs:  scope,
			BatchID:     r.URL.Query().Get("batch_id"),
			SourceRunID: r.URL.Query().Get("source_run"),
			TargetRunID: r.URL.Query().Get("target_run"),
			Decision:    r.URL.Query().Get("decision"),
			Status:      r.URL.Query().Get("status"),
			ActorID:     r.URL.Query().Get("actor"),
			From:        from, To: to, Limit: limit, Offset: offset,
		})
		if err != nil {
			writeError(w, 500, err)
			return
		}
		flow := make([]map[string]any, 0, len(deliveries))
		for _, delivery := range deliveries {
			entry := map[string]any{
				"delivery":     delivery,
				"batch":        nil,
				"source_event": nil,
			}
			if delivery.BatchID != "" {
				if batch, err := ws.GetOrgActivityBatch(r.Context(), delivery.BatchID); err == nil {
					entry["batch"] = batch
				}
			}
			if sourceID, eventID, ok := strings.Cut(delivery.SourceEventID, "/"); ok {
				if event, err := ws.GetOrgActivityEvent(r.Context(), sourceID, eventID); err == nil {
					entry["source_event"] = event
				}
			}
			flow = append(flow, entry)
		}
		writeJSON(w, 200, map[string]any{"flow": flow, "count": count})
	})

	// --- §5: node settings ---

	mux.HandleFunc("GET /v1/org/units/{unitID}/activity-settings", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		unitID := r.PathValue("unitID")
		if unitID != "" && !workspace.OrgVisible(unitID, deps.visibleUnits(r)) {
			orgWriteNotFound(w)
			return
		}
		result, err := ws.EffectiveOrgActivitySettings(r.Context(), unitID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		local, err := ws.OrgActivitySettingRows(r.Context(), unitID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{
			"org_unit_id": unitID,
			"settings":    result.Settings,
			"values":      result.Values,
			"sources":     result.Sources,
			"local":       local,
			"keys":        workspace.OrgActivitySettingKeys(),
		})
	})

	mux.HandleFunc("PUT /v1/org/units/{unitID}/activity-settings", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		unitID := r.PathValue("unitID")
		if unitID != "" && !workspace.OrgVisible(unitID, deps.visibleUnits(r)) {
			orgWriteNotFound(w)
			return
		}
		var req map[string]string
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if len(req) == 0 {
			writeError(w, 422, errors.New("at least one setting is required"))
			return
		}
		for key, value := range req {
			if err := ws.SetOrgActivitySetting(r.Context(), unitID, key, value); err != nil {
				writeError(w, 422, err)
				return
			}
		}
		changed := map[string]any{}
		for key, value := range req {
			changed[key] = value
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), "org.settings.changed", "org_unit", unitID, changed)
		result, err := ws.EffectiveOrgActivitySettings(r.Context(), unitID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"settings": result.Settings, "values": result.Values, "sources": result.Sources})
	})

	mux.HandleFunc("DELETE /v1/org/units/{unitID}/activity-settings/{key}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		unitID, key := r.PathValue("unitID"), r.PathValue("key")
		if unitID != "" && !workspace.OrgVisible(unitID, deps.visibleUnits(r)) {
			orgWriteNotFound(w)
			return
		}
		if err := ws.DeleteOrgActivitySetting(r.Context(), unitID, key); err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), "org.settings.changed", "org_unit", unitID,
			map[string]any{"deleted": key})
		writeJSON(w, 200, map[string]any{"deleted": true})
	})

	// --- §10/§13: work view (сводный канбан + management problems) ---

	mux.HandleFunc("GET /v1/org/work-view", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		units, err := ws.ListOrgUnits(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		scope, ok := orgUnitNodeScope(units, deps.visibleUnits(r), r.URL.Query().Get("org_node"), r.URL.Query().Get("scope"))
		if !ok {
			orgWriteNotFound(w)
			return
		}
		view, err := buildOrgWorkView(r.Context(), ws, scope)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, view)
	})

	// §13 management action on a review-column task: the human reviewed the
	// failed work and accepts it — the card moves to «Завершено» until the
	// activity window washes it out. Reader-gated like the awareness
	// dismissal (it changes the shared board, not RBAC), audited. A NEW run
	// for the task clears the state automatically (UpsertOrgActiveRun).
	mux.HandleFunc("POST /v1/org/work/task/{taskID}/accept", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		taskID := r.PathValue("taskID")
		if taskID == "" {
			writeError(w, 400, errors.New("task id is required"))
			return
		}
		if err := ws.SetOrgWorkTaskState(r.Context(), taskID, "accepted", requestUserID(r)); err != nil {
			writeError(w, 422, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), "org_work.task_accepted", "task", taskID, nil)
		writeJSON(w, 200, map[string]any{"state": "accepted"})
	})

	// --- §11: aggregated plan graph (confirmed links only) ---

	mux.HandleFunc("GET /v1/org/plan-graph", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		units, err := ws.ListOrgUnits(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		scope, ok := orgUnitNodeScope(units, deps.visibleUnits(r), r.URL.Query().Get("org_node"), r.URL.Query().Get("scope"))
		if !ok {
			orgWriteNotFound(w)
			return
		}
		graph, err := buildOrgPlanGraph(r.Context(), ws, scope)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, graph)
	})

	// §24: monthly usage per org unit — the stats view reads the same
	// org_month_usage rows the policy admission gate enforces. Months are the
	// last N calendar months including the current one (default 6, max 24).
	mux.HandleFunc("GET /v1/org/usage", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		units, err := ws.ListOrgUnits(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		scope, ok := orgUnitNodeScope(units, deps.visibleUnits(r), r.URL.Query().Get("org_node"), r.URL.Query().Get("scope"))
		if !ok {
			orgWriteNotFound(w)
			return
		}
		months := orgUsageMonths(r.URL.Query().Get("months"))
		rows, err := ws.OrgMonthUsage(r.Context(), scope, months)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"rows": rows, "months": months})
	})

	startOrgActivityLoop(ctx, deps.log, ws, deps.observationURL)
	if orgOperationsEnabled() {
		startOrgSupervisorLoop(ctx, deps.log, ws, activities, deps.observationURL, modelClient)
		startOrgAwarenessDeliveryLoop(ctx, deps.log, ws, signaler, orgSteerCancelerImpl(ws, activities.SourceID, signaler, deps.observationURL, deps.journalToken, deps.log))
	}
}

// orgOperationsEnabled is the subsystem kill switch: the projection always
// runs (the boards read it), the supervisor + steer delivery honor the
// switch.
func orgOperationsEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("ORG_OPERATIONS"))
	return raw != "off" && raw != "0" && raw != "false"
}

// orgSteerCancelerFn adapts a closure to the orgSteerCanceler interface.
type orgSteerCancelerFn func(ctx context.Context, project, runID, reason string) error

func (f orgSteerCancelerFn) Cancel(ctx context.Context, project, runID, reason string) error {
	return f(ctx, project, runID, reason)
}

// orgSteerCancelerImpl executes the supervisor's urgent_steer cancellation:
// terminate the run's workflow (the signaler IS the Temporal client — its
// interface carries TerminateWorkflow), then close the gap the workflow's
// deferred terminal event leaves behind: journal run.cancelled with the
// supervisor's reason and settle the workspace row — the same heal contract
// the operator stop route follows.
func orgSteerCancelerImpl(ws *workspace.Store, sourceID string, signaler orgNoteSignaler, observationURL string, journalToken func() string, log *slog.Logger) orgSteerCanceler {
	return orgSteerCancelerFn(func(ctx context.Context, project, runID, reason string) error {
		workflowID := agent.WorkflowID(sourceID, project, runID)
		if err := signaler.TerminateWorkflow(ctx, workflowID, runID, reason); err != nil {
			detail := strings.ToLower(err.Error())
			healed := strings.Contains(detail, "not found") ||
				strings.Contains(detail, "already completed") ||
				strings.Contains(detail, "already closed") ||
				strings.Contains(detail, "already canceled") ||
				strings.Contains(detail, "already cancelled")
			if !healed {
				return err
			}
			log.Warn("org steer cancel: workflow already gone", "run", runID)
		}
		// The journal keeps the auditable why; the workspace row settles so
		// the boards and the runs list agree the run is over.
		event := observation.Event{
			Schema:     observation.Schema,
			EventID:    fmt.Sprintf("org-steer-cancel/%s/%d", runID, time.Now().UnixNano()),
			OccurredAt: time.Now().UTC(),
			Source:     observation.Source{ID: sourceID, Integration: "agent-kernel", Version: "1"},
			Context:    observation.Context{Project: project, Run: runID, Actor: observation.Actor{ID: "org-awareness", Type: "system"}},
			Type:       "run.cancelled",
			Data:       map[string]any{"reason": "supervisor_steer", "detail": reason, "actor_id": "org-awareness"},
		}
		if err := event.Validate(); err == nil {
			payload, _ := json.Marshal(map[string]any{"events": []observation.Event{event}})
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(observationURL, "/")+"/v1/observations/events", bytes.NewReader(payload))
			if reqErr == nil {
				if token := journalToken(); token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				req.Header.Set("Content-Type", "application/json")
				resp, respErr := http.DefaultClient.Do(req)
				if respErr == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
			}
		}
		_ = ws.UpdateRunStatus(ctx, runID, "cancelled", "", 0, reason)
		return nil
	})
}
