package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// Uniform enable/disable switches (the trigger pattern) for the entities that
// lacked one: agents, skills and users. One request shape for all of them —
// POST /v1/workspace/<entity>/{id}/enabled with {"enabled": bool} — one UI
// toggle per mini-card, and an explicit audit event per flip. The regular
// entity PUTs never carry the flag, so editing a configuration cannot disable
// an entity by omission. Users map the flip onto workspace_user.active (the
// historical name of the same switch).
//
// Semantics per entity:
//   - agent:   no new runs — API starts, intake, triggers and delegation all
//     refuse; runs already in flight finish.
//   - skill:   never served to agents; runs silently degrade without it,
//     exactly like unapplied drafts.
//   - user:    cannot sign in (token and password) and drops out of delivery
//     resolution.
type enabledRequest struct {
	Enabled *bool `json:"enabled"`
}

func decodeEnabledRequest(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var req enabledRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		writeError(w, 400, err)
		return false, false
	}
	if req.Enabled == nil {
		writeError(w, 400, errors.New("enabled (boolean) is required"))
		return false, false
	}
	return *req.Enabled, true
}

func registerEntityEnabledRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store, visibleUnits func(*http.Request) []string) {
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/enabled", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		agentID := r.PathValue("agentID")
		enabled, ok := decodeEnabledRequest(w, r)
		if !ok {
			return
		}
		a, err := ws.GetAgent(r.Context(), agentID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(a.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		if err := ws.SetAgentEnabled(r.Context(), agentID, enabled); err != nil {
			writeError(w, 500, err)
			return
		}
		action := workspace.AuditAgentEnabled
		if !enabled {
			action = workspace.AuditAgentDisabled
		}
		_ = ws.RecordAccessAudit(r.Context(), requestSubject(r), action, "agent", agentID, map[string]any{"name": a.Name})
		a.Enabled = enabled
		writeJSON(w, 200, a)
	})

	mux.HandleFunc("POST /v1/workspace/skills/{skillID}/enabled", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		skillID := r.PathValue("skillID")
		enabled, ok := decodeEnabledRequest(w, r)
		if !ok {
			return
		}
		sk, err := ws.GetSkill(r.Context(), skillID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(sk.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		if err := ws.SetSkillEnabled(r.Context(), skillID, enabled); err != nil {
			writeError(w, 500, err)
			return
		}
		action := workspace.AuditSkillEnabled
		if !enabled {
			action = workspace.AuditSkillDisabled
		}
		_ = ws.RecordAccessAudit(r.Context(), requestSubject(r), action, "skill", skillID, map[string]any{"name": sk.Name})
		sk.Enabled = enabled
		writeJSON(w, 200, sk)
	})

	mux.HandleFunc("POST /v1/workspace/users/{userID}/enabled", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		userID := r.PathValue("userID")
		enabled, ok := decodeEnabledRequest(w, r)
		if !ok {
			return
		}
		u, err := ws.GetUser(r.Context(), userID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.SetUserActive(r.Context(), userID, enabled); err != nil {
			writeError(w, 500, err)
			return
		}
		action := workspace.AuditUserEnabled
		if !enabled {
			action = workspace.AuditUserDisabled
		}
		_ = ws.RecordAccessAudit(r.Context(), requestSubject(r), action, "user", userID, map[string]any{"name": u.Name})
		u.Active = enabled
		writeJSON(w, 200, u)
	})
}
