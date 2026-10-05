-- Triggers join the org visibility model (docs/org-structure.md §18) and can
-- carry the execution identity their automated runs execute under (§20-21):
-- a trigger run never inherits its creator's rights.
ALTER TABLE workspace_trigger ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;
ALTER TABLE workspace_trigger ADD COLUMN IF NOT EXISTS execution_identity_id TEXT NULL REFERENCES execution_identity(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS workspace_trigger_org_unit_idx ON workspace_trigger(org_unit_id);
