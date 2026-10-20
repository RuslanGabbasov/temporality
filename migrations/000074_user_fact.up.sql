-- Explicit durable facts and preferences about a workspace user («отвечай
-- кратко», «ведёт проект X»): accumulated from the channel dialog and the
-- user settings screen, injected into the dialogs that work for them.
CREATE TABLE IF NOT EXISTS workspace_user_fact (
    id         BIGSERIAL PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES workspace_user(id) ON DELETE CASCADE,
    fact       TEXT NOT NULL,
    origin     TEXT NOT NULL DEFAULT 'manual',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_fact_unique ON workspace_user_fact (user_id, fact);
CREATE INDEX IF NOT EXISTS idx_user_fact_user ON workspace_user_fact (user_id, created_at DESC);
