-- docs/knowledge-evolution.md Wave A: skill versions carry provenance and a
-- lifecycle. Agent proposals (skill_propose) land as status='draft' and never
-- become the current revision until a human applies them; origin records who
-- authored the change; source_runs / evidence_refs / knowledge_ids make the
-- evolution chain reconstructible (why version N became N+1).

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'initial';
ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS source_runs TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS evidence_refs TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS knowledge_ids TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS change_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';

CREATE INDEX IF NOT EXISTS idx_workspace_skill_version_status ON workspace_skill_version(skill_id, status);
