-- Open revisions live in the version table as draft rows (docs/artifact-
-- lifecycle.md §5: the approved version stays live and served while a
-- revision is open, so the draft carries its own full document fields).
ALTER TABLE public.workspace_artifact_version
    ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT 'builtin'::text,
    ADD COLUMN IF NOT EXISTS external_ref text NOT NULL DEFAULT ''::text,
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'reference'::text,
    ADD COLUMN IF NOT EXISTS saga_id text;
