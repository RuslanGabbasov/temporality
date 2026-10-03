DROP INDEX IF EXISTS workspace_user_org_idx;
DROP INDEX IF EXISTS workspace_provider_org_idx;
DROP INDEX IF EXISTS workspace_mcp_server_org_idx;
DROP INDEX IF EXISTS workspace_skill_org_idx;
DROP INDEX IF EXISTS workspace_agent_org_idx;
DROP INDEX IF EXISTS org_unit_parent_idx;

ALTER TABLE workspace_user       DROP COLUMN IF EXISTS org_unit_id;
ALTER TABLE workspace_provider   DROP COLUMN IF EXISTS org_unit_id;
ALTER TABLE workspace_mcp_server DROP COLUMN IF EXISTS org_unit_id;
ALTER TABLE workspace_skill      DROP COLUMN IF EXISTS org_unit_id;
ALTER TABLE workspace_agent      DROP COLUMN IF EXISTS org_unit_id;

DROP TABLE IF EXISTS workspace_project_org_unit;
DROP TABLE IF EXISTS org_unit;
