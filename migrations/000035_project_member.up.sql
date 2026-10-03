-- Explicit project membership and the minimal access audit journal
-- (docs/org-structure.md §16, §36).
--
-- workspace_project_member makes project access = org-unit access OR explicit
-- membership: a member sees the project even when their org chain does not
-- intersect the project's units (cross-functional teams, external
-- collaborators). Both sides cascade: deleting a project or a user drops its
-- membership rows.
CREATE TABLE IF NOT EXISTS workspace_project_member (
    project_id TEXT NOT NULL REFERENCES workspace_project(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES workspace_user(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'writer',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (project_id, user_id)
);

CREATE INDEX IF NOT EXISTS workspace_project_member_user_idx ON workspace_project_member(user_id);

-- Access audit (docs/org-structure.md §36): who changed which binding,
-- membership or org-tree node and when. Append-only; written by the kernel
-- after every access-relevant mutation.
CREATE TABLE IF NOT EXISTS access_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    actor       TEXT NOT NULL,
    action      TEXT NOT NULL,
    entity_kind TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    details     JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS access_audit_log_entity_idx ON access_audit_log(entity_kind, entity_id);
CREATE INDEX IF NOT EXISTS access_audit_log_created_idx ON access_audit_log(created_at);
