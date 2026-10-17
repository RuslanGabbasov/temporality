ALTER TABLE workspace_sandbox_profile
    DROP COLUMN IF EXISTS packages,
    DROP COLUMN IF EXISTS base;
