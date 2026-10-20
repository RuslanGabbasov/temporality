package workspace

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// UserFact is one explicit durable fact or preference about a workspace user
// («предпочитает краткие ответы», «ведёт проект Omni»): the accumulated
// picture the dialogs work against. Facts are short, user-scoped and
// deduplicated by exact text; origin records who wrote it (the user settings
// screen or the intake dialog's user_remember tool).
type UserFact struct {
	ID        int64     `json:"id"`
	UserID    string    `json:"user_id"`
	Fact      string    `json:"fact"`
	Origin    string    `json:"origin,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	UserFactOriginManual = "manual"
	UserFactOriginAgent  = "agent"
	// userFactsCap bounds one user's fact list: the profile block consumes
	// the newest slice, so an unbounded stream of facts would only push the
	// live ones out of view.
	userFactsCap = 50
)

// ListUserFacts returns the user's facts, newest first.
func (s *Store) ListUserFacts(ctx context.Context, userID string) ([]UserFact, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, fact, origin, created_at FROM workspace_user_fact
		 WHERE user_id = $1 ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	facts := []UserFact{}
	for rows.Next() {
		var f UserFact
		if err := rows.Scan(&f.ID, &f.UserID, &f.Fact, &f.Origin, &f.CreatedAt); err != nil {
			return nil, err
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

// AddUserFact stores one fact, deduplicated by exact text (a repeated fact is
// a no-op returning the existing row) and capped: past the cap the oldest
// facts fall off, keeping the list a live summary rather than an archive.
func (s *Store) AddUserFact(ctx context.Context, userID, fact, origin string) (UserFact, error) {
	fact = strings.TrimSpace(fact)
	if fact == "" {
		return UserFact{}, ErrNotFound
	}
	if origin == "" {
		origin = UserFactOriginManual
	}
	var existing UserFact
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, fact, origin, created_at FROM workspace_user_fact
		 WHERE user_id = $1 AND fact = $2`, userID, fact).
		Scan(&existing.ID, &existing.UserID, &existing.Fact, &existing.Origin, &existing.CreatedAt)
	if err == nil {
		return existing, nil // exact duplicate: idempotent save
	}
	if err != pgx.ErrNoRows {
		return UserFact{}, err
	}
	// Cap: drop the oldest facts beyond the window before inserting.
	_, _ = s.pool.Exec(ctx,
		`DELETE FROM workspace_user_fact WHERE user_id = $1 AND id NOT IN (
			 SELECT id FROM workspace_user_fact WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2
		 )`, userID, userFactsCap-1)
	var f UserFact
	err = s.pool.QueryRow(ctx,
		`INSERT INTO workspace_user_fact (user_id, fact, origin) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, fact) DO UPDATE SET origin = EXCLUDED.origin
		 RETURNING id, user_id, fact, origin, created_at`,
		userID, fact, origin).
		Scan(&f.ID, &f.UserID, &f.Fact, &f.Origin, &f.CreatedAt)
	return f, err
}

// DeleteUserFact removes one fact by id; unknown ids are ErrNotFound.
func (s *Store) DeleteUserFact(ctx context.Context, userID string, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_user_fact WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
