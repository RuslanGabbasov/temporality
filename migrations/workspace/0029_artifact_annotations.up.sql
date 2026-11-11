CREATE TABLE IF NOT EXISTS public.workspace_artifact_annotation (
    id text PRIMARY KEY,
    artifact_id text NOT NULL REFERENCES public.workspace_artifact(id) ON DELETE CASCADE,
    artifact_version integer NOT NULL,
    kind text NOT NULL CHECK (kind IN ('comment','change','approve','reject')),
    quote text NOT NULL,
    replacement text NOT NULL DEFAULT '',
    comment text NOT NULL DEFAULT '',
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS workspace_artifact_annotation_version
    ON public.workspace_artifact_annotation (artifact_id, artifact_version, created_at);
