-- MCP server registry (docs/mcp registry).
-- workspace_mcp_server holds per-project MCP server configs (stdio / sse / http).
-- Tool exposure is bound per agent: workspace_agent.tools stores the selected
-- tool model names (empty = all available tools), so the registry itself stays
-- a project-level connection while agents decide what they may call.

CREATE TABLE IF NOT EXISTS workspace_mcp_server (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES workspace_project(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    type           TEXT NOT NULL CHECK (type IN ('stdio','sse','http')),
    url            TEXT NOT NULL DEFAULT '',
    command        TEXT NOT NULL DEFAULT '',
    args           JSONB NOT NULL DEFAULT '[]',
    env            JSONB NOT NULL DEFAULT '[]',
    headers        JSONB NOT NULL DEFAULT '{}',
    allowed_tools  JSONB NOT NULL DEFAULT '[]',
    approval_tools JSONB NOT NULL DEFAULT '[]',
    enabled        BOOL NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_mcp_server_project ON workspace_mcp_server(project_id);

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS tools JSONB NOT NULL DEFAULT '[]';
