-- Revert agents to project-bound
DELETE FROM workspace_agent WHERE project_id IS NULL;
ALTER TABLE workspace_agent ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE workspace_agent DROP COLUMN IF EXISTS description;
ALTER TABLE workspace_agent DROP COLUMN IF EXISTS provider;
ALTER TABLE workspace_agent DROP COLUMN IF EXISTS temperature;
ALTER TABLE workspace_agent DROP COLUMN IF EXISTS max_tokens;
ALTER TABLE workspace_agent DROP COLUMN IF EXISTS network_access;
ALTER TABLE workspace_agent DROP COLUMN IF EXISTS read_only;
ALTER TABLE workspace_agent DROP COLUMN IF EXISTS max_turns;
ALTER TABLE workspace_agent DROP COLUMN IF EXISTS approval_mode;
ALTER TABLE workspace_agent DROP COLUMN IF EXISTS labels;
DROP TABLE IF EXISTS workspace_user;
DROP TABLE IF EXISTS workspace_provider;
