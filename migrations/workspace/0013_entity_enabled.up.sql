-- Uniform enable/disable for workspace entities (the same mechanism the
-- trigger table already has): a disabled agent cannot start new runs, a
-- disabled skill is never served to agents. Users keep workspace_user.active.
-- Idempotent: the set re-runs on every boot (workspace/store.go MigrateAll).
ALTER TABLE public.workspace_agent ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true;
ALTER TABLE public.workspace_skill ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true;
