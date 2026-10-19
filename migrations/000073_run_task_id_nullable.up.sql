-- Task-less runs (intake, docs/inbound-messages.md) never got a workspace_run
-- row: task_id was NOT NULL, the insert failed and the error was ignored, so
-- the channel answer watcher and the workspace run API found nothing. The FK
-- stays — it constrains only non-NULL values, so real task runs keep their
-- integrity.
ALTER TABLE workspace_run ALTER COLUMN task_id DROP NOT NULL;
