-- Research sessions (docs/human-knowledge-explorer.md §11): the saved state
-- of a knowledge investigation — question, filters, navigation path, notes
-- and the user's conclusion. The payload holds REFERENCES (filter values,
-- area keys, knowledge ids on the nav path), never copies of knowledge:
-- opening a session resolves the CURRENT projection and the reader's CURRENT
-- rights, so revoked visibility or a changed statement is honored (§11.3).

CREATE TABLE IF NOT EXISTS workspace_research_session (
    id         BIGSERIAL PRIMARY KEY,
    project    TEXT NOT NULL,
    title      TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    payload    JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_research_session_project
    ON workspace_research_session (project, updated_at DESC);
