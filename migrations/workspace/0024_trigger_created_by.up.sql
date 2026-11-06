-- Run visibility, part 1 (docs/activity-scoping.md, run scoping): a trigger's
-- runs are visible to the operator who configured the trigger — the journal's
-- run-visibility resolver maps runs to initiators (workspace_run's
-- exec_context.actor already carries the user for chat/API/delegated runs and
-- the trigger id for automated ones), and trigger ownership needs this column.
-- Existing rows keep '' (legacy triggers: their runs stay admin-only under the
-- operator scope, same as before this feature for non-initiators).
ALTER TABLE workspace_trigger ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';
