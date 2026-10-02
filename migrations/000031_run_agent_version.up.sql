-- Runs remember which agent definition version they were launched with, so
-- the agent Evolution view can attribute outcomes to an exact definition
-- snapshot instead of a time window (docs/evaluable-agent.md §15).
ALTER TABLE workspace_run ADD COLUMN IF NOT EXISTS agent_version INT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_workspace_run_agent ON workspace_run(agent_id);
