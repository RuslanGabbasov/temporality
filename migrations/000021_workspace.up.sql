CREATE TABLE IF NOT EXISTS workspace_project (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_agent (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES workspace_project(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    model          TEXT NOT NULL DEFAULT '',
    system_prompt  TEXT NOT NULL DEFAULT '',
    skills         JSONB NOT NULL DEFAULT '[]',
    mcp_servers    JSONB NOT NULL DEFAULT '[]',
    sandbox_profile TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_agent_project ON workspace_agent(project_id);

CREATE TABLE IF NOT EXISTS workspace_task (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES workspace_project(id) ON DELETE CASCADE,
    agent_id    TEXT REFERENCES workspace_agent(id) ON DELETE SET NULL,
    title       TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_task_project ON workspace_task(project_id);

CREATE TABLE IF NOT EXISTS workspace_run (
    id          TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL REFERENCES workspace_task(id) ON DELETE CASCADE,
    project_id  TEXT NOT NULL REFERENCES workspace_project(id) ON DELETE CASCADE,
    agent_id    TEXT REFERENCES workspace_agent(id) ON DELETE SET NULL,
    run_id      TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    model       TEXT NOT NULL DEFAULT '',
    answer      TEXT NOT NULL DEFAULT '',
    turns       INT NOT NULL DEFAULT 0,
    error       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_run_task ON workspace_run(task_id);
CREATE INDEX IF NOT EXISTS idx_workspace_run_project ON workspace_run(project_id);