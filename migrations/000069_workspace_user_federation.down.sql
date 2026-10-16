DROP INDEX IF EXISTS idx_workspace_user_external;
ALTER TABLE workspace_user DROP COLUMN IF EXISTS last_login_at;
ALTER TABLE workspace_user DROP COLUMN IF EXISTS sourced;
ALTER TABLE workspace_user DROP COLUMN IF EXISTS external_id;
ALTER TABLE workspace_user DROP COLUMN IF EXISTS external_issuer;
