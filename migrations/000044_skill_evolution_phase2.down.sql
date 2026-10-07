ALTER TABLE workspace_skill_version DROP COLUMN IF EXISTS observed_problem;
ALTER TABLE workspace_skill_version DROP COLUMN IF EXISTS proposed_change;
ALTER TABLE workspace_skill_version DROP COLUMN IF EXISTS expected_effect;
DROP TABLE IF EXISTS workspace_skill_evaluation_run;
DROP TABLE IF EXISTS workspace_skill_evaluation_suite;
