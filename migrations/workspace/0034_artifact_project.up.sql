-- Project attribution for artifacts (docs/artifact-registry.md): an empty
-- project_id keeps the registry-global contract (org_unit_id NULL = вся
-- инсталляция), a non-empty one pins the artifact to one project — work
-- products (specs, mockups, reports) surface in their project's catalog
-- view first while staying reachable from the global listing.
ALTER TABLE public.workspace_artifact ADD COLUMN IF NOT EXISTS project_id text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_workspace_artifact_project
    ON public.workspace_artifact (project_id, updated_at DESC);
