package workspace

// Personal dashboard storage (docs/system-dashboard.md, «Мой день»): the
// supervisor's daily brief per user and the personal GTD todo list. Both are
// strictly per-user — every query filters by user_id, there is no cross-user
// read path here.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// UserDailyBrief is one generated day brief. Payload is the marshalled
// UserBriefPayload (kernel-side type): the narrative summary plus the factual
// snapshot the UI renders.
type UserDailyBrief struct {
	UserID      string          `json:"user_id"`
	Day         string          `json:"day"` // YYYY-MM-DD (UTC)
	Payload     json.RawMessage `json:"payload"`
	GeneratedAt time.Time       `json:"generated_at"`
}

// UpsertUserDailyBrief stores (or replaces) the brief for one (user, day).
func (s *Store) UpsertUserDailyBrief(ctx context.Context, b *UserDailyBrief) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_daily_brief (user_id, day, payload, generated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (user_id, day) DO UPDATE SET
		   payload = EXCLUDED.payload, generated_at = now()`,
		b.UserID, b.Day, []byte(b.Payload))
	return err
}

// GetUserDailyBrief returns the stored brief for one (user, day); nil, nil
// when none is stored yet (the caller decides whether to generate).
func (s *Store) GetUserDailyBrief(ctx context.Context, userID, day string) (*UserDailyBrief, error) {
	var b UserDailyBrief
	err := s.pool.QueryRow(ctx,
		`SELECT user_id, day, payload, generated_at FROM user_daily_brief WHERE user_id = $1 AND day = $2`,
		userID, day).
		Scan(&b.UserID, &b.Day, &b.Payload, &b.GeneratedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// UserTodo is one personal todo note, clustered by a GTD group. Due is the
// optional due date (UTC midnight from the DATE column) feeding the «Сегодня»
// section and the brief's focus; ReturnGroup remembers where a todo lived
// before the checkbox moved it to done, so unchecking restores it.
type UserTodo struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Group       string     `json:"group"` // inbox | next | waiting | someday | done
	Text        string     `json:"text"`
	Note        string     `json:"note,omitempty"`
	Due         *time.Time `json:"due,omitempty"`
	ReturnGroup string     `json:"return_group,omitempty"`
	Sort        int        `json:"sort"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ListUserTodos returns the caller's todos ordered for the widget: GTD group
// first, then manual sort, then newest.
func (s *Store) ListUserTodos(ctx context.Context, userID string) ([]UserTodo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, grp, text, note, due, return_grp, sort, created_at, updated_at
		 FROM user_todo WHERE user_id = $1
		 ORDER BY grp, sort, created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []UserTodo{}
	for rows.Next() {
		var t UserTodo
		if err := rows.Scan(&t.ID, &t.UserID, &t.Group, &t.Text, &t.Note, &t.Due, &t.ReturnGroup, &t.Sort, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// CreateUserTodo stores a new todo. The row must carry its id (the kernel
// generates one), owner and a non-empty text.
func (s *Store) CreateUserTodo(ctx context.Context, t *UserTodo) error {
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_todo (id, user_id, grp, text, note, due, return_grp, sort, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		t.ID, t.UserID, t.Group, t.Text, t.Note, t.Due, t.ReturnGroup, t.Sort, t.CreatedAt, t.UpdatedAt)
	return err
}

// UpdateUserTodo rewrites group/text/note/due/sort of one OWN todo: the WHERE
// keeps user_id, so a forged id of another user's row updates nothing.
func (s *Store) UpdateUserTodo(ctx context.Context, t *UserTodo) error {
	t.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE user_todo SET grp = $3, text = $4, note = $5, due = $6, return_grp = $7, sort = $8, updated_at = $9
		 WHERE id = $1 AND user_id = $2`,
		t.ID, t.UserID, t.Group, t.Text, t.Note, t.Due, t.ReturnGroup, t.Sort, t.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUserTodo removes one OWN todo; another user's id deletes nothing.
func (s *Store) DeleteUserTodo(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM user_todo WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
