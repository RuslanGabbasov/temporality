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
	"strings"
	"time"
)

// TranscriptSizeLimit bounds one stored conversation (~1MB of text is far
// beyond any honest chat; the same ceiling as run prompts).
const TranscriptSizeLimit = 1 << 20

// Chat is one stored conversation. Transcript is the UI's messages array,
// kept as pre-marshaled JSON.
type Chat struct {
	ID         string
	ProjectID  string
	AgentID    string
	Title      string
	Transcript json.RawMessage
	UpdatedAt  time.Time
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

// UpsertChat stores (or replaces) one conversation.
func (s *Store) UpsertChat(ctx context.Context, c *Chat) error {
	if err := ValidateChat(c.ID, c.ProjectID, c.Transcript); err != nil {
		return err
	}
	c.UpdatedAt = time.Now().UTC()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_chat (id, project_id, agent_id, title, transcript, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (id) DO UPDATE SET
		   project_id = EXCLUDED.project_id, agent_id = EXCLUDED.agent_id,
		   title = EXCLUDED.title, transcript = EXCLUDED.transcript, updated_at = EXCLUDED.updated_at`,
		c.ID, c.ProjectID, c.AgentID, c.Title, []byte(c.Transcript), c.UpdatedAt)
	return err
}

// ListChats returns the project's conversations, newest first, with
// transcripts — the UI restores the full state in one call.
func (s *Store) ListChats(ctx context.Context, projectID string, limit int) ([]Chat, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, agent_id, title, transcript, updated_at
		 FROM workspace_chat WHERE project_id = $1
		 ORDER BY updated_at DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		var c Chat
		var raw []byte
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.AgentID, &c.Title, &raw, &c.UpdatedAt); err != nil {
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
