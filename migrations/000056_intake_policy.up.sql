-- Intake policy (docs/inbound-messages.md §12 phase 4): channel-initiated
-- tasks are opt-in per project, an org policy can deny them for a subtree,
-- and message bodies retire after a retention window (PII hygiene).

-- Projects accept channel-initiated tasks only when explicitly enabled.
-- Backfill keeps intake working for projects that already routed messages:
-- an upgrade must not silently turn a working channel into a dead one.
ALTER TABLE workspace_project
    ADD COLUMN IF NOT EXISTS intake_enabled BOOLEAN NOT NULL DEFAULT false;
UPDATE workspace_project SET intake_enabled = true
 WHERE NOT intake_enabled
   AND id IN (SELECT DISTINCT project_id FROM workspace_inbound_message
              WHERE project_id IS NOT NULL);

-- Retired bodies: the message row survives as the audit chain link
-- (id → intake_run_id → target_run_id), the PII payload does not.
ALTER TABLE workspace_inbound_message
    ADD COLUMN IF NOT EXISTS retired_at TIMESTAMPTZ;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_inbound_message_unretired') THEN
        CREATE INDEX idx_inbound_message_unretired
            ON workspace_inbound_message (created_at)
            WHERE retired_at IS NULL;
    END IF;
END $$;

-- Org policy overlay: '' = no opinion, 'deny' = no channel-initiated tasks
-- in this subtree (restrictive merge: a child cannot loosen an ancestor's
-- deny). There is no 'allow' — enabling happens on the project.
ALTER TABLE org_policy
    ADD COLUMN IF NOT EXISTS intake_mode TEXT NOT NULL DEFAULT ''
    CHECK (intake_mode IN ('', 'deny'));
