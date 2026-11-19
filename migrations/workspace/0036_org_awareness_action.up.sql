-- The supervisor's urgent_steer may carry an executable action (docs/
-- organizational-operations-spec §7.2): cancel_run terminates the target run,
-- cancel_subtree terminates it plus every live run under it. Empty = the
-- default informational note delivery.
ALTER TABLE public.org_awareness_delivery
    ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT '';
