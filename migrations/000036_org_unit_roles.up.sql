-- Org unit roles (docs/org-structure.md §13): a role granted on a unit acts
-- on the unit and its whole subtree. The effective role at a resource is the
-- maximum of the installation role and every grant whose unit is the
-- resource's unit or one of its ancestors.
CREATE TABLE IF NOT EXISTS org_unit_role (
    user_id    TEXT NOT NULL REFERENCES workspace_user(id) ON DELETE CASCADE,
    org_unit_id TEXT NOT NULL REFERENCES org_unit(id) ON DELETE RESTRICT,
    role       TEXT NOT NULL,
    granted_by TEXT NOT NULL DEFAULT '',
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, org_unit_id)
);

CREATE INDEX IF NOT EXISTS org_unit_role_unit_idx ON org_unit_role(org_unit_id);
