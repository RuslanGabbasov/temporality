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

var ErrNotFound = errors.New("not found")

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
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
		`INSERT INTO workspace_project (id, name, description, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)`,
		p.ID, p.Name, p.Description, p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, created_at, updated_at FROM workspace_project WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, created_at, updated_at FROM workspace_project ORDER BY created_at DESC`)
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

func (s *Store) UpdateProject(ctx context.Context, p Project) error {
	p.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_project SET name = $2, description = $3, updated_at = $4 WHERE id = $1`,
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

// Agents — top-level entities, project_id is optional.

func (s *Store) CreateAgent(ctx context.Context, a *Agent) error {
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now
	skills, _ := json.Marshal(a.Skills)
	mcp, _ := json.Marshal(a.MCPServers)
	labels, _ := json.Marshal(a.Labels)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_agent
		 (id, project_id, name, description, model, provider, system_prompt, skills, mcp_servers,
		  sandbox_profile, temperature, max_tokens, network_access, read_only, max_turns, approval_mode, labels, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		a.ID, nullString(a.ProjectID), a.Name, a.Description, a.Model, a.Provider,
		a.SystemPrompt, skills, mcp, a.SandboxProfile,
		a.Temperature, a.MaxTokens, a.NetworkAccess, a.ReadOnly,
		a.MaxTurns, a.ApprovalMode, labels, a.CreatedAt, a.UpdatedAt)
	return err
}

func (s *Store) GetAgent(ctx context.Context, id string) (Agent, error) {
	var a Agent
	var skills, mcp, labels []byte
	var projectID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, name, description, model, provider, system_prompt, skills, mcp_servers,
		        sandbox_profile, temperature, max_tokens, network_access, read_only, max_turns, approval_mode, labels, created_at, updated_at
		 FROM workspace_agent WHERE id = $1`, id).
		Scan(&a.ID, &projectID, &a.Name, &a.Description, &a.Model, &a.Provider,
			&a.SystemPrompt, &skills, &mcp, &a.SandboxProfile,
			&a.Temperature, &a.MaxTokens, &a.NetworkAccess, &a.ReadOnly,
			&a.MaxTurns, &a.ApprovalMode, &labels, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.ProjectID = derefPtr(projectID)
	_ = json.Unmarshal(skills, &a.Skills)
	_ = json.Unmarshal(mcp, &a.MCPServers)
	_ = json.Unmarshal(labels, &a.Labels)
	return a, nil
}

func (s *Store) ListAllAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, description, model, provider, system_prompt, skills, mcp_servers,
		        sandbox_profile, temperature, max_tokens, network_access, read_only, max_turns, approval_mode, labels, created_at, updated_at
		 FROM workspace_agent ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

func (s *Store) ListAgentsByProject(ctx context.Context, projectID string) ([]Agent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, description, model, provider, system_prompt, skills, mcp_servers,
		        sandbox_profile, temperature, max_tokens, network_access, read_only, max_turns, approval_mode, labels, created_at, updated_at
		 FROM workspace_agent WHERE project_id = $1 ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

func scanAgents(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]Agent, error) {
	var result []Agent
	for rows.Next() {
		var a Agent
		var skills, mcp, labels []byte
		var projectID *string
		if err := rows.Scan(&a.ID, &projectID, &a.Name, &a.Description, &a.Model, &a.Provider,
			&a.SystemPrompt, &skills, &mcp, &a.SandboxProfile,
			&a.Temperature, &a.MaxTokens, &a.NetworkAccess, &a.ReadOnly,
			&a.MaxTurns, &a.ApprovalMode, &labels, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.ProjectID = derefPtr(projectID)
		_ = json.Unmarshal(skills, &a.Skills)
		_ = json.Unmarshal(mcp, &a.MCPServers)
		_ = json.Unmarshal(labels, &a.Labels)
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *Store) UpdateAgent(ctx context.Context, a Agent) error {
	a.UpdatedAt = time.Now().UTC()
	skills, _ := json.Marshal(a.Skills)
	mcp, _ := json.Marshal(a.MCPServers)
	labels, _ := json.Marshal(a.Labels)
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_agent
		 SET project_id=$2, name=$3, description=$4, model=$5, provider=$6, system_prompt=$7,
		     skills=$8, mcp_servers=$9, sandbox_profile=$10, temperature=$11, max_tokens=$12,
		     network_access=$13, read_only=$14, max_turns=$15, approval_mode=$16, labels=$17, updated_at=$18
		 WHERE id=$1`,
		a.ID, nullString(a.ProjectID), a.Name, a.Description, a.Model, a.Provider,
		a.SystemPrompt, skills, mcp, a.SandboxProfile,
		a.Temperature, a.MaxTokens, a.NetworkAccess, a.ReadOnly,
		a.MaxTurns, a.ApprovalMode, labels, a.UpdatedAt)
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
		`SELECT id, project_id, agent_id, title, prompt, created_at, updated_at FROM workspace_task WHERE id = $1`, id).
		Scan(&t.ID, &t.ProjectID, &agentID, &t.Title, &t.Prompt, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.AgentID = derefPtr(agentID)
	return t, nil
}

func (s *Store) ListTasks(ctx context.Context, projectID string) ([]Task, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, agent_id, title, prompt, created_at, updated_at FROM workspace_task WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
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
		t.AgentID = derefPtr(agentID)
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) UpdateTaskPrompt(ctx context.Context, id, prompt, title string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_task SET prompt = $2, title = COALESCE(NULLIF($3, ''), title), updated_at = $4 WHERE id = $1`,
		id, prompt, title, time.Now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
		`INSERT INTO workspace_run (id, task_id, project_id, agent_id, run_id, status, model, answer, turns, error, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		r.ID, r.TaskID, r.ProjectID, nullString(r.AgentID), r.RunID, r.Status, r.Model, r.Answer, r.Turns, r.Error, r.CreatedAt, r.UpdatedAt)
	return err
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
	if err != nil {
		return r, err
	}
	r.AgentID = derefPtr(agentID)
	return r, nil
}

func (s *Store) UpdateRunStatus(ctx context.Context, id, status, answer string, turns int, runErr string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_run SET status = $2, answer = $3, turns = $4, error = $5, updated_at = $6 WHERE id = $1`,
		id, status, answer, turns, runErr, time.Now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
		r.AgentID = derefPtr(agentID)
		result = append(result, r)
	}
	return result, rows.Err()
}

// StreamEvent is a lightweight event representation for SSE streaming.
type StreamEvent struct {
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurred_at"`
	EventID    string          `json:"event_id"`
	Data       json.RawMessage `json:"data"`
}

// StreamRunEvents fetches observation events for a run, optionally starting
// after a cursor (event_id). Returns events ordered by occurred_at.
func (s *Store) StreamRunEvents(ctx context.Context, projectID, runID, afterCursor string, limit int) ([]StreamEvent, string, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if afterCursor == "" {
		rows, err = s.pool.Query(ctx,
			`SELECT event_id, type, occurred_at, data FROM observation_events
			 WHERE project_id = $1 AND run_id = $2
			 ORDER BY occurred_at, event_id LIMIT $3`, projectID, runID, limit)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT event_id, type, occurred_at, data FROM observation_events
			 WHERE project_id = $1 AND run_id = $2
			 AND occurred_at > (SELECT occurred_at FROM observation_events WHERE event_id = $3 LIMIT 1)
			 ORDER BY occurred_at LIMIT $4`, projectID, runID, afterCursor, limit)
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var events []StreamEvent
	var lastID string
	for rows.Next() {
		var ev StreamEvent
		var at time.Time
		if err := rows.Scan(&ev.EventID, &ev.Type, &at, &ev.Data); err != nil {
			return nil, "", err
		}
		if ev.Data == nil {
			ev.Data = json.RawMessage("{}")
		}
		_ = at
		// Use event_id as cursor instead of timestamp to avoid precision issues.
		lastID = ev.EventID
		ev.OccurredAt = at.UTC().Format(time.RFC3339Nano)
		events = append(events, ev)
	}
	return events, lastID, rows.Err()
}

// ListAllProjects returns workspace projects merged with journal projects.
func (s *Store) ListAllProjects(ctx context.Context) ([]Project, error) {
	wp, err := s.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]Project{}
	for _, p := range wp {
		byID[p.ID] = p
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT project_id FROM observation_events WHERE project_id != '' ORDER BY project_id`)
	if err != nil {
		return wp, nil
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

// Providers

func (s *Store) CreateProvider(ctx context.Context, p *Provider) error {
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	labels, _ := json.Marshal(p.Labels)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_provider (id, name, base_url, api_key_ref, models, labels, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		p.ID, p.Name, p.BaseURL, p.APIKeyRef, p.Models, labels, p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *Store) GetProvider(ctx context.Context, id string) (Provider, error) {
	var p Provider
	var labels []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, base_url, api_key_ref, models, labels, created_at, updated_at
		 FROM workspace_provider WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKeyRef, &p.Models, &labels, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	_ = json.Unmarshal(labels, &p.Labels)
	return p, nil
}

func (s *Store) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, base_url, api_key_ref, models, labels, created_at, updated_at FROM workspace_provider ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Provider
	for rows.Next() {
		var p Provider
		var labels []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKeyRef, &p.Models, &labels, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(labels, &p.Labels)
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) UpdateProvider(ctx context.Context, p Provider) error {
	p.UpdatedAt = time.Now().UTC()
	labels, _ := json.Marshal(p.Labels)
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_provider SET name=$2, base_url=$3, api_key_ref=$4, models=$5, labels=$6, updated_at=$7 WHERE id=$1`,
		p.ID, p.Name, p.BaseURL, p.APIKeyRef, p.Models, labels, p.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_provider WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Users

func (s *Store) CreateUser(ctx context.Context, u *User) error {
	now := time.Now().UTC()
	u.CreatedAt = now
	u.UpdatedAt = now
	if !u.Active {
		u.Active = true
	}
	projects, _ := json.Marshal(u.Projects)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_user (id, name, email, role, token, projects, active, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		u.ID, u.Name, u.Email, u.Role, u.Token, projects, u.Active, u.CreatedAt, u.UpdatedAt)
	return err
}

func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	var projects []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, email, role, COALESCE(token, ''), projects, active, created_at, updated_at
		 FROM workspace_user WHERE id = $1`, id).
		Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Token, &projects, &u.Active, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	_ = json.Unmarshal(projects, &u.Projects)
	return u, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, email, role, '', projects, active, created_at, updated_at FROM workspace_user ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []User
	for rows.Next() {
		var u User
		var projects []byte
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Token, &projects, &u.Active, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(projects, &u.Projects)
		result = append(result, u)
	}
	return result, rows.Err()
}

func (s *Store) UpdateUser(ctx context.Context, u User) error {
	u.UpdatedAt = time.Now().UTC()
	projects, _ := json.Marshal(u.Projects)
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_user SET name=$2, email=$3, role=$4, projects=$5, active=$6, updated_at=$7 WHERE id=$1`,
		u.ID, u.Name, u.Email, u.Role, projects, u.Active, u.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_user WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Helpers

func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	return data, err
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// GetUserByToken looks up a user by their bearer token.
// Returns ErrNotFound if no active user has that token.
func (s *Store) GetUserByToken(ctx context.Context, token string) (User, error) {
	var u User
	var projects []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, email, role, '', projects, active, created_at, updated_at
		 FROM workspace_user WHERE token = $1 AND active = true`, token).
		Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Token, &projects, &u.Active, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	_ = json.Unmarshal(projects, &u.Projects)
	return u, nil
}
