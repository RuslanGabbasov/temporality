-- Org structure tree and resource bindings (docs/org-structure.md §3).
--
-- org_unit is the org hierarchy: parent_id NULL marks a root (one per
-- company; an installation may hold several). path is the materialized
-- ancestor-id path ("root.dept" for a team under dept) maintained by the
-- store on create/move; it exists so visibility resolves without recursion:
-- a user's effective chain is string_to_array(path, '.') || id.
--
-- Bindings: org_unit_id NULL on a resource keeps today's "whole
-- installation" semantics; a value scopes the resource to the unit and its
-- subtree. Projects link to several units many-to-many; the project side of
-- that link cascades (deleting a project drops its bindings), while deleting
-- a unit requires clearing its bindings first (the store enforces
-- "empty unit only", the database is the second line of defense).
CREATE TABLE IF NOT EXISTS org_unit (
    id         TEXT PRIMARY KEY,
    parent_id  TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT,
    kind       TEXT NOT NULL DEFAULT 'department',
    name       TEXT NOT NULL,
    path       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT org_unit_kind_check CHECK (kind IN ('organization', 'department', 'team'))
);

CREATE TABLE IF NOT EXISTS workspace_project_org_unit (
    project_id  TEXT NOT NULL REFERENCES workspace_project(id) ON DELETE CASCADE,
    org_unit_id TEXT NOT NULL REFERENCES org_unit(id) ON DELETE RESTRICT,
    PRIMARY KEY (project_id, org_unit_id)
);

-- Older copies of this migration created the project FK as RESTRICT, which
-- broke project deletion. Re-point it to CASCADE (idempotent pair).
ALTER TABLE workspace_project_org_unit DROP CONSTRAINT IF EXISTS workspace_project_org_unit_project_id_fkey;
ALTER TABLE workspace_project_org_unit
    ADD CONSTRAINT workspace_project_org_unit_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES workspace_project(id) ON DELETE CASCADE;

ALTER TABLE workspace_agent      ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;
ALTER TABLE workspace_skill      ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;
ALTER TABLE workspace_mcp_server ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;
ALTER TABLE workspace_provider   ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;
ALTER TABLE workspace_user       ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS org_unit_parent_idx ON org_unit(parent_id);
CREATE INDEX IF NOT EXISTS workspace_agent_org_idx      ON workspace_agent(org_unit_id);
CREATE INDEX IF NOT EXISTS workspace_skill_org_idx      ON workspace_skill(org_unit_id);
CREATE INDEX IF NOT EXISTS workspace_mcp_server_org_idx ON workspace_mcp_server(org_unit_id);
CREATE INDEX IF NOT EXISTS workspace_provider_org_idx   ON workspace_provider(org_unit_id);
CREATE INDEX IF NOT EXISTS workspace_user_org_idx       ON workspace_user(org_unit_id);
