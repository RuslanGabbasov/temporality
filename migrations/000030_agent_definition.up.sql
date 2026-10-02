-- Evaluable Agent (docs/evaluable-agent.md, plan: docs/plan-evaluable-agent.md).
-- workspace_agent.definition holds the structured agent definition (capabilities,
-- constraints, completion criteria); the system prompt is compiled from it at
-- every run start. workspace_agent_version keeps immutable snapshots so run
-- outcomes can be correlated with the exact definition the agent ran with.

ALTER TABLE workspace_agent
    ADD COLUMN IF NOT EXISTS definition JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS definition_version INT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS workspace_agent_version (
    agent_id        TEXT NOT NULL REFERENCES workspace_agent(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    definition      JSONB NOT NULL DEFAULT '{}',
    description     TEXT NOT NULL DEFAULT '',
    compiled_prompt TEXT NOT NULL DEFAULT '',
    prompt_source   TEXT NOT NULL DEFAULT 'manual', -- manual | ai | builtin
    generator_model TEXT NOT NULL DEFAULT '',
    author          TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, version)
);

CREATE INDEX IF NOT EXISTS idx_workspace_agent_version_agent ON workspace_agent_version(agent_id);
