-- Add allowed_users column to workspace_project
-- '*' = all users can see the project
-- [] (empty) = only admins can see
-- ['user-1', 'user-2'] = only listed users can see
ALTER TABLE workspace_project ADD COLUMN IF NOT EXISTS allowed_users JSONB NOT NULL DEFAULT '"*"';

-- Update existing projects to allow all users (backward compatible)
UPDATE workspace_project SET allowed_users = '"*"' WHERE allowed_users IS NULL OR allowed_users = 'null';