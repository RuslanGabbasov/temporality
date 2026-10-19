-- Restoring NOT NULL requires no orphan rows: task-less runs (intake) must go.
DELETE FROM workspace_run WHERE task_id IS NULL;
ALTER TABLE workspace_run ALTER COLUMN task_id SET NOT NULL;
