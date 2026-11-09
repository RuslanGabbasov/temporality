-- Agent vision policy (docs/harness-settings.md): how many most-recent
-- screenshot messages stay in the conversation; older vision blocks are
-- evicted as new ones land (one-time mutation per message, the message keeps
-- an explicit notice). NULL/0 = keep everything — the pre-existing behavior.
-- Idempotent: the ALTER re-runs on every boot (workspace/store.go MigrateAll).
ALTER TABLE public.workspace_agent ADD COLUMN IF NOT EXISTS vision_keep_images int;
