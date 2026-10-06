ALTER TABLE workspace_skill_version DROP COLUMN IF EXISTS origin;
ALTER TABLE workspace_skill_version DROP COLUMN IF EXISTS source_runs;
ALTER TABLE workspace_skill_version DROP COLUMN IF EXISTS evidence_refs;
ALTER TABLE workspace_skill_version DROP COLUMN IF EXISTS knowledge_ids;
ALTER TABLE workspace_skill_version DROP COLUMN IF EXISTS change_summary;
ALTER TABLE workspace_skill_version DROP COLUMN IF EXISTS status;
DROP INDEX IF EXISTS idx_workspace_skill_version_status;
