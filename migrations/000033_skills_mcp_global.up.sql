-- Skills and MCP servers become workspace-global assets, like agents: they
-- are cross-functional and shared by every project.
--
-- The migration chain re-runs idempotently on every kernel start, so the
-- legacy project_id columns must stay (000028/000029 still reference them);
-- they become nullable vestiges and are cleared, which also removes the
-- ON DELETE CASCADE hazard for skills bound to deleted projects. Project
-- provenance lives on in workspace_skill_execution.project_id.
-- ADD COLUMN IF NOT EXISTS self-heals databases where the column was dropped
-- outright by an earlier build of this migration.

ALTER TABLE workspace_skill ADD COLUMN IF NOT EXISTS project_id TEXT;
ALTER TABLE workspace_mcp_server ADD COLUMN IF NOT EXISTS project_id TEXT;

ALTER TABLE workspace_skill ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE workspace_mcp_server ALTER COLUMN project_id DROP NOT NULL;

UPDATE workspace_skill SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE workspace_mcp_server SET project_id = NULL WHERE project_id IS NOT NULL;
