package main

// Chat persistence routes (docs/resilience-and-scale.md §4): the UI pushes
// each conversation here after changes and restores from here on any
// device. The transcript is opaque JSON owned by the UI; the backend only
// bounds its size. Writer-gated: anyone who can start runs can keep their
// chats; list scope follows the project filter like other workspace reads.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// chatsStore is the storage surface behind the chat routes (fakes in tests).
type chatsStore interface {
	UpsertChat(ctx context.Context, c *workspace.Chat) error
	ListChats(ctx context.Context, projectID string, limit int) ([]workspace.Chat, error)
	DeleteChat(ctx context.Context, id string) error
}

func RegisterChatRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws chatsStore) {
	mux.HandleFunc("GET /v1/workspace/chats", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		chats, err := ws.ListChats(r.Context(), project, limit)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if chats == nil {
			chats = []workspace.Chat{}
		}
		writeJSON(w, 200, map[string]any{"chats": chats})
	})

	mux.HandleFunc("PUT /v1/workspace/chats/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		id := r.PathValue("id")
		var req struct {
			ProjectID  string          `json:"project_id"`
			AgentID    string          `json:"agent_id"`
			Title      string          `json:"title"`
			Transcript json.RawMessage `json:"transcript"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, workspace.TranscriptSizeLimit+(1<<16))).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		chat := &workspace.Chat{
			ID: id, ProjectID: req.ProjectID, AgentID: req.AgentID,
			Title: req.Title, Transcript: req.Transcript,
		}
		if err := ws.UpsertChat(r.Context(), chat); err != nil {
			writeError(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "updated_at": chat.UpdatedAt})
	})

	mux.HandleFunc("DELETE /v1/workspace/chats/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		if err := ws.DeleteChat(r.Context(), r.PathValue("id")); err != nil {
			writeSandboxLikeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
}

// writeSandboxLikeError is writeSandboxProfileError's mapping reused for any
// store error surface: not-found → 404, everything else → 400.
func writeSandboxLikeError(w http.ResponseWriter, err error) {
	if errors.Is(err, workspace.ErrNotFound) {
		writeError(w, 404, err)
		return
	}
	writeError(w, 400, err)
}
