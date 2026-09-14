CREATE TABLE IF NOT EXISTS planner_runs (
    execution_id UUID PRIMARY KEY REFERENCES executions(execution_id) ON DELETE CASCADE,
    context JSONB NOT NULL,
    state JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running','completed','failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS planner_steps (
    execution_id UUID NOT NULL REFERENCES planner_runs(execution_id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    proposal JSONB NOT NULL,
    result JSONB,
    status TEXT NOT NULL CHECK (status IN ('proposed','completed','failed')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (execution_id, ordinal)
);

CREATE INDEX IF NOT EXISTS planner_steps_execution_idx ON planner_steps(execution_id, ordinal);
