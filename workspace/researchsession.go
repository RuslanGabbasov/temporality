package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ResearchSession is a saved knowledge-explorer investigation
// (docs/human-knowledge-explorer.md §11): the research position (question,
// filters, navigation path) plus the researcher's own notes and conclusion.
// Payload holds references — filter values, area keys, knowledge ids — never
// copies of knowledge: opening a session resolves the current projection and
// the reader's current rights, so a revoked scope or a changed statement is
// honored (§11.3). Notes and conclusion are the user's words; the UI renders
// them visibly distinct from knowledge (§11.2).
type ResearchSession struct {
	ID        int64          `json:"id"`
	Project   string         `json:"project"`
	Title     string         `json:"title"`
	CreatedBy string         `json:"created_by"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// researchSessionPayloadLimit keeps a pathological save from turning the row
// into a document store: references are small by design.
const researchSessionPayloadLimit = 128 << 10

func validateResearchSessionTitle(title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("title is required")
	}
	if len(title) > 200 {
		return fmt.Errorf("title must be at most 200 characters (got %d)", len(title))
	}
	return nil
}

// ValidateResearchSession checks the create-facing invariants: non-empty
// project and title and a bounded payload. Handlers call it before the store
// so a 422 never depends on parsing store error strings.
func ValidateResearchSession(project, title string, payload map[string]any) error {
	if strings.TrimSpace(project) == "" {
		return errors.New("project is required")
	}
	if err := validateResearchSessionTitle(title); err != nil {
		return err
	}
	return validateResearchSessionPayload(payload)
}

func validateResearchSessionPayload(payload map[string]any) error {
	if payload == nil {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("payload is not serializable: %w", err)
	}
	if len(raw) > researchSessionPayloadLimit {
		return fmt.Errorf("payload is too large (max %d bytes, got %d)", researchSessionPayloadLimit, len(raw))
	}
	return nil
}

const researchSessionColumns = `id, project, title, created_by, payload, created_at, updated_at`

func scanResearchSession(row interface{ Scan(dest ...any) error }, withPayload bool) (ResearchSession, error) {
	var s ResearchSession
	var payload []byte
	if err := row.Scan(&s.ID, &s.Project, &s.Title, &s.CreatedBy, &payload, &s.CreatedAt, &s.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s, ErrNotFound
		}
		return s, err
	}
	if withPayload && len(payload) > 0 {
		_ = json.Unmarshal(payload, &s.Payload)
	}
	return s, nil
}

// CreateResearchSession persists a new session and fills ID and timestamps.
func (s *Store) CreateResearchSession(ctx context.Context, session *ResearchSession) error {
	if err := ValidateResearchSession(session.Project, session.Title, session.Payload); err != nil {
		return err
	}
	raw, err := json.Marshal(session.Payload)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_research_session (project, title, created_by, payload)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at, updated_at`,
		strings.TrimSpace(session.Project), strings.TrimSpace(session.Title), session.CreatedBy, raw,
	).Scan(&session.ID, &session.CreatedAt, &session.UpdatedAt)
}

// GetResearchSession returns one session with its payload; a missing row is
// ErrNotFound.
func (s *Store) GetResearchSession(ctx context.Context, id int64) (ResearchSession, error) {
	return scanResearchSession(s.pool.QueryRow(ctx,
		`SELECT `+researchSessionColumns+` FROM workspace_research_session WHERE id = $1`, id), true)
}

// ListResearchSessions returns the project's sessions, newest update first,
// WITHOUT payloads — the list stays light; the UI fetches a session on open.
func (s *Store) ListResearchSessions(ctx context.Context, project string, limit int) ([]ResearchSession, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+researchSessionColumns+` FROM workspace_research_session
		 WHERE project = $1 ORDER BY updated_at DESC, id DESC LIMIT $2`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := []ResearchSession{}
	for rows.Next() {
		session, err := scanResearchSession(rows, false)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// UpdateResearchSession replaces title and payload and bumps updated_at.
// Title validation applies; project stays fixed at create time.
func (s *Store) UpdateResearchSession(ctx context.Context, id int64, title string, payload map[string]any) (ResearchSession, error) {
	if err := validateResearchSessionTitle(title); err != nil {
		return ResearchSession{}, err
	}
	if err := validateResearchSessionPayload(payload); err != nil {
		return ResearchSession{}, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ResearchSession{}, err
	}
	return scanResearchSession(s.pool.QueryRow(ctx,
		`UPDATE workspace_research_session SET title = $2, payload = $3, updated_at = now()
		 WHERE id = $1 RETURNING `+researchSessionColumns, id, strings.TrimSpace(title), raw), true)
}

// DeleteResearchSession removes a session; deleting a missing row is
// ErrNotFound.
func (s *Store) DeleteResearchSession(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_research_session WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
