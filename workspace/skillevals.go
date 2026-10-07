package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Evaluation models (docs/living-skills.md §29): a suite is a list of cases
// stored per skill; a run records per-case outcomes for a tested version.
// Cases are intentionally declarative — expected substrings the answer must
// and must not contain — so evaluation stays reproducible and cheap. Richer
// assertions (scripts, trajectory checks) are later phases.

// EvaluationCase is one suite case: a task input plus expected answer patterns.
type EvaluationCase struct {
	Name           string   `json:"name"`
	Input          string   `json:"input"`
	MustContain    []string `json:"must_contain,omitempty"`
	MustNotContain []string `json:"must_not_contain,omitempty"`
}

// EvaluationSuite is the stored suite of a skill (one per skill, MVP).
type EvaluationSuite struct {
	SkillID   string           `json:"skill_id"`
	Cases     []EvaluationCase `json:"cases"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// EvaluationCaseResult is the outcome of one case in one run.
type EvaluationCaseResult struct {
	Name       string   `json:"name"`
	Passed     bool     `json:"passed"`
	Answer     string   `json:"answer,omitempty"`
	Missed     []string `json:"missed,omitempty"`     // must_contain entries absent from the answer
	Unexpected []string `json:"unexpected,omitempty"` // must_not_contain entries found in the answer
}

// EvaluationRun is one executed suite pass over a skill version.
type EvaluationRun struct {
	ID           int64                  `json:"id"`
	SkillID      string                 `json:"skill_id"`
	SkillVersion string                 `json:"skill_version"`
	Passed       int                    `json:"passed"`
	Failed       int                    `json:"failed"`
	Cases        []EvaluationCaseResult `json:"cases"`
	CreatedAt    time.Time              `json:"created_at"`
}

// GetEvaluationSuite returns the stored suite; an empty suite (no row) is not
// an error — callers decide whether an empty suite is runnable.
func (s *Store) GetEvaluationSuite(ctx context.Context, skillID string) (EvaluationSuite, error) {
	suite := EvaluationSuite{SkillID: skillID, Cases: []EvaluationCase{}}
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT cases, updated_at FROM workspace_skill_evaluation_suite WHERE skill_id = $1`, skillID).
		Scan(&raw, &suite.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return suite, nil
	}
	if err != nil {
		return suite, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &suite.Cases); err != nil {
			return suite, err
		}
	}
	return suite, nil
}

// SaveEvaluationSuite replaces the stored suite.
func (s *Store) SaveEvaluationSuite(ctx context.Context, skillID string, cases []EvaluationCase) error {
	if cases == nil {
		cases = []EvaluationCase{}
	}
	raw, err := json.Marshal(cases)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO workspace_skill_evaluation_suite (skill_id, cases, updated_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (skill_id) DO UPDATE SET cases = EXCLUDED.cases, updated_at = now()`,
		skillID, raw)
	return err
}

// RecordEvaluationRun stores one executed suite pass.
func (s *Store) RecordEvaluationRun(ctx context.Context, run *EvaluationRun) error {
	raw, err := json.Marshal(run.Cases)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_skill_evaluation_run (skill_id, skill_version, passed, failed, cases)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		run.SkillID, run.SkillVersion, run.Passed, run.Failed, raw).
		Scan(&run.ID, &run.CreatedAt)
}

// ListEvaluationRuns returns recent runs for a skill.
func (s *Store) ListEvaluationRuns(ctx context.Context, skillID string, limit int) ([]EvaluationRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, skill_id, skill_version, passed, failed, cases, created_at
		 FROM workspace_skill_evaluation_run WHERE skill_id = $1 ORDER BY created_at DESC LIMIT $2`, skillID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EvaluationRun
	for rows.Next() {
		var run EvaluationRun
		var raw []byte
		if err := rows.Scan(&run.ID, &run.SkillID, &run.SkillVersion, &run.Passed, &run.Failed, &raw, &run.CreatedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &run.Cases); err != nil {
				return nil, err
			}
		}
		result = append(result, run)
	}
	return result, rows.Err()
}
