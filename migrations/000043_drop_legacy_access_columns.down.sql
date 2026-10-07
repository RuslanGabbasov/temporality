-- Best-effort inverse: re-create the legacy columns with their original
-- defaults (migrations 000022 / 000027). The dropped JSONB values are gone.
ALTER TABLE workspace_project ADD COLUMN IF NOT EXISTS allowed_users JSONB NOT NULL DEFAULT '"*"';
ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS projects JSONB NOT NULL DEFAULT '[]';
