-- Auto-continuation switch (docs/agent-async-operations.md): the number of
-- automatic restarts when a run finishes by exhausting its turn budget
-- (status turn_limit). NULL/0 = off — the legacy honest-stop behavior.
-- Idempotent: the ALTER re-runs on every boot (workspace/store.go MigrateAll).
ALTER TABLE public.workspace_agent ADD COLUMN IF NOT EXISTS turn_limit_restarts int;
