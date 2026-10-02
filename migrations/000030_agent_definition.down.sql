DROP TABLE IF EXISTS workspace_agent_version;
ALTER TABLE workspace_agent
    DROP COLUMN IF EXISTS definition,
    DROP COLUMN IF EXISTS definition_version;
