DROP INDEX IF EXISTS public.idx_workspace_artifact_project;
ALTER TABLE public.workspace_artifact DROP COLUMN IF EXISTS project_id;
