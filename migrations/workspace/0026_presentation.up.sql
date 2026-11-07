-- Presentations (docs/presentation-engine.md §12–§13): the durable store of
-- agent-authored structured documents. One row is one communication record —
-- the IR lives in a jsonb column, the lifecycle state machine lives in the
-- kernel (kernel/presentation), and the revision history keeps every
-- published version so update_presentation never rewrites the past.
--
-- The id is agent- or server-supplied (slug), project-scoped like every
-- workspace row. run_id/task_id/agent_id carry the §13 provenance links;
-- text_fallback caches the server-rendered plain-text form (§7.6) so text
-- channels never re-render; expires_at drives the Expired lifecycle state
-- (presentations are communication records, not knowledge — §13).

CREATE TABLE IF NOT EXISTS public.workspace_presentation (
    id text NOT NULL,
    project_id text NOT NULL,
    run_id text DEFAULT ''::text NOT NULL,
    task_id text DEFAULT ''::text NOT NULL,
    agent_id text DEFAULT ''::text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    document jsonb NOT NULL,
    text_fallback text DEFAULT ''::text NOT NULL,
    artifacts jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_by text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    published_at timestamp with time zone,
    expires_at timestamp with time zone
);

CREATE TABLE IF NOT EXISTS public.workspace_presentation_revision (
    presentation_id text NOT NULL,
    version integer NOT NULL,
    document jsonb NOT NULL,
    changed_by text DEFAULT ''::text NOT NULL,
    changed_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (presentation_id, version)
);

DO $con$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_presentation_pkey') THEN
        ALTER TABLE ONLY public.workspace_presentation ADD CONSTRAINT workspace_presentation_pkey PRIMARY KEY (id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_presentation_status_check') THEN
        ALTER TABLE ONLY public.workspace_presentation ADD CONSTRAINT workspace_presentation_status_check
            CHECK (status IN ('draft', 'published', 'updated', 'archived', 'expired'));
    END IF;
END $con$;

CREATE INDEX IF NOT EXISTS workspace_presentation_project_idx ON workspace_presentation (project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS workspace_presentation_run_idx ON workspace_presentation (run_id) WHERE run_id <> '';
