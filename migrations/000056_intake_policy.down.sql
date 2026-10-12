ALTER TABLE org_policy DROP COLUMN IF EXISTS intake_mode;
DROP INDEX IF EXISTS idx_inbound_message_unretired;
ALTER TABLE workspace_inbound_message DROP COLUMN IF EXISTS retired_at;
ALTER TABLE workspace_project DROP COLUMN IF EXISTS intake_enabled;
