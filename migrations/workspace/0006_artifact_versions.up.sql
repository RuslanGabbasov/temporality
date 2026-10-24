-- Artifact version history (docs/artifact-lifecycle.md §5, §12 Phase 3 —
-- pulled forward): every approved version is snapshotted before a new draft
-- revision opens, so «создать новую редакцию» stops destroying the approved
-- body. One row per (artifact, version); the live workspace_artifact row
-- always carries the current version.
CREATE TABLE IF NOT EXISTS public.workspace_artifact_version (
    artifact_id text NOT NULL,
    version integer NOT NULL,
    title text NOT NULL,
    description text NOT NULL DEFAULT ''::text,
    content text NOT NULL DEFAULT ''::text,
    content_hash text NOT NULL DEFAULT ''::text,
    size_bytes bigint NOT NULL DEFAULT 0,
    mime_type text NOT NULL DEFAULT 'text/markdown'::text,
    status text NOT NULL DEFAULT 'approved'::text,
    origin_type text NOT NULL DEFAULT 'human'::text,
    origin_user text,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    superseded_at timestamp with time zone,
    PRIMARY KEY (artifact_id, version)
);
