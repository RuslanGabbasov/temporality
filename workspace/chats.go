package workspace

// Workspace chats (docs/resilience-and-scale.md §4): the durable,
// server-side copy of UI chat conversations. The transcript shape belongs
// to the UI — the backend stores it opaquely as JSON and never interprets
// it. Chats sync from the browser after every change and restore from here
// on any device, so a browser data wipe is no longer data loss.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// TranscriptSizeLimit bounds one stored conversation (~1MB of text is far
// beyond any honest chat; the same ceiling as run prompts).
const TranscriptSizeLimit = 1 << 20

// Chat is one stored conversation. Transcript is the UI's messages array,
// kept as pre-marshaled JSON. TaskID links the chat to its task row so the
// restored copy can rejoin task-scoped state (live-run badges). OwnerUserID
// is the chat's creator (docs/resilience-and-scale.md §4, ownership scoping):
// the listing serves each user their own chats only — a project-shared chat
// wall was leaking everyone's conversations into every sidebar.
type Chat struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	AgentID     string          `json:"agent_id"`
	TaskID      string          `json:"task_id"`
	OwnerUserID string          `json:"owner_user_id,omitempty"`
	Title       string          `json:"title"`
	Transcript  json.RawMessage `json:"transcript"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// ChatScope bounds the chat listing: All (admins debugging with ?all=1),
// the caller's own rows, and — for admins — unattributed legacy rows so
// nothing orphans invisibly. The zero scope returns nothing.
type ChatScope struct {
	All              bool
	Owners           []string
	IncludeOwnerless bool
}

// ValidateChat enforces the storage contract before any write.
func ValidateChat(id, projectID string, transcript json.RawMessage) error {
	if strings.TrimSpace(id) == "" || len(id) > 128 {
		return fmt.Errorf("chat id must be 1..128 characters")
	}
	if strings.TrimSpace(projectID) == "" {
		return errors.New("project_id is required")
	}
	if !json.Valid(transcript) {
		return errors.New("transcript must be valid JSON")
	}
	if len(transcript) > TranscriptSizeLimit {
		return fmt.Errorf("transcript exceeds %d bytes", TranscriptSizeLimit)
	}
	return nil
}

// GetChat loads one conversation row (ownership checks on update/delete).
func (s *Store) GetChat(ctx context.Context, id string) (Chat, error) {
	var c Chat
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, agent_id, COALESCE(task_id, ''), COALESCE(owner_user_id, ''), title, transcript, updated_at
		 FROM workspace_chat WHERE id = $1`, id).
		Scan(&c.ID, &c.ProjectID, &c.AgentID, &c.TaskID, &c.OwnerUserID, &c.Title, &c.Transcript, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	return c, nil
}

// UpsertChat stores (or replaces) one conversation. The owner is stamped on
// insert and NEVER rewritten on update — an upsert from anyone else must not
// hijack ownership (the route layer already refuses non-owner writes).
func (s *Store) UpsertChat(ctx context.Context, c *Chat) error {
	if err := ValidateChat(c.ID, c.ProjectID, c.Transcript); err != nil {
		return err
	}
	c.UpdatedAt = time.Now().UTC()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_chat (id, project_id, agent_id, task_id, owner_user_id, title, transcript, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (id) DO UPDATE SET
		   project_id = EXCLUDED.project_id, agent_id = EXCLUDED.agent_id,
		   task_id = EXCLUDED.task_id, title = EXCLUDED.title,
		   transcript = EXCLUDED.transcript, updated_at = EXCLUDED.updated_at`,
		c.ID, c.ProjectID, c.AgentID, c.TaskID, c.OwnerUserID, c.Title, []byte(c.Transcript), c.UpdatedAt)
	return err
}

// ListChats returns the project's conversations per the ownership scope,
// newest first, with transcripts — the UI restores the full state in one call.
func (s *Store) ListChats(ctx context.Context, projectID string, limit int, scope ChatScope) ([]Chat, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `SELECT id, project_id, agent_id, COALESCE(task_id, ''), COALESCE(owner_user_id, ''), title, transcript, updated_at
		 FROM workspace_chat WHERE project_id = $1`
	args := []any{projectID}
	switch {
	case scope.All:
		// no owner constraint
	case len(scope.Owners) > 0 || scope.IncludeOwnerless:
		args = append(args, scope.Owners)
		ownerFilter := ` AND (owner_user_id = ANY($2)`
		if scope.IncludeOwnerless {
			ownerFilter += ` OR owner_user_id = ''`
		}
		query += ownerFilter + `)`
	default:
		return []Chat{}, nil
	}
	args = append(args, limit)
	query += ` ORDER BY updated_at DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		var c Chat
		var raw []byte
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.AgentID, &c.TaskID, &c.OwnerUserID, &c.Title, &raw, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Transcript = json.RawMessage(raw)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteChat removes one conversation (the UI's delete action).
func (s *Store) DeleteChat(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_chat WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
