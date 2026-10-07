-- Wave G cleanup (docs/org-structure.md §48 stage 5, docs/plan-org-structure.md):
-- drop the legacy JSONB access lists. Reading them for access decisions was
-- disabled earlier (wave B froze allowed_users, wave C moved user project
-- scopes to VisibleProjectIDs); the org model (org_unit tree, resource
-- org_unit_id bindings, workspace_project_org_unit, workspace_project_member,
-- workspace_user.org_unit_id) is the single source of truth.
-- The pre-drop divergence check is scripts/access-divergence.sql.

ALTER TABLE workspace_project DROP COLUMN IF EXISTS allowed_users;
ALTER TABLE workspace_user DROP COLUMN IF EXISTS projects;
