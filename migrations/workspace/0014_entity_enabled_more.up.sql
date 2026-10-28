-- Uniform enable/disable reaches the rest of the workspace entities (the
-- trigger/MCP pattern): a disabled team cannot be launched, a disabled
-- sandbox profile cannot be resolved for new runs, a disabled secret stops
-- resolving to values. Users keep workspace_user.active.
-- Idempotent: the set re-runs on every boot (workspace/store.go MigrateAll).
ALTER TABLE public.workspace_team ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true;
ALTER TABLE public.workspace_sandbox_profile ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true;
ALTER TABLE public.workspace_secret ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true;
