-- specs/quality-loop.md §6 (M4): roll back the judge quality-loop schema.
DROP TABLE IF EXISTS workspace_judge_calibration;
DROP INDEX IF EXISTS idx_workspace_agent_evaluation_run_gate;
ALTER TABLE workspace_agent_evaluation_run DROP COLUMN IF EXISTS verdict;
ALTER TABLE workspace_agent_evaluation_run DROP COLUMN IF EXISTS cause;
ALTER TABLE workspace_agent_evaluation_run DROP COLUMN IF EXISTS threshold;
ALTER TABLE workspace_agent_evaluation_run DROP COLUMN IF EXISTS judge_model;
ALTER TABLE workspace_agent_evaluation_run DROP COLUMN IF EXISTS judge_same_model;
ALTER TABLE workspace_agent_evaluation_run DROP COLUMN IF EXISTS judge_degraded;
ALTER TABLE workspace_agent_evaluation_run DROP COLUMN IF EXISTS trigger;
