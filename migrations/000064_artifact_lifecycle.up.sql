-- Artifact lifecycle (docs/artifact-lifecycle.md): agents create and reuse
-- artifacts; only a human approval moves a version to `approved`, and only
-- approved versions may be referenced from skill manifests.

-- Existing rows are human-created through the UI — that explicit act is their
-- approval. They become approved v1 reference documents; origin records who
-- signed them in.
ALTER TABLE workspace_artifact
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'approved',
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'reference',
    ADD COLUMN IF NOT EXISTS version INT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS origin_type TEXT NOT NULL DEFAULT 'human',
    ADD COLUMN IF NOT EXISTS origin_user TEXT,
    ADD COLUMN IF NOT EXISTS origin_run TEXT,
    ADD COLUMN IF NOT EXISTS saga_id TEXT;

-- One row per review round. A version may go through several rounds
-- (rejected → edit → submit again); each round keeps its own decision.
CREATE TABLE IF NOT EXISTS workspace_artifact_review (
    id TEXT PRIMARY KEY,
    artifact_id TEXT NOT NULL REFERENCES workspace_artifact(id) ON DELETE CASCADE,
    artifact_version INT NOT NULL,
    reviewers TEXT[] NOT NULL,
    mode TEXT NOT NULL DEFAULT 'all',          -- all | any
    status TEXT NOT NULL DEFAULT 'open',       -- open | approved | rejected | cancelled
    note TEXT NOT NULL DEFAULT '',
    requested_by TEXT NOT NULL DEFAULT '',
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Individual decisions per reviewer, kept as the review closes: mode=all
    -- needs every name and its answer to be auditable.
    decisions JSONB NOT NULL DEFAULT '{}'::jsonb,
    resolved_by TEXT,
    resolved_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS workspace_artifact_review_artifact
    ON workspace_artifact_review (artifact_id, requested_at DESC);
CREATE INDEX IF NOT EXISTS workspace_artifact_status
    ON workspace_artifact (status);
