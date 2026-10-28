-- Artifact tags (like real knowledge bases): a catalog of reference
-- documents needs labels to filter tens of thousands of entries by topic,
-- product or team. Tags are catalog metadata on the artifact row — approved
-- bodies stay immutable, tagging is not a content edit.
-- Idempotent: the set re-runs on every boot (workspace/store.go MigrateAll).
ALTER TABLE public.workspace_artifact ADD COLUMN IF NOT EXISTS tags text[] DEFAULT '{}' NOT NULL;
