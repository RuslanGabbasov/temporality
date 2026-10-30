DROP INDEX IF EXISTS public.idx_workspace_artifact_contributors;
ALTER TABLE public.workspace_artifact DROP COLUMN IF EXISTS contributors;
