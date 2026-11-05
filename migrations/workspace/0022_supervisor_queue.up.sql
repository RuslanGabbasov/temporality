-- Supervisor attention queue (docs/supervisor-model.md §3, queue rewrite):
-- supervisor verdicts dedupe into CASES — one row per subject, not one row
-- per re-published verdict. The case identity is the journal event id without
-- its nano suffix (supervisor-case/<family>/<subject>/<nano>), which is
-- exactly how acks already name them.
--
-- agentSRE rollups (the ongoing series the queue and the anomaly detector
-- read): bucketed health stats per project/model, computed with the same
-- observation.ComputeHealthStats math the dashboard uses. Version-level SLI
-- comparison lives in workspace_agent_version_metrics (agent-evolution v2) —
-- this table is the OPERATIONAL series only.

CREATE TABLE IF NOT EXISTS public.workspace_supervisor_case (
    id              BIGSERIAL PRIMARY KEY,
    case_key        TEXT NOT NULL UNIQUE,
    family          TEXT NOT NULL,
    cause           TEXT NOT NULL DEFAULT '',
    subject         TEXT NOT NULL DEFAULT '',
    project         TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',
    recommendation  TEXT NOT NULL DEFAULT '',
    payload         JSONB NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'open',
    occurrences     INT NOT NULL DEFAULT 1,
    first_seen      TIMESTAMPTZ NOT NULL,
    last_seen       TIMESTAMPTZ NOT NULL,
    last_event_id   TEXT NOT NULL DEFAULT '',
    decided_by      TEXT NOT NULL DEFAULT '',
    decided_at      TIMESTAMPTZ,
    decision        TEXT NOT NULL DEFAULT '',
    CONSTRAINT supervisor_case_status CHECK (status IN ('open', 'decided', 'digest'))
);

CREATE INDEX IF NOT EXISTS idx_supervisor_case_queue
    ON public.workspace_supervisor_case (status, family, last_seen DESC);

-- One row per journal verdict event: the sync's idempotency anchor (a
-- re-processed event never double-counts an occurrence).
CREATE TABLE IF NOT EXISTS public.workspace_supervisor_case_event (
    event_id    TEXT PRIMARY KEY,
    case_key    TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_supervisor_case_event_key
    ON public.workspace_supervisor_case_event (case_key);

-- The sync watermark: verdicts newer than this are folded into cases.
CREATE TABLE IF NOT EXISTS public.workspace_supervisor_sync (
    id        BOOL PRIMARY KEY DEFAULT TRUE,
    watermark TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS public.workspace_sre_rollup (
    bucket_start        TIMESTAMPTZ NOT NULL,
    granularity         TEXT NOT NULL,
    project             TEXT NOT NULL,
    model               TEXT NOT NULL DEFAULT '',
    runs_started        INT NOT NULL DEFAULT 0,
    runs_completed      INT NOT NULL DEFAULT 0,
    runs_failed         INT NOT NULL DEFAULT 0,
    runs_cancelled      INT NOT NULL DEFAULT 0,
    runs_running        INT NOT NULL DEFAULT 0,
    model_calls         INT NOT NULL DEFAULT 0,
    model_failures      INT NOT NULL DEFAULT 0,
    model_retries       INT NOT NULL DEFAULT 0,
    model_nudged        INT NOT NULL DEFAULT 0,
    latency_p50_ms      BIGINT NOT NULL DEFAULT 0,
    latency_p95_ms      BIGINT NOT NULL DEFAULT 0,
    total_tokens        BIGINT NOT NULL DEFAULT 0,
    cached_tokens       BIGINT NOT NULL DEFAULT 0,
    approvals_requested INT NOT NULL DEFAULT 0,
    approvals_granted   INT NOT NULL DEFAULT 0,
    approvals_auto      INT NOT NULL DEFAULT 0,
    ask_human_answered  INT NOT NULL DEFAULT 0,
    hab_seconds         BIGINT NOT NULL DEFAULT 0,
    hwt_seconds         BIGINT NOT NULL DEFAULT 0,
    flow_efficiency     INT NOT NULL DEFAULT 0,
    CONSTRAINT sre_rollup_granularity CHECK (granularity IN ('hour', 'day')),
    PRIMARY KEY (bucket_start, granularity, project, model)
);

CREATE INDEX IF NOT EXISTS idx_sre_rollup_recent
    ON public.workspace_sre_rollup (granularity, bucket_start DESC);
