-- Federation identity on workspace_user (docs/auth-federation.md §6): the
-- (issuer, external_id) pair links a user to its IdP identity; JIT creates
-- rows with sourced='federated'. Users are never deleted — deactivation only.
ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS external_issuer TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS external_id TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS sourced TEXT NOT NULL DEFAULT 'local';
ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ;
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_user_external
    ON workspace_user(external_issuer, external_id) WHERE external_id != '';
