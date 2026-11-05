-- Agent evolution v2 (docs/agent-evolution.md): version lineage (kind,
-- baseline_version, rollback_of, change_summary), proposal sources with the
-- applied-version pin, per-agent observation windows, the daily version
-- metrics rollup and the check-case store. The evaluation suite/run,
-- golden-case candidate and judge-calibration tables of the retired quality
-- loop (docs/agent-quality-loop.md) are dropped with their data — the
-- replacement check cases are collected fresh.
-- Idempotent: the file set re-runs on every boot (workspace/store.go MigrateAll).

ALTER TABLE public.workspace_agent_version ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'edit';
ALTER TABLE public.workspace_agent_version ADD COLUMN IF NOT EXISTS baseline_version INT;
ALTER TABLE public.workspace_agent_version ADD COLUMN IF NOT EXISTS rollback_of INT;
ALTER TABLE public.workspace_agent_version ADD COLUMN IF NOT EXISTS change_summary TEXT NOT NULL DEFAULT '';

ALTER TABLE public.workspace_agent_proposal ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'agent';
ALTER TABLE public.workspace_agent_proposal ADD COLUMN IF NOT EXISTS change_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE public.workspace_agent_proposal ADD COLUMN IF NOT EXISTS context JSONB;
ALTER TABLE public.workspace_agent_proposal ADD COLUMN IF NOT EXISTS applied_version INT;

ALTER TABLE public.workspace_agent ADD COLUMN IF NOT EXISTS observation_days INT NOT NULL DEFAULT 7;
ALTER TABLE public.workspace_agent ADD COLUMN IF NOT EXISTS observation_min_runs INT NOT NULL DEFAULT 10;

CREATE TABLE IF NOT EXISTS public.workspace_agent_version_metrics (
    agent_id TEXT NOT NULL,
    version INT NOT NULL,
    day DATE NOT NULL,
    runs INT NOT NULL DEFAULT 0,
    succeeded INT NOT NULL DEFAULT 0,
    failed INT NOT NULL DEFAULT 0,
    cancelled INT NOT NULL DEFAULT 0,
    turn_limited INT NOT NULL DEFAULT 0,
    verdict_accepted INT NOT NULL DEFAULT 0,
    verdict_corrected INT NOT NULL DEFAULT 0,
    verdict_rejected INT NOT NULL DEFAULT 0,
    interventions INT NOT NULL DEFAULT 0,
    reworks INT NOT NULL DEFAULT 0,
    tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    wall_ms BIGINT NOT NULL DEFAULT 0,
    uncertain_ops INT NOT NULL DEFAULT 0,
    incidents INT NOT NULL DEFAULT 0,
    PRIMARY KEY (agent_id, version, day)
);

CREATE TABLE IF NOT EXISTS public.workspace_agent_check_case (
    id BIGSERIAL PRIMARY KEY,
    agent_id TEXT NOT NULL,
    input TEXT NOT NULL,
    must_contain TEXT[] NOT NULL DEFAULT '{}',
    must_not_contain TEXT[] NOT NULL DEFAULT '{}',
    from_run_id TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

DROP TABLE IF EXISTS public.workspace_agent_evaluation_suite, public.workspace_agent_evaluation_run, public.workspace_eval_case_candidate, public.workspace_judge_calibration;
