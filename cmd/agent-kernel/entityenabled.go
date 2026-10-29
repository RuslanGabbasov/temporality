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

	mux.HandleFunc("POST /v1/workspace/teams/{teamID}/enabled", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		teamID := r.PathValue("teamID")
		enabled, ok := decodeEnabledRequest(w, r)
		if !ok {
			return
		}
		team, err := ws.GetTeam(r.Context(), teamID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(team.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		if err := ws.SetTeamEnabled(r.Context(), teamID, enabled); err != nil {
			writeError(w, 500, err)
			return
		}
		action := workspace.AuditTeamEnabled
		if !enabled {
			action = workspace.AuditTeamDisabled
		}
		_ = ws.RecordAccessAudit(r.Context(), requestSubject(r), action, "team", teamID, map[string]any{"name": team.Name})
		team.Enabled = enabled
		writeJSON(w, 200, team)
	})

	mux.HandleFunc("POST /v1/workspace/mcp-servers/{serverID}/enabled", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		serverID := r.PathValue("serverID")
		enabled, ok := decodeEnabledRequest(w, r)
		if !ok {
			return
		}
		server, err := ws.GetMCPServer(r.Context(), serverID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(server.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		if err := ws.SetMCPServerEnabled(r.Context(), serverID, enabled); err != nil {
			writeError(w, 500, err)
			return
		}
		action := workspace.AuditMCPEnabled
		if !enabled {
			action = workspace.AuditMCPDisabled
		}
		_ = ws.RecordAccessAudit(r.Context(), requestSubject(r), action, "mcp_server", serverID, map[string]any{"name": server.Name})
		server.Enabled = enabled
		writeJSON(w, 200, server)
	})

	mux.HandleFunc("POST /v1/workspace/secrets/{secretID}/enabled", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		secretID := r.PathValue("secretID")
		enabled, ok := decodeEnabledRequest(w, r)
		if !ok {
			return
		}
		secret, err := ws.GetSecret(r.Context(), secretID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(secret.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		if err := ws.SetSecretEnabled(r.Context(), secretID, enabled); err != nil {
			writeError(w, 500, err)
			return
		}
		action := workspace.AuditSecretEnabled
		if !enabled {
			action = workspace.AuditSecretDisabled
		}
		_ = ws.RecordAccessAudit(r.Context(), requestSubject(r), action, "secret", secretID, map[string]any{"name": secret.Name})
		secret.Enabled = enabled
		writeJSON(w, 200, secret)
	})

	mux.HandleFunc("POST /v1/workspace/providers/{providerID}/enabled", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		providerID := r.PathValue("providerID")
		enabled, ok := decodeEnabledRequest(w, r)
		if !ok {
			return
		}
		provider, err := ws.GetProvider(r.Context(), providerID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(provider.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		if err := ws.SetProviderEnabled(r.Context(), providerID, enabled); err != nil {
			writeError(w, 500, err)
			return
		}
		action := workspace.AuditProviderEnabled
		if !enabled {
			action = workspace.AuditProviderDisabled
		}
		_ = ws.RecordAccessAudit(r.Context(), requestSubject(r), action, "provider", providerID, map[string]any{"name": provider.Name})
		provider.Enabled = enabled
		writeJSON(w, 200, provider)
	})

	mux.HandleFunc("POST /v1/workspace/sandbox-profiles/{profileID}/enabled", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		profileID := r.PathValue("profileID")
		enabled, ok := decodeEnabledRequest(w, r)
		if !ok {
			return
		}
		profile, err := ws.GetSandboxProfile(r.Context(), profileID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.SetSandboxProfileEnabled(r.Context(), profileID, enabled); err != nil {
			writeError(w, 500, err)
			return
		}
		action := workspace.AuditSandboxEnabled
		if !enabled {
			action = workspace.AuditSandboxDisabled
		}
		_ = ws.RecordAccessAudit(r.Context(), requestSubject(r), action, "sandbox_profile", profileID, map[string]any{"name": profile.Name})
		profile.Enabled = enabled
		writeJSON(w, 200, profile)
	})
}
