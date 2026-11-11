ALTER TABLE public.workspace_artifact_annotation
    DROP COLUMN IF EXISTS start_offset,
    DROP COLUMN IF EXISTS end_offset;
