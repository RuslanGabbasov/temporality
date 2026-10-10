-- docs/plan-evaluable-agent.md §2.2 stage 8: per-agent evaluation suites and
-- runs. Mirrors the skill evaluation tables (000044): a suite is a list of
-- cases stored per agent; a run records per-case outcomes. The key difference:
-- a run is pinned to the definition version whose compiled prompt snapshot was
-- tested, so version comparison compares definitions, not prompt drift.

CREATE TABLE IF NOT EXISTS workspace_agent_evaluation_suite (
    agent_id   TEXT PRIMARY KEY REFERENCES workspace_agent(id) ON DELETE CASCADE,
    cases      JSONB NOT NULL DEFAULT '[]',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_agent_evaluation_run (
    id            BIGSERIAL PRIMARY KEY,
    agent_id      TEXT NOT NULL REFERENCES workspace_agent(id) ON DELETE CASCADE,
    agent_version INT NOT NULL,
    passed        INT NOT NULL DEFAULT 0,
    failed        INT NOT NULL DEFAULT 0,
    cases         JSONB NOT NULL DEFAULT '[]',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_agent_evaluation_run ON workspace_agent_evaluation_run(agent_id, created_at DESC);
