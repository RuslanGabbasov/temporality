-- Server-side chat persistence (docs/resilience-and-scale.md §4): UI chats
-- used to live only in browser localStorage — silently lost on browser data
-- wipes and invisible on other devices. The workspace DB is the durable
-- copy; the transcript shape is owned by the UI (stored as JSON, opaque to
-- the backend).
CREATE TABLE IF NOT EXISTS workspace_chat (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL DEFAULT '',
    agent_id   TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL DEFAULT '',
    transcript JSONB NOT NULL DEFAULT '[]',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_workspace_chat_project
    ON workspace_chat (project_id, updated_at DESC);
