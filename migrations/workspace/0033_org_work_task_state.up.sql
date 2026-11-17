-- The Work board's dismissal marker: a task card a human has analyzed and
-- decided needs no action (a failed run not worth restarting) leaves the
-- board. The row is deleted by UpsertOrgActiveRun when a NEW run starts for
-- the task — real work un-dismisses the card automatically.
CREATE TABLE IF NOT EXISTS public.org_work_task_state (
    task_id TEXT PRIMARY KEY,
    state   TEXT NOT NULL,               -- 'dismissed'
    by      TEXT NOT NULL DEFAULT '',
    at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
