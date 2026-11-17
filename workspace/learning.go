package workspace

// Learning execution and evidence storage (docs/ai-guided-onboarding-and-
// learning.md §4/§11): programs and sessions are per-user execution state,
// progress is per-user-per-capability evidence. The lesson DEFINITIONS live
// in code (cmd/agent-kernel/learningcatalog.go — the catalog ships with the
// feature), so this store never mixes methodology with personal history.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// LearningProgramItem is one lesson slot inside a program.
type LearningProgramItem struct {
	LessonID    string     `json:"lesson_id"`
	State       string     `json:"state"` // pending|scheduled|available|in_progress|completed|skipped|not_applicable
	Why         string     `json:"why,omitempty"`
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
}

// LearningProgram is the per-user learning plan (§4.1).
type LearningProgram struct {
	ID        string                `json:"id"`
	UserID    string                `json:"user_id"`
	Kind      string                `json:"kind"` // initial_onboarding|feature_introduction|contextual|recommended|user_initiated
	Goal      string                `json:"goal,omitempty"`
	Status    string                `json:"status"` // active|paused|completed|cancelled
	Items     []LearningProgramItem `json:"items"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

// LearningSession is one lesson run (§4.4). The step index and history live
// server-side, so a session survives browser restarts (§16.4).
type LearningSession struct {
	ID        string            `json:"id"`
	UserID    string            `json:"user_id"`
	LessonID  string            `json:"lesson_id"`
	ProgramID string            `json:"program_id,omitempty"`
	Status    string            `json:"status"` // active|waiting_for_user|paused|completed|aborted
	StepIndex int               `json:"step_index"`
	History   []json.RawMessage `json:"history,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// LearningProgress is the capability evidence (§4.5): lesson completion and
// actual mastery are independent facts.
type LearningProgress struct {
	UserID     string    `json:"user_id"`
	FeatureID  string    `json:"feature_id"`
	LessonID   string    `json:"lesson_id,omitempty"`
	Completion bool      `json:"completion"`
	Mastery    string    `json:"mastery"` // unknown|introduced|practiced|verified|needs_refresh
	Source     string    `json:"source,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

func programJSON(items []LearningProgramItem) []byte {
	raw, _ := json.Marshal(items)
	return raw
}

// UpsertLearningProgram creates or refreshes a user's program. Completed
// lesson states are carried over onto the new item list (§4.1 — a rebuild
// keeps the completion history).
func (s *Store) UpsertLearningProgram(ctx context.Context, p *LearningProgram) error {
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	var prevID string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO learning_program (id, user_id, kind, goal, status, items, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (user_id) DO UPDATE SET kind = EXCLUDED.kind, goal = EXCLUDED.goal,
		   status = 'active', items = EXCLUDED.items, updated_at = now()
		 RETURNING (SELECT id FROM learning_program WHERE user_id = EXCLUDED.user_id)`,
		p.ID, p.UserID, p.Kind, p.Goal, p.Status, programJSON(p.Items), now, now).Scan(&prevID)
	if err != nil {
		return err
	}
	p.ID = prevID
	return nil
}

// GetLearningProgram returns the user's current program (nil = none yet).
func (s *Store) GetLearningProgram(ctx context.Context, userID string) (*LearningProgram, error) {
	var p LearningProgram
	var items []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, kind, goal, status, items, created_at, updated_at
		 FROM learning_program WHERE user_id = $1 AND status <> 'cancelled' ORDER BY updated_at DESC LIMIT 1`, userID).
		Scan(&p.ID, &p.UserID, &p.Kind, &p.Goal, &p.Status, &items, &p.CreatedAt, &p.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(items, &p.Items)
	return &p, nil
}

// SetLearningProgramStatus flips the program switch (pause/resume/cancel).
func (s *Store) SetLearningProgramStatus(ctx context.Context, userID, status string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE learning_program SET status = $2, updated_at = now() WHERE user_id = $1`, userID, status)
	return err
}

// SetLearningProgramItemState marks one lesson slot inside the program.
func (s *Store) SetLearningProgramItemState(ctx context.Context, programID, lessonID, state string) error {
	var items []byte
	err := s.pool.QueryRow(ctx,
		`SELECT items FROM learning_program WHERE id = $1`, programID).Scan(&items)
	if err != nil {
		return err
	}
	var list []LearningProgramItem
	if err := json.Unmarshal(items, &list); err != nil {
		return err
	}
	for i := range list {
		if list[i].LessonID == lessonID {
			list[i].State = state
			break
		}
	}
	raw, _ := json.Marshal(list)
	_, err = s.pool.Exec(ctx,
		`UPDATE learning_program SET items = $2, updated_at = now() WHERE id = $1`, programID, raw)
	return err
}

// StartLearningSession opens (or resumes) a lesson run for the user.
func (s *Store) StartLearningSession(ctx context.Context, session *LearningSession) error {
	now := time.Now().UTC()
	session.CreatedAt = now
	session.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO learning_session (id, user_id, lesson_id, program_id, status, step_index, history, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9)`,
		session.ID, session.UserID, session.LessonID, session.ProgramID, session.Status,
		session.StepIndex, session.History, now, now)
	return err
}

// GetLearningSession loads one session.
func (s *Store) GetLearningSession(ctx context.Context, id string) (*LearningSession, error) {
	var sess LearningSession
	var history []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, lesson_id, program_id, status, step_index, history, created_at, updated_at
		 FROM learning_session WHERE id = $1`, id).
		Scan(&sess.ID, &sess.UserID, &sess.LessonID, &sess.ProgramID, &sess.Status, &sess.StepIndex, &history, &sess.CreatedAt, &sess.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(history, &sess.History)
	return &sess, nil
}

// GetCurrentLearningSession returns the user's active (or paused) session —
// the resume path after a browser or backend restart (§16.4).
func (s *Store) GetCurrentLearningSession(ctx context.Context, userID string) (*LearningSession, error) {
	var sess LearningSession
	var history []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, lesson_id, program_id, status, step_index, history, created_at, updated_at
		 FROM learning_session WHERE user_id = $1 AND status IN ('active','waiting_for_user','paused')
		 ORDER BY updated_at DESC LIMIT 1`, userID).
		Scan(&sess.ID, &sess.UserID, &sess.LessonID, &sess.ProgramID, &sess.Status, &sess.StepIndex, &history, &sess.CreatedAt, &sess.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(history, &sess.History)
	return &sess, nil
}

// UpdateLearningSession advances the step, appends a history entry and
// updates the status in one statement (single-writer per session).
func (s *Store) UpdateLearningSession(ctx context.Context, id string, stepIndex, status string, entry json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE learning_session SET
		   step_index = $2,
		   status = CASE WHEN $3 = '' THEN status ELSE $3::text END,
		   history = history || $4::jsonb,
		   updated_at = now()
		 WHERE id = $1`, id, stepIndex, status, entry)
	return err
}

// SetLearningSessionStatus flips the session switch without a history entry.
func (s *Store) SetLearningSessionStatus(ctx context.Context, id, status string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE learning_session SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	return err
}

// UpsertLearningProgress records one capability evidence fact.
func (s *Store) UpsertLearningProgress(ctx context.Context, p *LearningProgress) error {
	p.CheckedAt = time.Now().UTC()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO learning_progress (user_id, feature_id, lesson_id, completion, mastery, source, detail, checked_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (user_id, feature_id) DO UPDATE SET
		   lesson_id = EXCLUDED.lesson_id, completion = EXCLUDED.completion,
		   mastery = EXCLUDED.mastery, source = EXCLUDED.source, detail = EXCLUDED.detail, checked_at = now()`,
		p.UserID, p.FeatureID, p.LessonID, p.Completion, p.Mastery, p.Source, p.Detail, p.CheckedAt)
	return err
}

// ListLearningProgress returns the user's capability evidence.
func (s *Store) ListLearningProgress(ctx context.Context, userID string) ([]LearningProgress, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, feature_id, lesson_id, completion, mastery, source, detail, checked_at
	 FROM learning_progress WHERE user_id = $1 ORDER BY checked_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LearningProgress{}
	for rows.Next() {
		var p LearningProgress
		if err := rows.Scan(&p.UserID, &p.FeatureID, &p.LessonID, &p.Completion, &p.Mastery, &p.Source, &p.Detail, &p.CheckedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LearningSuggestion is a P3 recommendation slot: one feature suggested to one
// user, with the frequency-cap timestamp (§9.2 — never a notification flood).
type LearningSuggestion struct {
	UserID    string    `json:"user_id"`
	FeatureID string    `json:"feature_id"`
	Status    string    `json:"status"` // suggested|dismissed|accepted
	At        time.Time `json:"at"`
}

// UpsertLearningSuggestion records (or refreshes) one proactive suggestion.
func (s *Store) UpsertLearningSuggestion(ctx context.Context, sugg LearningSuggestion) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO learning_suggestion (user_id, feature_id, status, at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (user_id, feature_id) DO UPDATE SET status = EXCLUDED.status, at = now()`,
		sugg.UserID, sugg.FeatureID, sugg.Status)
	return err
}

// ListLearningSuggestions returns the user's suggestion slots with statuses
// and timestamps (the planner's «предыдущие рекомендации» input, §5.4).
func (s *Store) ListLearningSuggestions(ctx context.Context, userID string) ([]LearningSuggestion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, feature_id, status, at FROM learning_suggestion WHERE user_id = $1 ORDER BY at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LearningSuggestion{}
	for rows.Next() {
		var sugg LearningSuggestion
		if err := rows.Scan(&sugg.UserID, &sugg.FeatureID, &sugg.Status, &sugg.At); err != nil {
			return nil, err
		}
		out = append(out, sugg)
	}
	return out, rows.Err()
}
