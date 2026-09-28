DROP INDEX IF EXISTS idx_workspace_user_token;
ALTER TABLE workspace_user DROP COLUMN IF EXISTS token;
