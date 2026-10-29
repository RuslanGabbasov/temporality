-- Uniform enable/disable reaches model providers (the trigger/MCP pattern):
-- a disabled provider refuses to serve model calls — runs referencing it fail
-- loudly instead of looping on provider auth errors.
-- Idempotent: the set re-runs on every boot (workspace/store.go MigrateAll).
ALTER TABLE public.workspace_provider ADD COLUMN IF NOT EXISTS enabled boolean DEFAULT true NOT NULL;
