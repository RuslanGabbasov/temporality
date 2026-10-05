DROP INDEX IF EXISTS workspace_trigger_org_unit_idx;
ALTER TABLE workspace_trigger DROP COLUMN IF EXISTS execution_identity_id;
ALTER TABLE workspace_trigger DROP COLUMN IF EXISTS org_unit_id;
