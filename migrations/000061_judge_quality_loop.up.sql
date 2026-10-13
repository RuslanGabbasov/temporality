-- specs/quality-loop.md §6 (M4): LLM-judge with calibration and thresholds.
-- The run verdict columns are the shared §5.1 contract with the eval gate
-- (M3); every statement is idempotent so a parallel M3 migration touching
-- the same columns no-ops instead of colliding.

-- Unified run verdict (§5.2): pass | fail | error; legacy rows keep the
-- 'unknown' default and never enter the gate. cause explains error/fail.
ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS verdict TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS cause TEXT NOT NULL DEFAULT '';
-- Snapshot of the thresholds that governed the verdict (§5.1): min pass
-- rate, regression tolerance and the per-case judge thresholds.
ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS threshold JSONB NOT NULL DEFAULT '{}';
-- Judge metadata (§6.2-6.4): which judge model scored the run, whether it
-- equals the tested model (self-bias marker, §11.1) and whether the latest
-- calibration rated the judge degraded below the 0.7 agreement floor.
ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS judge_model TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS judge_same_model BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS judge_degraded BOOLEAN NOT NULL DEFAULT false;
-- What started the run: manual | gate | schedule (§5.1).
ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS trigger TEXT NOT NULL DEFAULT 'manual';

-- Gate lookup (§10.2): the gate reads the latest pass on a version by
-- (agent_id, agent_version, verdict, recency) without scanning history.
CREATE INDEX IF NOT EXISTS idx_workspace_agent_evaluation_run_gate
    ON workspace_agent_evaluation_run (agent_id, agent_version, verdict, created_at DESC);

-- Calibration history (§6.4): agreement of the installation-wide judge model
-- against known verdicts, kept per agent that ran the calibration so the UI
-- can show it next to the judge cases.
CREATE TABLE IF NOT EXISTS workspace_judge_calibration (
    id                 BIGSERIAL PRIMARY KEY,
    agent_id           TEXT NOT NULL REFERENCES workspace_agent(id) ON DELETE CASCADE,
    judge_model        TEXT NOT NULL,
    agreement_rate     DOUBLE PRECISION NOT NULL,
    mean_abs_deviation DOUBLE PRECISION NOT NULL,
    histogram          JSONB NOT NULL DEFAULT '[]',
    degraded           BOOLEAN NOT NULL DEFAULT false,
    cases_checked      INT NOT NULL DEFAULT 0,
    cases_snapshot     JSONB NOT NULL DEFAULT '[]',
    created_by         TEXT NOT NULL DEFAULT '',
    checked_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_judge_calibration_agent
    ON workspace_judge_calibration (agent_id, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_judge_calibration_model
    ON workspace_judge_calibration (judge_model, checked_at DESC);
