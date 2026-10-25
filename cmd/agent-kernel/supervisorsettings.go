package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// Supervisor settings surface (docs/supervisor-model.md): the installation-
// wide configuration of the model that works for the administrator — judge
// evaluations, vision fallback, embeddings (reserved), and the human-attention
// estimate coefficients. Reader-gated reads; operator-gated writes with
// provider-registry validation of every model name.

// RegisterSupervisorRoutes mounts GET/PUT /v1/workspace/supervisor and the
// verdict-acknowledgement endpoint.
func RegisterSupervisorRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store, activities *agent.Activities) {
	mux.HandleFunc("GET /v1/workspace/supervisor", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		settings, err := ws.SupervisorSettings(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		// Semantic-layer health rides along (docs/supervisor-model.md §6.5):
		// the operator sees degradation without digging kernel logs.
		writeJSON(w, 200, map[string]any{
			"judge_model":  settings.JudgeModel,
			"vision_model": settings.VisionModel,
			"hab":          settings.HAB,
			"embeddings":   activities.EmbeddingsStatus(),
		})
	})

	mux.HandleFunc("PUT /v1/workspace/supervisor", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			JudgeModel  string `json:"judge_model"`
			VisionModel string `json:"vision_model"`
			HAB         struct {
				ApprovalSeconds   *int `json:"approval_seconds,omitempty"`
				AnswerBaseSeconds *int `json:"answer_base_seconds,omitempty"`
				ReadingWPM        *int `json:"reading_wpm,omitempty"`
			} `json:"hab"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		// Every non-empty model must be served by a configured provider: a
		// typo'd model would otherwise fail silently at first use.
		for name, model := range map[string]string{
			"judge_model":  req.JudgeModel,
			"vision_model": req.VisionModel,
		} {
			if strings.TrimSpace(model) == "" {
				continue
			}
			if _, ok, err := ws.ProviderOfModel(r.Context(), strings.TrimSpace(model)); err != nil {
				writeError(w, 500, err)
				return
			} else if !ok {
				writeError(w, 422, fmt.Errorf("%s: model %q is not listed by any configured provider", name, model))
				return
			}
		}
		current, err := ws.SupervisorSettings(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		// Partial update semantics: omitted HAB fields keep their stored value.
		if req.HAB.ApprovalSeconds != nil {
			current.HAB.ApprovalSeconds = *req.HAB.ApprovalSeconds
		}
		if req.HAB.AnswerBaseSeconds != nil {
			current.HAB.AnswerBaseSeconds = *req.HAB.AnswerBaseSeconds
		}
		if req.HAB.ReadingWPM != nil {
			current.HAB.ReadingWPM = *req.HAB.ReadingWPM
		}
		next := workspace.SupervisorSettings{
			JudgeModel:  req.JudgeModel,
			VisionModel: req.VisionModel,
			HAB:         current.HAB,
		}
		if err := ws.SaveSupervisorSettings(r.Context(), next); err != nil {
			writeError(w, 500, err)
			return
		}
		saved, err := ws.SupervisorSettings(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSettingChanged, "supervisor_settings", "installation", map[string]any{
			"judge_model": saved.JudgeModel, "vision_model": saved.VisionModel,
		})
		writeJSON(w, 200, saved)
	})

	// Acknowledging a verdict closes it in the problem queue without touching
	// anything the verdict proposes: the human decided — applied elsewhere,
	// dismissed, or noted. The acknowledgement is journaled, so the queue can
	// hide closed rows while keeping the audit trail.
	mux.HandleFunc("POST /v1/workspace/supervisor/verdicts/acknowledge", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			EventID string `json:"event_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		eventID := strings.TrimSpace(req.EventID)
		if eventID == "" {
			writeError(w, 422, fmt.Errorf("event_id is required"))
			return
		}
		item := observation.Event{
			Schema:     observation.Schema,
			EventID:    fmt.Sprintf("supervisor-ack/%s/%d", eventID, time.Now().UnixNano()),
			OccurredAt: time.Now().UTC(),
			Source:     observation.Source{ID: activities.SourceID, Integration: "temporality-agent-kernel", Version: "1"},
			Context:    observation.Context{Actor: observation.Actor{ID: requestUserID(r), Type: "human"}},
			Type:       "supervisor.verdict.acknowledged",
			Data:       map[string]any{"verdict_event_id": eventID},
		}
		if err := item.Validate(); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := activities.Events.Enqueue(r.Context(), item); err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSettingChanged, "supervisor_verdict", eventID, map[string]any{"acknowledged": true})
		writeJSON(w, 200, map[string]any{"acknowledged": eventID})
	})

	// Durable ack state (docs/supervisor-model.md §7): GET returns the whole
	// closed set, POST acknowledges a batch in one round-trip — the queue's
	// bulk actions were one sequential request per row and still lost rows to
	// the event-window replay. The journal keeps an audit event per batch
	// (the ids ride the data); the workspace table is the source of truth.
	mux.HandleFunc("GET /v1/workspace/supervisor/acks", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		ids, err := ws.ListSupervisorAcks(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ids": ids})
	})
	mux.HandleFunc("POST /v1/workspace/supervisor/acks", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			VerdictEventIDs []string `json:"verdict_event_ids"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		ids := make([]string, 0, len(req.VerdictEventIDs))
		for _, id := range req.VerdictEventIDs {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				ids = append(ids, trimmed)
			}
		}
		if len(ids) == 0 {
			writeError(w, 422, fmt.Errorf("verdict_event_ids is required"))
			return
		}
		if err := ws.AckSupervisorVerdicts(r.Context(), ids, requestUserID(r)); err != nil {
			writeError(w, 500, err)
			return
		}
		// One audit event per batch (ids in data), not per id — a bulk skip of
		// two hundred rows must not flood the journal it once came from.
		audit := observation.Event{
			Schema:     observation.Schema,
			EventID:    fmt.Sprintf("supervisor-ack-batch/%d", time.Now().UnixNano()),
			OccurredAt: time.Now().UTC(),
			Source:     observation.Source{ID: activities.SourceID, Integration: "temporality-agent-kernel", Version: "1"},
			Context:    observation.Context{Actor: observation.Actor{ID: requestUserID(r), Type: "human"}},
			Type:       "supervisor.verdict.acknowledged",
			Data:       map[string]any{"verdict_event_ids": ids, "bulk": len(ids) > 1},
		}
		if audit.Validate() == nil {
			_ = activities.Events.Enqueue(r.Context(), audit)
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSettingChanged, "supervisor_verdicts", fmt.Sprintf("%d", len(ids)), map[string]any{"acknowledged": true})
		writeJSON(w, 200, map[string]any{"acknowledged": ids})
	})
}
