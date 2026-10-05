-- Human requests as a first-class entity (docs/org-structure.md §28,
-- docs/triggers-and-escalations.md §10). One row per ask_human call, keyed by
-- the operation id so the approval endpoint can close it atomically.
CREATE TABLE IF NOT EXISTS human_request (
    id TEXT PRIMARY KEY,                     -- operation id of the ask_human call
    run_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    agent_id TEXT NOT NULL DEFAULT '',
    recipient TEXT NOT NULL DEFAULT '',      -- logical recipient as requested (role:qa, org:unit, project_owner, user:id, '')
    resolved_user TEXT NOT NULL DEFAULT '',  -- concrete workspace user after resolution
    question TEXT NOT NULL,
    context TEXT NOT NULL DEFAULT '',
    options JSONB NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'pending',  -- pending | delivered | answered | expired | cancelled | rejected
    channel TEXT NOT NULL DEFAULT '',        -- delivery transport used (web | matrix | telegram)
    response TEXT NOT NULL DEFAULT '',
    answered_by TEXT NOT NULL DEFAULT '',
    timeout_seconds INTEGER NOT NULL DEFAULT 0,
    timeout_policy TEXT NOT NULL DEFAULT 'fallback',
    execution_identity_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    answered_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_human_request_status ON human_request (status);
CREATE INDEX IF NOT EXISTS idx_human_request_project ON human_request (project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_human_request_resolved_user ON human_request (resolved_user, status);
