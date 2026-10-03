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
	// Default to all users if not specified
	allowedUsers := p.AllowedUsers
	if allowedUsers == nil {
		allowedUsers = []string{"*"}
	}
	allowedUsersJSON, _ := json.Marshal(allowedUsers)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_project (id, name, description, default_agent_id, default_model, allowed_users, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		p.ID, p.Name, p.Description, nullString(p.DefaultAgentID), p.DefaultModel, allowedUsersJSON, p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	var p Project
	var agentID *string
	var allowedUsersJSON []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, default_agent_id, default_model, archived, allowed_users, created_at, updated_at FROM workspace_project WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.Description, &agentID, &p.DefaultModel, &p.Archived, &allowedUsersJSON, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.DefaultAgentID = derefPtr(agentID)
	if len(allowedUsersJSON) > 0 {
		_ = json.Unmarshal(allowedUsersJSON, &p.AllowedUsers)
	}
	if units, err := s.ProjectOrgUnits(ctx, id); err == nil {
		p.OrgUnitIDs = units
	}
	return p, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, default_agent_id, default_model, archived, allowed_users, created_at, updated_at FROM workspace_project ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Project
	for rows.Next() {
		var p Project
		var agentID *string
		var allowedUsersJSON []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &agentID, &p.DefaultModel, &p.Archived, &allowedUsersJSON, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.DefaultAgentID = derefPtr(agentID)
		if len(allowedUsersJSON) > 0 {
			_ = json.Unmarshal(allowedUsersJSON, &p.AllowedUsers)
		}
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	orgLinks, err := s.allProjectOrgUnits(ctx)
	if err != nil {
		return nil, err
	}
	for i := range result {
		result[i].OrgUnitIDs = orgLinks[result[i].ID]
	}
	return result, nil
}

func (s *Store) UpdateProject(ctx context.Context, p Project) error {
	p.UpdatedAt = time.Now().UTC()
	allowedUsers := p.AllowedUsers
	if allowedUsers == nil {
		allowedUsers = []string{"*"}
	}
	allowedUsersJSON, _ := json.Marshal(allowedUsers)
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_project SET name = $2, description = $3, default_agent_id = $4, default_model = $5, allowed_users = $6, updated_at = $7 WHERE id = $1`,
		p.ID, p.Name, p.Description, nullString(p.DefaultAgentID), p.DefaultModel, allowedUsersJSON, p.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	// Try deleting from workspace_project first
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_project WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// Project only exists in observation_events (append-only, can't delete).
	// Create an archived workspace_project entry so ListAllProjects hides it.
	now := time.Now().UTC()
	_, _ = s.pool.Exec(ctx,
		`INSERT INTO workspace_project (id, name, description, archived, created_at, updated_at) VALUES ($1, $2, '', true, $3, $3) ON CONFLICT (id) DO UPDATE SET archived = true, updated_at = $3`,
		id, id, now)
	return nil
}

// Agents — top-level entities, project_id is optional.

func (s *Store) CreateAgent(ctx context.Context, a *Agent) error {
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now
	skills, _ := json.Marshal(a.Skills)
	mcp, _ := json.Marshal(a.MCPServers)
	tools, _ := json.Marshal(a.Tools)
	labels, _ := json.Marshal(a.Labels)
	definition, _ := json.Marshal(a.Definition)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_agent
		 (id, project_id, name, description, model, provider, system_prompt, skills, mcp_servers, tools,
		  sandbox_profile, temperature, max_tokens, network_access, read_only, max_turns, approval_mode, labels,
		  definition, definition_version, org_unit_id, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		a.ID, nullString(a.ProjectID), a.Name, a.Description, a.Model, a.Provider,
		a.SystemPrompt, skills, mcp, tools, a.SandboxProfile,
		a.Temperature, a.MaxTokens, a.NetworkAccess, a.ReadOnly,
		a.MaxTurns, a.ApprovalMode, labels, definition, a.DefinitionVersion, nullString(a.OrgUnitID), a.CreatedAt, a.UpdatedAt)
	return err
}

func (s *Store) GetAgent(ctx context.Context, id string) (Agent, error) {
	var a Agent
	var skills, mcp, tools, labels, definition []byte
	var projectID, orgUnitID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, name, description, model, provider, system_prompt, skills, mcp_servers, tools,
		        sandbox_profile, temperature, max_tokens, network_access, read_only, max_turns, approval_mode, labels,
		        definition, definition_version, org_unit_id, created_at, updated_at
		 FROM workspace_agent WHERE id = $1`, id).
		Scan(&a.ID, &projectID, &a.Name, &a.Description, &a.Model, &a.Provider,
			&a.SystemPrompt, &skills, &mcp, &tools, &a.SandboxProfile,
			&a.Temperature, &a.MaxTokens, &a.NetworkAccess, &a.ReadOnly,
			&a.MaxTurns, &a.ApprovalMode, &labels, &definition, &a.DefinitionVersion, &orgUnitID, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.ProjectID = derefPtr(projectID)
	a.OrgUnitID = derefPtr(orgUnitID)
	_ = json.Unmarshal(skills, &a.Skills)
	_ = json.Unmarshal(mcp, &a.MCPServers)
	_ = json.Unmarshal(tools, &a.Tools)
	_ = json.Unmarshal(labels, &a.Labels)
	a.Definition = decodeAgentDefinition(definition)
	return a, nil
}

func (s *Store) ListAllAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, description, model, provider, system_prompt, skills, mcp_servers, tools,
		        sandbox_profile, temperature, max_tokens, network_access, read_only, max_turns, approval_mode, labels,
		        definition, definition_version, org_unit_id, created_at, updated_at
		 FROM workspace_agent ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

// ListAgentsVisible returns agents visible to a viewer whose org chain is
// units: org-neutral agents plus those bound inside the chain. nil units
// disables filtering (admin and service tokens).
func (s *Store) ListAgentsVisible(ctx context.Context, units []string) ([]Agent, error) {
	if units == nil {
		return s.ListAllAgents(ctx)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, description, model, provider, system_prompt, skills, mcp_servers, tools,
		        sandbox_profile, temperature, max_tokens, network_access, read_only, max_turns, approval_mode, labels,
		        definition, definition_version, org_unit_id, created_at, updated_at
		 FROM workspace_agent WHERE org_unit_id IS NULL OR org_unit_id = ANY($1) ORDER BY created_at`, units)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

func (s *Store) ListAgentsByProject(ctx context.Context, projectID string) ([]Agent, error) {
	// Agents are cross-functional (docs/evaluable-agent.md §16): globals are
	// usable in every project, project-bound ones only in their project.
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, description, model, provider, system_prompt, skills, mcp_servers, tools,
		        sandbox_profile, temperature, max_tokens, network_access, read_only, max_turns, approval_mode, labels,
		        definition, definition_version, org_unit_id, created_at, updated_at
		 FROM workspace_agent WHERE project_id = $1 OR project_id IS NULL ORDER BY created_at`, projectID)
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
		var skills, mcp, tools, labels, definition []byte
		var projectID, orgUnitID *string
		if err := rows.Scan(&a.ID, &projectID, &a.Name, &a.Description, &a.Model, &a.Provider,
			&a.SystemPrompt, &skills, &mcp, &tools, &a.SandboxProfile,
			&a.Temperature, &a.MaxTokens, &a.NetworkAccess, &a.ReadOnly,
			&a.MaxTurns, &a.ApprovalMode, &labels, &definition, &a.DefinitionVersion, &orgUnitID, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.ProjectID = derefPtr(projectID)
		a.OrgUnitID = derefPtr(orgUnitID)
		_ = json.Unmarshal(skills, &a.Skills)
		_ = json.Unmarshal(mcp, &a.MCPServers)
		_ = json.Unmarshal(tools, &a.Tools)
		_ = json.Unmarshal(labels, &a.Labels)
		a.Definition = decodeAgentDefinition(definition)
		result = append(result, a)
	}
	return result, rows.Err()
}

// decodeAgentDefinition keeps Definition nil for empty/absent JSON so legacy
// agents (prompt-only, pre-definition) stay distinguishable.
func decodeAgentDefinition(raw []byte) *AgentDefinition {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return nil
	}
	var def AgentDefinition
	if err := json.Unmarshal(raw, &def); err != nil {
		return nil
	}
	return &def
}

func (s *Store) UpdateAgent(ctx context.Context, a Agent) error {
	a.UpdatedAt = time.Now().UTC()
	skills, _ := json.Marshal(a.Skills)
	mcp, _ := json.Marshal(a.MCPServers)
	tools, _ := json.Marshal(a.Tools)
	labels, _ := json.Marshal(a.Labels)
	definition, _ := json.Marshal(a.Definition)
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_agent
		 SET project_id=$2, name=$3, description=$4, model=$5, provider=$6, system_prompt=$7,
		     skills=$8, mcp_servers=$9, tools=$10, sandbox_profile=$11, temperature=$12, max_tokens=$13,
		     network_access=$14, read_only=$15, max_turns=$16, approval_mode=$17, labels=$18,
		     definition=$19, definition_version=$20, org_unit_id=$21, updated_at=$22
		 WHERE id=$1`,
		a.ID, nullString(a.ProjectID), a.Name, a.Description, a.Model, a.Provider,
		a.SystemPrompt, skills, mcp, tools, a.SandboxProfile,
		a.Temperature, a.MaxTokens, a.NetworkAccess, a.ReadOnly,
		a.MaxTurns, a.ApprovalMode, labels, definition, a.DefinitionVersion, nullString(a.OrgUnitID), a.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// InsertAgentVersion writes an immutable definition snapshot. The compiled
// prompt is recorded for reproducibility even though runs recompile it fresh
// (docs/plan-evaluable-agent.md, decision 2).
func (s *Store) InsertAgentVersion(ctx context.Context, v AgentVersion) error {
	definition, _ := json.Marshal(v.Definition)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_agent_version
		 (agent_id, version, definition, description, compiled_prompt, prompt_source, generator_model, author, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (agent_id, version) DO NOTHING`,
		v.AgentID, v.Version, definition, v.Description, v.CompiledPrompt, v.PromptSource, v.GeneratorModel, v.Author, v.CreatedAt)
	return err
}

func (s *Store) ListAgentVersions(ctx context.Context, agentID string) ([]AgentVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT agent_id, version, definition, description, compiled_prompt, prompt_source, generator_model, author, created_at
		 FROM workspace_agent_version WHERE agent_id = $1 ORDER BY version DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AgentVersion
	for rows.Next() {
		var v AgentVersion
		var definition []byte
		if err := rows.Scan(&v.AgentID, &v.Version, &definition, &v.Description, &v.CompiledPrompt, &v.PromptSource, &v.GeneratorModel, &v.Author, &v.CreatedAt); err != nil {
			return nil, err
		}
		if def := decodeAgentDefinition(definition); def != nil {
			v.Definition = *def
		}
		result = append(result, v)
	}
	return result, rows.Err()
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
	execContext, _ := json.Marshal(execContextOrEmpty(r.ExecContext))
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_run (id, task_id, project_id, agent_id, agent_version, exec_context, run_id, status, model, answer, turns, error, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		r.ID, r.TaskID, r.ProjectID, nullString(r.AgentID), r.AgentVersion, execContext, r.RunID, r.Status, r.Model, r.Answer, r.Turns, r.Error, r.CreatedAt, r.UpdatedAt)
	return err
}

// execContextOrEmpty keeps nil maps out of the JSON column ({} not null).
func execContextOrEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// decodeExecContext unmarshals the exec_context column; corrupt rows decode
// to nil rather than failing the whole list.
func decodeExecContext(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	var agentID *string
	var execContext []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, task_id, project_id, agent_id, agent_version, exec_context, run_id, status, model, answer, turns, error, created_at, updated_at
		 FROM workspace_run WHERE id = $1`, id).
		Scan(&r.ID, &r.TaskID, &r.ProjectID, &agentID, &r.AgentVersion, &execContext, &r.RunID, &r.Status, &r.Model, &r.Answer, &r.Turns, &r.Error, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.AgentID = derefPtr(agentID)
	r.ExecContext = decodeExecContext(execContext)
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
		`SELECT id, task_id, project_id, agent_id, agent_version, exec_context, run_id, status, model, answer, turns, error, created_at, updated_at
		 FROM workspace_run WHERE task_id = $1 ORDER BY created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Run
	for rows.Next() {
		var r Run
		var agentID *string
		var execContext []byte
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ProjectID, &agentID, &r.AgentVersion, &execContext, &r.RunID, &r.Status, &r.Model, &r.Answer, &r.Turns, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.AgentID = derefPtr(agentID)
		r.ExecContext = decodeExecContext(execContext)
		result = append(result, r)
	}
	return result, rows.Err()
}

// ListRunsByProject returns all completed runs for a project.
func (s *Store) ListRunsByProject(ctx context.Context, projectID string) ([]Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, task_id, project_id, agent_id, agent_version, exec_context, run_id, status, model, answer, turns, error, created_at, updated_at
		 FROM workspace_run WHERE project_id = $1 AND status = 'completed' ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Run
	for rows.Next() {
		var r Run
		var agentID *string
		var execContext []byte
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ProjectID, &agentID, &r.AgentVersion, &execContext, &r.RunID, &r.Status, &r.Model, &r.Answer, &r.Turns, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.AgentID = derefPtr(agentID)
		r.ExecContext = decodeExecContext(execContext)
		result = append(result, r)
	}
	return result, rows.Err()
}

// ListRunsByAgent returns recent runs launched with a specific agent across
// projects, newest first — the data behind the agent Evolution view.
func (s *Store) ListRunsByAgent(ctx context.Context, agentID string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, task_id, project_id, agent_id, agent_version, exec_context, run_id, status, model, answer, turns, error, created_at, updated_at
		 FROM workspace_run WHERE agent_id = $1 ORDER BY created_at DESC LIMIT $2`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Run
	for rows.Next() {
		var r Run
		var id *string
		var execContext []byte
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ProjectID, &id, &r.AgentVersion, &execContext, &r.RunID, &r.Status, &r.Model, &r.Answer, &r.Turns, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.AgentID = derefPtr(id)
		r.ExecContext = decodeExecContext(execContext)
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

// Triggers

func (s *Store) CreateTrigger(ctx context.Context, t *Trigger) error {
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now
	if len(t.Config) == 0 {
		t.Config = json.RawMessage(`{}`)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_trigger (id, project_id, agent_id, name, type, enabled, config, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		t.ID, t.ProjectID, nullString(t.AgentID), t.Name, t.Type, t.Enabled, t.Config, t.CreatedAt, t.UpdatedAt)
	return err
}

func (s *Store) GetTrigger(ctx context.Context, id string) (Trigger, error) {
	var t Trigger
	var agentID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, agent_id, name, type, enabled, config, created_at, updated_at
		 FROM workspace_trigger WHERE id = $1`, id).
		Scan(&t.ID, &t.ProjectID, &agentID, &t.Name, &t.Type, &t.Enabled, &t.Config, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	t.AgentID = derefPtr(agentID)
	return t, err
}

func (s *Store) ListTriggers(ctx context.Context, projectID string) ([]Trigger, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, agent_id, name, type, enabled, config, created_at, updated_at
		 FROM workspace_trigger WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Trigger
	for rows.Next() {
		var t Trigger
		var agentID *string
		if err := rows.Scan(&t.ID, &t.ProjectID, &agentID, &t.Name, &t.Type, &t.Enabled, &t.Config, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.AgentID = derefPtr(agentID)
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) ListAllTriggers(ctx context.Context) ([]Trigger, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, agent_id, name, type, enabled, config, created_at, updated_at
		 FROM workspace_trigger WHERE enabled = true ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Trigger
	for rows.Next() {
		var t Trigger
		var agentID *string
		if err := rows.Scan(&t.ID, &t.ProjectID, &agentID, &t.Name, &t.Type, &t.Enabled, &t.Config, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.AgentID = derefPtr(agentID)
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) UpdateTrigger(ctx context.Context, t Trigger) error {
	t.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_trigger SET name=$2, agent_id=$3, type=COALESCE(NULLIF($4,''),type), enabled=$5, config=COALESCE($6, config), updated_at=$7 WHERE id=$1`,
		t.ID, t.Name, nullString(t.AgentID), t.Type, t.Enabled, nullRawMessage(t.Config), t.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteTrigger(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_trigger WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAllProjects returns workspace projects merged with journal projects.
func (s *Store) ListAllProjects(ctx context.Context) ([]Project, error) {
	wp, err := s.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]Project{}
	archived := map[string]bool{}
	for _, p := range wp {
		if p.Archived {
			archived[p.ID] = true
		} else {
			byID[p.ID] = p
		}
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
		if _, exists := byID[pid]; !exists && !archived[pid] {
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

// ListProjectsForUser returns projects visible to a specific user
// (docs/org-structure.md §15-16): org-unit access OR explicit membership.
// The legacy allowed_users column is frozen on read — it is no longer
// consulted and will be dropped by a later cleanup migration.
func (s *Store) ListProjectsForUser(ctx context.Context, userID string, isAdmin bool, units []string) ([]Project, error) {
	allProjects, err := s.ListAllProjects(ctx)
	if err != nil {
		return nil, err
	}
	if isAdmin {
		return allProjects, nil
	}
	var orgLinks map[string][]string
	if units != nil {
		orgLinks, err = s.allProjectOrgUnits(ctx)
		if err != nil {
			return nil, err
		}
	}
	var members map[string]map[string]bool
	if userID != "" {
		members, err = s.allProjectMembers(ctx)
		if err != nil {
			return nil, err
		}
	}
	var result []Project
	for _, p := range allProjects {
		if !projectVisibleFor(orgLinks[p.ID], units, members[p.ID] != nil && members[p.ID][userID]) {
			continue
		}
		result = append(result, p)
	}
	return result, nil
}

// VisibleProjectIDs answers the project ids a user may act on: "*" when the
// set is unbounded (admins and org-unassigned users in transition mode), the
// concrete id list otherwise (org intersection plus explicit membership). It
// feeds the auth-gate token scopes, so run gates reject invisible projects
// before the workflow starts.
func (s *Store) VisibleProjectIDs(ctx context.Context, userID string, isAdmin bool, units []string) ([]string, error) {
	if isAdmin || units == nil {
		return []string{"*"}, nil
	}
	projects, err := s.ListProjectsForUser(ctx, userID, isAdmin, units)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ID)
	}
	return ids, nil
}

// projectVisibleFor reports whether a project with the given org bindings is
// visible to a viewer with the given ancestor chain and membership state
// (docs/org-structure.md §15-16). Nil units disables the org check (admins,
// unassigned viewers — transition mode).
func projectVisibleFor(boundUnits, units []string, isMember bool) bool {
	if units == nil {
		return true
	}
	return orgAllowsAny(boundUnits, units) || isMember
}

// orgAllowsAny reports whether any of the project's bound units is in the
// viewer's chain. A project without bindings is org-neutral (visible).
func orgAllowsAny(boundUnits, units []string) bool {
	if len(boundUnits) == 0 {
		return true
	}
	for _, bound := range boundUnits {
		for _, u := range units {
			if bound == u {
				return true
			}
		}
	}
	return false
}

// Providers

func (s *Store) CreateProvider(ctx context.Context, p *Provider) error {
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	labels, _ := json.Marshal(p.Labels)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_provider (id, name, base_url, api_key_ref, models, labels, org_unit_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		p.ID, p.Name, p.BaseURL, p.APIKeyRef, p.Models, labels, nullString(p.OrgUnitID), p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *Store) GetProvider(ctx context.Context, id string) (Provider, error) {
	var p Provider
	var labels []byte
	var orgUnitID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, base_url, api_key_ref, models, labels, org_unit_id, created_at, updated_at
		 FROM workspace_provider WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKeyRef, &p.Models, &labels, &orgUnitID, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.OrgUnitID = derefPtr(orgUnitID)
	_ = json.Unmarshal(labels, &p.Labels)
	return p, nil
}

func (s *Store) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, base_url, api_key_ref, models, labels, org_unit_id, created_at, updated_at FROM workspace_provider ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Provider
	for rows.Next() {
		var p Provider
		var labels []byte
		var orgUnitID *string
		if err := rows.Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKeyRef, &p.Models, &labels, &orgUnitID, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.OrgUnitID = derefPtr(orgUnitID)
		_ = json.Unmarshal(labels, &p.Labels)
		result = append(result, p)
	}
	return result, rows.Err()
}

// ListProvidersVisible returns providers visible to a viewer whose org chain
// is units; nil units disables filtering (admin and service tokens).
func (s *Store) ListProvidersVisible(ctx context.Context, units []string) ([]Provider, error) {
	if units == nil {
		return s.ListProviders(ctx)
	}
	rows, err := s.pool.Query(ctx, `SELECT id, name, base_url, api_key_ref, models, labels, org_unit_id, created_at, updated_at FROM workspace_provider WHERE org_unit_id IS NULL OR org_unit_id = ANY($1) ORDER BY created_at`, units)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Provider
	for rows.Next() {
		var p Provider
		var labels []byte
		var orgUnitID *string
		if err := rows.Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKeyRef, &p.Models, &labels, &orgUnitID, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.OrgUnitID = derefPtr(orgUnitID)
		_ = json.Unmarshal(labels, &p.Labels)
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) UpdateProvider(ctx context.Context, p Provider) error {
	p.UpdatedAt = time.Now().UTC()
	labels, _ := json.Marshal(p.Labels)
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_provider SET name=$2, base_url=$3, api_key_ref=$4, models=$5, labels=$6, org_unit_id=$7, updated_at=$8 WHERE id=$1`,
		p.ID, p.Name, p.BaseURL, p.APIKeyRef, p.Models, labels, nullString(p.OrgUnitID), p.UpdatedAt)
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
	channels, _ := json.Marshal(channelList(u.Channels))
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_user (id, name, email, role, token, projects, org_unit_id, active, channels, preferred_channel, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		u.ID, u.Name, u.Email, u.Role, u.Token, projects, nullString(u.OrgUnitID), u.Active, channels, u.PreferredChannel, u.CreatedAt, u.UpdatedAt)
	return err
}

// channelList keeps the channels column an array even when empty.
func channelList(channels []UserChannel) []UserChannel {
	if channels == nil {
		return []UserChannel{}
	}
	return channels
}

func scanChannels(raw []byte) []UserChannel {
	var channels []UserChannel
	_ = json.Unmarshal(raw, &channels)
	return channels
}

func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	var projects, channels []byte
	var orgUnitID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, email, role, COALESCE(token, ''), projects, org_unit_id, active, channels, preferred_channel, created_at, updated_at
		 FROM workspace_user WHERE id = $1`, id).
		Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Token, &projects, &orgUnitID, &u.Active, &channels, &u.PreferredChannel, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.OrgUnitID = derefPtr(orgUnitID)
	_ = json.Unmarshal(projects, &u.Projects)
	u.Channels = scanChannels(channels)
	return u, nil
}

// ListUsers returns users for API responses: the token value is never
// included, only HasToken reporting whether one exists.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, email, role, (token <> '') AS has_token, projects, org_unit_id, active, channels, preferred_channel, created_at, updated_at FROM workspace_user ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []User
	for rows.Next() {
		var u User
		var projects, channels []byte
		var orgUnitID *string
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.HasToken, &projects, &orgUnitID, &u.Active, &channels, &u.PreferredChannel, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.OrgUnitID = derefPtr(orgUnitID)
		_ = json.Unmarshal(projects, &u.Projects)
		u.Channels = scanChannels(channels)
		result = append(result, u)
	}
	return result, rows.Err()
}

// ListUsersWithTokens is the privileged variant used by the kernel auth
// gate: it returns the raw bearer tokens. Its results must never be
// serialized to API clients.
func (s *Store) ListUsersWithTokens(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, email, role, token, projects, org_unit_id, active, channels, preferred_channel, created_at, updated_at FROM workspace_user ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []User
	for rows.Next() {
		var u User
		var projects, channels []byte
		var orgUnitID *string
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Token, &projects, &orgUnitID, &u.Active, &channels, &u.PreferredChannel, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.OrgUnitID = derefPtr(orgUnitID)
		_ = json.Unmarshal(projects, &u.Projects)
		u.Channels = scanChannels(channels)
		u.HasToken = u.Token != ""
		result = append(result, u)
	}
	return result, rows.Err()
}

// UpdateUserToken replaces a user's bearer token (regeneration).
func (s *Store) UpdateUserToken(ctx context.Context, id, token string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE workspace_user SET token = $2, updated_at = now() WHERE id = $1`, id, token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateUser(ctx context.Context, u User) error {
	u.UpdatedAt = time.Now().UTC()
	projects, _ := json.Marshal(u.Projects)
	channels, _ := json.Marshal(channelList(u.Channels))
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_user SET name=$2, email=$3, role=$4, projects=$5, org_unit_id=$6, active=$7, channels=$8, preferred_channel=$9, updated_at=$10 WHERE id=$1`,
		u.ID, u.Name, u.Email, u.Role, projects, nullString(u.OrgUnitID), u.Active, channels, u.PreferredChannel, u.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserChannels replaces only the communication channels and the
// preferred transport. Role, projects and tokens are intentionally out of
// scope: this backs the self-service profile endpoint.
func (s *Store) UpdateUserChannels(ctx context.Context, id string, channels []UserChannel, preferred string) error {
	raw, _ := json.Marshal(channelList(channels))
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_user SET channels=$2, preferred_channel=$3, updated_at=now() WHERE id=$1`,
		id, raw, preferred)
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

func nullRawMessage(b json.RawMessage) interface{} {
	if len(b) == 0 || string(b) == "null" || string(b) == "{}" {
		return nil
	}
	return b
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
	var projects, channels []byte
	var orgUnitID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, email, role, '', projects, org_unit_id, active, channels, preferred_channel, created_at, updated_at
		 FROM workspace_user WHERE token = $1 AND active = true`, token).
		Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Token, &projects, &orgUnitID, &u.Active, &channels, &u.PreferredChannel, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.OrgUnitID = derefPtr(orgUnitID)
	_ = json.Unmarshal(projects, &u.Projects)
	u.Channels = scanChannels(channels)
	return u, nil
}
