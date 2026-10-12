-- Org unit head (docs/inbound-messages.md §7): the explicit manager edge the
-- escalation walk previously lacked. Effective head resolves bottom-up: a unit
-- without a head inherits the nearest ancestor's head. NULL keeps today's
-- behavior (proximity-based escalation), so existing installations change
-- nothing until a head is assigned explicitly.
ALTER TABLE org_unit ADD COLUMN IF NOT EXISTS head_user_id TEXT REFERENCES workspace_user(id) ON DELETE SET NULL;
