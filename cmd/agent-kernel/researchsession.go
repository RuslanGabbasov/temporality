package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// Research-session HTTP surface (docs/human-knowledge-explorer.md §11): the
// knowledge explorer's saved investigations. A session stores the research
// position (question, filters, nav path) and the researcher's notes and
// conclusion as references; the client re-resolves the current projection on
// open, so nothing here snapshots knowledge. Readers list and read, writers
// create, update and delete — like the other workspace entities.

// researchSessionStore is the storage surface this router needs;
// *workspace.Store satisfies it and tests provide fakes.
type researchSessionStore interface {
	CreateResearchSession(ctx context.Context, session *workspace.ResearchSession) error
	GetResearchSession(ctx context.Context, id int64) (workspace.ResearchSession, error)
	ListResearchSessions(ctx context.Context, project string, limit int) ([]workspace.ResearchSession, error)
	UpdateResearchSession(ctx context.Context, id int64, title string, payload map[string]any) (workspace.ResearchSession, error)
	DeleteResearchSession(ctx context.Context, id int64) error
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
}

type researchSessionRequest struct {
	Project string         `json:"project"`
	Title   string         `json:"title"`
	Payload map[string]any `json:"payload"`
}

func parseResearchSessionID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("session id must be a positive number")
	}
	return id, nil
}

func researchSessionErrorStatus(err error) int {
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return 404
	case strings.Contains(err.Error(), "title"), strings.Contains(err.Error(), "payload"),
		strings.Contains(err.Error(), "project"):
		return 422
	default:
		return 500
	}
}

func RegisterResearchSessionRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws researchSessionStore) {
	// List a project's sessions, newest first. Payloads are not included:
	// the list stays light and the UI fetches a session when it opens one.
	mux.HandleFunc("GET /v1/workspace/research-sessions", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		project := strings.TrimSpace(r.URL.Query().Get("project"))
		if project == "" {
			writeError(w, 400, errors.New("project is required"))
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				limit = parsed
			}
		}
		sessions, err := ws.ListResearchSessions(r.Context(), project, limit)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if sessions == nil {
			sessions = []workspace.ResearchSession{}
		}
		writeJSON(w, 200, map[string]any{"sessions": sessions})
	})

	// Create a session: 201 with the stored row (server timestamps, id).
	mux.HandleFunc("POST /v1/workspace/research-sessions", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req researchSessionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := workspace.ValidateResearchSession(req.Project, req.Title, req.Payload); err != nil {
			writeError(w, 422, err)
			return
		}
		session := workspace.ResearchSession{
			Project: req.Project, Title: req.Title, CreatedBy: requestUserID(r), Payload: req.Payload,
		}
		if err := ws.CreateResearchSession(r.Context(), &session); err != nil {
			writeError(w, researchSessionErrorStatus(err), err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), session.CreatedBy, workspace.AuditResearchSessionCreated, "research_session", strconv.FormatInt(session.ID, 10), map[string]any{
			"project": session.Project, "title": session.Title,
		})
		writeJSON(w, 201, session)
	})

	// One session with its payload — the reader resolves it against the
	// CURRENT projection and CURRENT rights (§11.3).
	mux.HandleFunc("GET /v1/workspace/research-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		id, err := parseResearchSessionID(r)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		session, err := ws.GetResearchSession(r.Context(), id)
		if err != nil {
			writeError(w, researchSessionErrorStatus(err), err)
			return
		}
		writeJSON(w, 200, session)
	})

	// Replace title and payload (full snapshot per save — the client always
	// sends the complete research position).
	mux.HandleFunc("PUT /v1/workspace/research-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		id, err := parseResearchSessionID(r)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		var req researchSessionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		session, err := ws.UpdateResearchSession(r.Context(), id, req.Title, req.Payload)
		if err != nil {
			writeError(w, researchSessionErrorStatus(err), err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditResearchSessionUpdated, "research_session", strconv.FormatInt(id, 10), map[string]any{
			"title": session.Title,
		})
		writeJSON(w, 200, session)
	})

	mux.HandleFunc("DELETE /v1/workspace/research-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		id, err := parseResearchSessionID(r)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		if err := ws.DeleteResearchSession(r.Context(), id); err != nil {
			writeError(w, researchSessionErrorStatus(err), err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditResearchSessionDeleted, "research_session", strconv.FormatInt(id, 10), nil)
		writeJSON(w, 200, map[string]any{"deleted": true})
	})
}
