DROP INDEX IF EXISTS workspace_artifact_status;
DROP INDEX IF EXISTS workspace_artifact_review_artifact;
DROP TABLE IF EXISTS workspace_artifact_review;
ALTER TABLE workspace_artifact
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS kind,
    DROP COLUMN IF EXISTS version,
    DROP COLUMN IF EXISTS origin_type,
    DROP COLUMN IF EXISTS origin_user,
    DROP COLUMN IF EXISTS origin_run,
    DROP COLUMN IF EXISTS saga_id;
