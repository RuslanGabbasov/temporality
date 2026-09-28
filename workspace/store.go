package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

var ErrNotFound = errors.New("not found")

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Migrate(ctx context.Context, path string) error {
	data, err := readFile(path)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, string(data))
	return err
}

// Projects

func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_project (id, name, description, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		p.ID, p.Name, p.Description, p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, created_at, updated_at
		 FROM workspace_project WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, description, created_at, updated_at
		 FROM workspace_project ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// ListAllAgents returns all agents across all projects.
func (s *Store) ListAllAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, model, system_prompt, skills, mcp_servers, sandbox_profile, created_at, updated_at
		 FROM workspace_agent ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Agent
	for rows.Next() {
		var a Agent
		var skills, mcp []byte
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Model, &a.SystemPrompt, &skills, &mcp, &a.SandboxProfile, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(skills, &a.Skills)
		_ = json.Unmarshal(mcp, &a.MCPServers)
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *Store) UpdateProject(ctx context.Context, p Project) error {
	p.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_project SET name = $2, description = $3, updated_at = $4
		 WHERE id = $1`,
		p.ID, p.Name, p.Description, p.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_project WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Agents

func (s *Store) CreateAgent(ctx context.Context, a *Agent) error {
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now
	skills, _ := json.Marshal(a.Skills)
	mcp, _ := json.Marshal(a.MCPServers)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_agent (id, project_id, name, model, system_prompt, skills, mcp_servers, sandbox_profile, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		a.ID, a.ProjectID, a.Name, a.Model, a.SystemPrompt, skills, mcp, a.SandboxProfile, a.CreatedAt, a.UpdatedAt)
	return err
}

func (s *Store) GetAgent(ctx context.Context, id string) (Agent, error) {
	var a Agent
	var skills, mcp []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, name, model, system_prompt, skills, mcp_servers, sandbox_profile, created_at, updated_at
		 FROM workspace_agent WHERE id = $1`, id).
		Scan(&a.ID, &a.ProjectID, &a.Name, &a.Model, &a.SystemPrompt, &skills, &mcp, &a.SandboxProfile, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	_ = json.Unmarshal(skills, &a.Skills)
	_ = json.Unmarshal(mcp, &a.MCPServers)
	return a, nil
}

func (s *Store) ListAgents(ctx context.Context, projectID string) ([]Agent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, model, system_prompt, skills, mcp_servers, sandbox_profile, created_at, updated_at
		 FROM workspace_agent WHERE project_id = $1 ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Agent
	for rows.Next() {
		var a Agent
		var skills, mcp []byte
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Model, &a.SystemPrompt, &skills, &mcp, &a.SandboxProfile, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(skills, &a.Skills)
		_ = json.Unmarshal(mcp, &a.MCPServers)
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *Store) UpdateAgent(ctx context.Context, a Agent) error {
	a.UpdatedAt = time.Now().UTC()
	skills, _ := json.Marshal(a.Skills)
	mcp, _ := json.Marshal(a.MCPServers)
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_agent
		 SET name = $2, model = $3, system_prompt = $4, skills = $5, mcp_servers = $6, sandbox_profile = $7, updated_at = $8
		 WHERE id = $1`,
		a.ID, a.Name, a.Model, a.SystemPrompt, skills, mcp, a.SandboxProfile, a.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_agent WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Tasks

func (s *Store) CreateTask(ctx context.Context, t *Task) error {
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_task (id, project_id, agent_id, title, prompt, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.ID, t.ProjectID, nullString(t.AgentID), t.Title, t.Prompt, t.CreatedAt, t.UpdatedAt)
	return err
}

func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {
	var t Task
	var agentID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, agent_id, title, prompt, created_at, updated_at
		 FROM workspace_task WHERE id = $1`, id).
		Scan(&t.ID, &t.ProjectID, &agentID, &t.Title, &t.Prompt, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if agentID != nil {
		t.AgentID = *agentID
	}
	return t, err
}

func (s *Store) ListTasks(ctx context.Context, projectID string) ([]Task, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, agent_id, title, prompt, created_at, updated_at
		 FROM workspace_task WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Task
	for rows.Next() {
		var t Task
		var agentID *string
		if err := rows.Scan(&t.ID, &t.ProjectID, &agentID, &t.Title, &t.Prompt, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if agentID != nil {
			t.AgentID = *agentID
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) DeleteTask(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_task WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Runs

func (s *Store) CreateRun(ctx context.Context, r *Run) error {
	now := time.Now().UTC()
	r.CreatedAt = now
	r.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_run (id, task_id, project_id, agent_id, run_id, status, model, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		r.ID, r.TaskID, r.ProjectID, nullString(r.AgentID), r.RunID, r.Status, r.Model, r.CreatedAt, r.UpdatedAt)
	return err
}

func (s *Store) UpdateRunStatus(ctx context.Context, id, status, answer string, turns int, runErr string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_run SET status = $2, answer = $3, turns = $4, error = $5, updated_at = $6
		 WHERE id = $1`,
		id, status, answer, turns, runErr, time.Now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	var agentID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, task_id, project_id, agent_id, run_id, status, model, answer, turns, error, created_at, updated_at
		 FROM workspace_run WHERE id = $1`, id).
		Scan(&r.ID, &r.TaskID, &r.ProjectID, &agentID, &r.RunID, &r.Status, &r.Model, &r.Answer, &r.Turns, &r.Error, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if agentID != nil {
		r.AgentID = *agentID
	}
	return r, err
}

func (s *Store) ListRuns(ctx context.Context, taskID string) ([]Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, task_id, project_id, agent_id, run_id, status, model, answer, turns, error, created_at, updated_at
		 FROM workspace_run WHERE task_id = $1 ORDER BY created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Run
	for rows.Next() {
		var r Run
		var agentID *string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ProjectID, &agentID, &r.RunID, &r.Status, &r.Model, &r.Answer, &r.Turns, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if agentID != nil {
			r.AgentID = *agentID
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) ListRunsByProject(ctx context.Context, projectID string) ([]Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, task_id, project_id, agent_id, run_id, status, model, answer, turns, error, created_at, updated_at
		 FROM workspace_run WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Run
	for rows.Next() {
		var r Run
		var agentID *string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ProjectID, &agentID, &r.RunID, &r.Status, &r.Model, &r.Answer, &r.Turns, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if agentID != nil {
			r.AgentID = *agentID
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListAllProjects returns workspace projects merged with journal projects
// (distinct project_id from observation_events).
func (s *Store) ListAllProjects(ctx context.Context) ([]Project, error) {
	// Workspace projects
	wp, err := s.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]Project{}
	for _, p := range wp {
		byID[p.ID] = p
	}
	// Journal projects (observation_events)
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT project_id FROM observation_events WHERE project_id != '' ORDER BY project_id`)
	if err != nil {
		return wp, nil // fallback to workspace-only
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			continue
		}
		if _, exists := byID[pid]; !exists {
			byID[pid] = Project{ID: pid, Name: pid}
		}
	}
	result := make([]Project, 0, len(byID))
	for _, p := range byID {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
