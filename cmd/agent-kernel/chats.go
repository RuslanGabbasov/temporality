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
	GetChat(ctx context.Context, id string) (workspace.Chat, error)
	UpsertChat(ctx context.Context, c *workspace.Chat) error
	ListChats(ctx context.Context, projectID string, limit int, scope workspace.ChatScope) ([]workspace.Chat, error)
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
		// Ownership scoping (docs/resilience-and-scale.md §4): everyone lists
		// their OWN chats — a shared wall leaked every user's conversations
		// into every sidebar. Unattributed legacy rows (owner_user_id = '') are
		// visible ONLY through ?all=1 (admin debugging escape): folding them
		// into every admin's sidebar re-imported foreign conversations after
		// the ownership fix — env-token clients of the pre-fallback era left
		// dozens of ownerless rows that all landed in the admin's list.
		scope := workspace.ChatScope{Owners: []string{requestUserID(r)}}
		if principal, ok := controlplane.FromContext(r.Context()); ok && principal.Role >= controlplane.RoleAdmin {
			if r.URL.Query().Get("all") == "1" {
				scope = workspace.ChatScope{All: true}
			}
		}
		chats, err := ws.ListChats(r.Context(), project, limit, scope)
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
			TaskID     string          `json:"task_id"`
			Title      string          `json:"title"`
			Transcript json.RawMessage `json:"transcript"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, workspace.TranscriptSizeLimit+(1<<16))).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		chat := &workspace.Chat{
			ID: id, ProjectID: req.ProjectID, AgentID: req.AgentID, TaskID: req.TaskID,
			Title: req.Title, Transcript: req.Transcript,
		}
		// Ownership: the creator is stamped on insert and immutable; a later
		// upsert from someone else (or an admin supporting) updates content but
		// never steals the chat. Foreign non-admin writes are refused.
		owner := requestUserID(r)
		if existing, err := ws.GetChat(r.Context(), id); err == nil {
			if existing.OwnerUserID != "" && existing.OwnerUserID != owner {
				if principal, ok := controlplane.FromContext(r.Context()); !ok || principal.Role < controlplane.RoleAdmin {
					writeError(w, 403, errors.New("this chat belongs to another user"))
					return
				}
			}
			chat.OwnerUserID = existing.OwnerUserID
		} else {
			chat.OwnerUserID = owner
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
		id := r.PathValue("id")
		// Ownership: deleting someone else's chat (it used to be possible —
		// the foreign copy was in the sidebar and the delete hit the shared
		// row) removes THEIR conversation. Owner or admin only.
		if existing, err := ws.GetChat(r.Context(), id); err == nil {
			if existing.OwnerUserID != "" && existing.OwnerUserID != requestUserID(r) {
				if principal, ok := controlplane.FromContext(r.Context()); !ok || principal.Role < controlplane.RoleAdmin {
					writeError(w, 403, errors.New("this chat belongs to another user"))
					return
				}
			}
		} else if !errors.Is(err, workspace.ErrNotFound) {
			writeError(w, 500, err)
			return
		}
		if err := ws.DeleteChat(r.Context(), id); err != nil {
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
