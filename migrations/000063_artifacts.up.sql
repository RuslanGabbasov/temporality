-- Reference artifact registry (docs/artifact-registry.md).
-- workspace_artifact holds reference documents (regulations, approved
-- instructions, checklists) that skills point at from their manifests but
-- never inline into the prompt: agents load them on demand through
-- artifact_read, chunked, with a content checksum for audit.
--
-- provider=builtin stores the document body in content (bounded 2 MiB on
-- the API level); provider=url keeps only the external link (external_ref)
-- with the body living in the corporate system (Confluence, СЭД, S3-backed
-- portals); future providers (s3, sed) reuse external_ref the same way,
-- which is why provider carries no CHECK constraint.
--
-- content_hash is the sha256 of the full body, recomputed on every write:
-- artifact.read journal events carry it, so "which document version
-- informed this decision" stays reconstructible without body snapshots.

CREATE TABLE IF NOT EXISTS workspace_artifact (
    id           TEXT PRIMARY KEY,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    provider     TEXT NOT NULL DEFAULT 'builtin',
    external_ref TEXT NOT NULL DEFAULT '',
    content      TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    size_bytes   BIGINT NOT NULL DEFAULT 0,
    mime_type    TEXT NOT NULL DEFAULT 'text/markdown',
    org_unit_id  TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_artifact_org ON workspace_artifact(org_unit_id);
