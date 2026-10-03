package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Skill is a Living Skill: a versioned capability (SKILL.md + skill.yaml
// manifest). Manifest is stored as parsed JSON; versions are immutable.
// Skills are workspace-global: agents bind them across every project; where
// a skill ran is recorded per execution in SkillExecution.ProjectID.
type Skill struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Markdown    string          `json:"markdown"`
	Manifest    json.RawMessage `json:"manifest"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// SkillVersion is an immutable snapshot of a skill's content.
type SkillVersion struct {
	SkillID   string          `json:"skill_id"`
	Version   string          `json:"version"`
	Markdown  string          `json:"markdown"`
	Manifest  json.RawMessage `json:"manifest"`
	CreatedAt time.Time       `json:"created_at"`
}

// SkillExecution links a run to the exact skill version used.
// Status is derived from the run; this row is the provenance record.
type SkillExecution struct {
	ID           int64     `json:"id"`
	SkillID      string    `json:"skill_id"`
	SkillVersion string    `json:"skill_version"`
	ProjectID    string    `json:"project_id"`
	RunID        string    `json:"run_id"`
	AgentID      string    `json:"agent_id"`
	StartedAt    time.Time `json:"started_at"`
}

// Skills

func (s *Store) CreateSkill(ctx context.Context, sk *Skill) error {
	now := time.Now().UTC()
	sk.CreatedAt = now
	sk.UpdatedAt = now
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_skill (id, name, description, version, markdown, manifest, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sk.ID, sk.Name, sk.Description, sk.Version, sk.Markdown, manifestOrNull(sk.Manifest), sk.CreatedAt, sk.UpdatedAt); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_skill_version (skill_id, version, markdown, manifest, created_at) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (skill_id, version) DO NOTHING`,
		sk.ID, sk.Version, sk.Markdown, manifestOrNull(sk.Manifest), now)
	return err
}

func (s *Store) GetSkill(ctx context.Context, id string) (Skill, error) {
	var sk Skill
	var manifest []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, version, markdown, manifest, created_at, updated_at FROM workspace_skill WHERE id = $1`, id).
		Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Version, &sk.Markdown, &manifest, &sk.CreatedAt, &sk.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return sk, ErrNotFound
	}
	sk.Manifest = rawMessage(manifest)
	return sk, err
}

func (s *Store) ListAllSkills(ctx context.Context) ([]Skill, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, description, version, markdown, manifest, created_at, updated_at
			 FROM workspace_skill ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSkills(rows)
}

func scanSkills(rows pgx.Rows) ([]Skill, error) {
	var result []Skill
	for rows.Next() {
		var sk Skill
		var manifest []byte
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Version, &sk.Markdown, &manifest, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, err
		}
		sk.Manifest = rawMessage(manifest)
		result = append(result, sk)
	}
	return result, rows.Err()
}

// UpdateSkill updates the current revision and records a new immutable version
// when the content actually changed. Version bumps are the caller's decision;
// if the caller keeps the same version string, the version row is refreshed
// only when nothing changed since (ON CONFLICT DO NOTHING keeps the original).
func (s *Store) UpdateSkill(ctx context.Context, sk *Skill) error {
	sk.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_skill SET name = $2, description = $3, version = $4, markdown = $5, manifest = $6, updated_at = $7 WHERE id = $1`,
		sk.ID, sk.Name, sk.Description, sk.Version, sk.Markdown, manifestOrNull(sk.Manifest), sk.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO workspace_skill_version (skill_id, version, markdown, manifest, created_at) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (skill_id, version) DO NOTHING`,
		sk.ID, sk.Version, sk.Markdown, manifestOrNull(sk.Manifest), sk.UpdatedAt)
	return err
}

func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_skill WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Versions

func (s *Store) ListSkillVersions(ctx context.Context, skillID string) ([]SkillVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT skill_id, version, markdown, manifest, created_at FROM workspace_skill_version WHERE skill_id = $1 ORDER BY created_at DESC`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SkillVersion
	for rows.Next() {
		var v SkillVersion
		var manifest []byte
		if err := rows.Scan(&v.SkillID, &v.Version, &v.Markdown, &manifest, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Manifest = rawMessage(manifest)
		result = append(result, v)
	}
	return result, rows.Err()
}

// Executions

func (s *Store) RecordSkillExecution(ctx context.Context, e *SkillExecution) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_skill_execution (skill_id, skill_version, project_id, run_id, agent_id, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		e.SkillID, e.SkillVersion, e.ProjectID, e.RunID, e.AgentID, e.StartedAt)
	return err
}

func (s *Store) ListSkillExecutions(ctx context.Context, skillID string, limit int) ([]SkillExecution, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, skill_id, skill_version, project_id, run_id, agent_id, started_at
		 FROM workspace_skill_execution WHERE skill_id = $1 ORDER BY started_at DESC LIMIT $2`, skillID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SkillExecution
	for rows.Next() {
		var e SkillExecution
		if err := rows.Scan(&e.ID, &e.SkillID, &e.SkillVersion, &e.ProjectID, &e.RunID, &e.AgentID, &e.StartedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// ListSkillExecutionsByRun returns executions recorded for a run.
func (s *Store) ListSkillExecutionsByRun(ctx context.Context, runID string) ([]SkillExecution, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, skill_id, skill_version, project_id, run_id, agent_id, started_at
		 FROM workspace_skill_execution WHERE run_id = $1 ORDER BY started_at DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SkillExecution
	for rows.Next() {
		var e SkillExecution
		if err := rows.Scan(&e.ID, &e.SkillID, &e.SkillVersion, &e.ProjectID, &e.RunID, &e.AgentID, &e.StartedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func manifestOrNull(m json.RawMessage) any {
	if len(m) == 0 {
		return nil
	}
	return []byte(m)
}

func rawMessage(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}
