-- Execution identities (docs/org-structure.md §20): the security context of
-- automated runs. A trigger's run never inherits its creator's rights — it
-- runs under an execution identity that names the agents, MCP servers,
-- providers, projects and human request targets it may touch. "*" in an
-- allowed-list means "anything visible in the identity's org scope".
CREATE TABLE IF NOT EXISTS execution_identity (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT,
    allowed_agents    JSONB NOT NULL DEFAULT '["*"]',
    allowed_mcp       JSONB NOT NULL DEFAULT '["*"]',
    allowed_providers JSONB NOT NULL DEFAULT '["*"]',
    allowed_projects  JSONB NOT NULL DEFAULT '["*"]',
    human_targets     JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS execution_identity_org_idx ON execution_identity(org_unit_id);
