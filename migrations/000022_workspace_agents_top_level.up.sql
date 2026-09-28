-- Make agents top-level: project_id optional, add new fields
ALTER TABLE workspace_agent ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS temperature DOUBLE PRECISION;
ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS max_tokens INT;
ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS network_access BOOLEAN;
ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS read_only BOOLEAN;
ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS max_turns INT;
ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS approval_mode TEXT;
ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS labels JSONB NOT NULL DEFAULT '{}';

-- Model providers (OpenAI-compatible endpoints)
CREATE TABLE IF NOT EXISTS workspace_provider (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    base_url    TEXT NOT NULL,
    api_key_ref TEXT NOT NULL DEFAULT '',
    models      TEXT[] NOT NULL DEFAULT '{}',
    labels      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Platform users
CREATE TABLE IF NOT EXISTS workspace_user (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    email       TEXT NOT NULL DEFAULT '',
    role        TEXT NOT NULL DEFAULT 'viewer',
    projects    JSONB NOT NULL DEFAULT '[]',
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
