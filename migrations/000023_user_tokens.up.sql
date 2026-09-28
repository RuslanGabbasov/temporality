-- Add token column to workspace_user for database-stored tokens
ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS token TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_workspace_user_token ON workspace_user(token) WHERE token != '';
