-- Living Skills registry (docs/living-skills.md).
-- workspace_skill holds the current revision; every content change produces an
-- immutable workspace_skill_version row. Executions link runs to the exact
-- skill version used, so later evolution never re-binds history (§39).

CREATE TABLE IF NOT EXISTS workspace_skill (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES workspace_project(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    version     TEXT NOT NULL DEFAULT '1.0.0',
    markdown    TEXT NOT NULL DEFAULT '',
    manifest    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_skill_version (
    skill_id    TEXT NOT NULL REFERENCES workspace_skill(id) ON DELETE CASCADE,
    version     TEXT NOT NULL,
    markdown    TEXT NOT NULL DEFAULT '',
    manifest    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (skill_id, version)
);

CREATE TABLE IF NOT EXISTS workspace_skill_execution (
    id            BIGSERIAL PRIMARY KEY,
    skill_id      TEXT NOT NULL REFERENCES workspace_skill(id) ON DELETE CASCADE,
    skill_version TEXT NOT NULL,
    project_id    TEXT NOT NULL,
    run_id        TEXT NOT NULL,
    agent_id      TEXT NOT NULL DEFAULT '',
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_skill_project ON workspace_skill(project_id);
CREATE INDEX IF NOT EXISTS idx_workspace_skill_execution_skill ON workspace_skill_execution(skill_id);
CREATE INDEX IF NOT EXISTS idx_workspace_skill_execution_run ON workspace_skill_execution(run_id);
