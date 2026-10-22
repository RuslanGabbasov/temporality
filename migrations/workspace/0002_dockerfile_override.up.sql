-- Operator recipe override (docs/tasks/2026-10-21-sandbox-dockerfile-editing,
-- D1): a custom profile may carry a full operator-authored Dockerfile instead
-- of base+packages. Exactly one recipe source per row (D2.1); builtins never
-- set the column (D2.4).
ALTER TABLE workspace_sandbox_profile
    ADD COLUMN IF NOT EXISTS dockerfile_override TEXT NOT NULL DEFAULT '';
