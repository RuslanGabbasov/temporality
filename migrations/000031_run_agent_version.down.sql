DROP INDEX IF EXISTS idx_workspace_run_agent;
ALTER TABLE workspace_run DROP COLUMN IF EXISTS agent_version;
