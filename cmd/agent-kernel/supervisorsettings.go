package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// Supervisor settings surface (docs/supervisor-model.md): the installation-
// wide configuration of the model that works for the administrator — judge
// evaluations, vision fallback, embeddings (reserved), and the human-attention
// estimate coefficients. Reader-gated reads; operator-gated writes with
// provider-registry validation of every model name.

// RegisterSupervisorRoutes mounts GET/PUT /v1/workspace/supervisor.
func RegisterSupervisorRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store) {
	mux.HandleFunc("GET /v1/workspace/supervisor", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		settings, err := ws.SupervisorSettings(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, settings)
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
}
