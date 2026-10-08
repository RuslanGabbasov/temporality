-- Password login for workspace users (docs/org-structure.md §3.3): bcrypt
-- hash; empty string = password login not set up for that user.
ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT '';
