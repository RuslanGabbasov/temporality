ALTER TABLE public.workspace_artifact_annotation
    ADD COLUMN IF NOT EXISTS start_offset integer NOT NULL DEFAULT -1,
    ADD COLUMN IF NOT EXISTS end_offset integer NOT NULL DEFAULT -1;
