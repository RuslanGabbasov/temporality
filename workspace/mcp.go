package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// MCPServer is a project-scoped MCP server connection. Type selects the
// transport: stdio (command + args + env), sse or http (url + headers).
// AllowedTools/ApprovalTools store original tool names from the server;
// empty AllowedTools exposes every tool the server advertises.
type MCPServer struct {
	ID            string            `json:"id"`
	ProjectID     string            `json:"project_id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"` // stdio | sse | http
	URL           string            `json:"url,omitempty"`
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args"`
	Env           []string          `json:"env"`     // KEY=VALUE entries, stdio only
	Headers       map[string]string `json:"headers"` // sse/http only
	AllowedTools  []string          `json:"allowed_tools"`
	ApprovalTools []string          `json:"approval_tools"`
	Enabled       bool              `json:"enabled"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

func marshalStrings(v []string) []byte {
	if v == nil {
		return []byte("[]")
	}
	b, _ := json.Marshal(v)
	return b
}

func marshalMap(v map[string]string) []byte {
	if v == nil {
		return []byte("{}")
	}
	b, _ := json.Marshal(v)
	return b
}

func (s *Store) CreateMCPServer(ctx context.Context, m *MCPServer) error {
	now := time.Now().UTC()
	m.CreatedAt = now
	m.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_mcp_server
		 (id, project_id, name, type, url, command, args, env, headers, allowed_tools, approval_tools, enabled, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		m.ID, m.ProjectID, m.Name, m.Type, m.URL, m.Command,
		marshalStrings(m.Args), marshalStrings(m.Env), marshalMap(m.Headers),
		marshalStrings(m.AllowedTools), marshalStrings(m.ApprovalTools),
		m.Enabled, m.CreatedAt, m.UpdatedAt)
	return err
}

func (s *Store) GetMCPServer(ctx context.Context, id string) (MCPServer, error) {
	var m MCPServer
	var args, env, headers, allowed, approval []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, name, type, url, command, args, env, headers, allowed_tools, approval_tools, enabled, created_at, updated_at
		 FROM workspace_mcp_server WHERE id = $1`, id).
		Scan(&m.ID, &m.ProjectID, &m.Name, &m.Type, &m.URL, &m.Command, &args, &env, &headers, &allowed, &approval, &m.Enabled, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	return scanMCPServer(m, args, env, headers, allowed, approval), nil
}

func (s *Store) ListMCPServers(ctx context.Context, projectID string) ([]MCPServer, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, type, url, command, args, env, headers, allowed_tools, approval_tools, enabled, created_at, updated_at
		 FROM workspace_mcp_server WHERE project_id = $1 ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMCPServers(rows)
}

func (s *Store) ListAllMCPServers(ctx context.Context) ([]MCPServer, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, type, url, command, args, env, headers, allowed_tools, approval_tools, enabled, created_at, updated_at
		 FROM workspace_mcp_server ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMCPServers(rows)
}

func (s *Store) UpdateMCPServer(ctx context.Context, m *MCPServer) error {
	m.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_mcp_server
		 SET name=$2, type=$3, url=$4, command=$5, args=$6, env=$7, headers=$8,
		     allowed_tools=$9, approval_tools=$10, enabled=$11, updated_at=$12
		 WHERE id=$1`,
		m.ID, m.Name, m.Type, m.URL, m.Command,
		marshalStrings(m.Args), marshalStrings(m.Env), marshalMap(m.Headers),
		marshalStrings(m.AllowedTools), marshalStrings(m.ApprovalTools),
		m.Enabled, m.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteMCPServer(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_mcp_server WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanMCPServers(rows pgx.Rows) ([]MCPServer, error) {
	var result []MCPServer
	for rows.Next() {
		var m MCPServer
		var args, env, headers, allowed, approval []byte
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Name, &m.Type, &m.URL, &m.Command, &args, &env, &headers, &allowed, &approval, &m.Enabled, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, scanMCPServer(m, args, env, headers, allowed, approval))
	}
	return result, rows.Err()
}

func scanMCPServer(m MCPServer, args, env, headers, allowed, approval []byte) MCPServer {
	_ = json.Unmarshal(args, &m.Args)
	_ = json.Unmarshal(env, &m.Env)
	_ = json.Unmarshal(headers, &m.Headers)
	_ = json.Unmarshal(allowed, &m.AllowedTools)
	_ = json.Unmarshal(approval, &m.ApprovalTools)
	return m
}
