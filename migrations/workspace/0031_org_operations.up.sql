-- Organizational Operations (docs/temporality-organizational-operations-spec.md):
-- node settings with inheritance, the Organizational Activity projection
-- (watermark + idempotent anchor fold of lifecycle journal events), the
-- active-run snapshot, supervisor batches with decisions and the awareness
-- delivery ledger. Derived projections only — the journal stays the source of
-- truth; every row carries a reference back to its source event.

-- §5: per-node settings. org_unit_id = '' is the installation-wide (system)
-- row; resolution walks the unit's ancestor chain, nearest explicit value
-- wins, system row is the fallback, defaults last (org_policy pattern,
-- sources are returned so the UI can show where each value comes from).
CREATE TABLE IF NOT EXISTS public.org_activity_setting (
    org_unit_id TEXT NOT NULL DEFAULT '',
    key         TEXT NOT NULL,
    value       TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_unit_id, key)
);

-- §4: the projection sync watermark (single row, workspace_supervisor_sync
-- shape).
CREATE TABLE IF NOT EXISTS public.org_activity_sync (
    id        BOOL PRIMARY KEY DEFAULT TRUE,
    watermark TIMESTAMPTZ NOT NULL
);

-- §4.3: one projection row per folded journal event. (source_id, event_id) is
-- the journal's own identity — replays upsert idempotently. org_node_id is
-- the launch-time attribution SNAPSHOT (spec §3: a transfer must not move
-- history), org_node_path the materialized path at fold time.
CREATE TABLE IF NOT EXISTS public.org_activity (
    source_id         TEXT NOT NULL,
    event_id          TEXT NOT NULL,
    event_type        TEXT NOT NULL,
    category          TEXT NOT NULL DEFAULT '',   -- run|progress|plan|task|artifact|quality|human
    occurred_at       TIMESTAMPTZ NOT NULL,
    received_at       TIMESTAMPTZ,
    project_id        TEXT NOT NULL DEFAULT '',
    run_id            TEXT NOT NULL DEFAULT '',
    task_id           TEXT NOT NULL DEFAULT '',
    plan_id           TEXT NOT NULL DEFAULT '',
    actor_id          TEXT NOT NULL DEFAULT '',
    org_node_id       TEXT NOT NULL DEFAULT '',
    org_node_path     TEXT NOT NULL DEFAULT '',
    summary           TEXT NOT NULL DEFAULT '',
    visibility_policy TEXT NOT NULL DEFAULT 'org-unit',
    refs              JSONB NOT NULL DEFAULT '{}', -- artifact/component/delegation refs from data
    source_ref        JSONB NOT NULL DEFAULT '{}', -- source.id, integration, evidence — provenance, not content
    schema_version    INT  NOT NULL DEFAULT 1,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_id, event_id)
);

CREATE INDEX IF NOT EXISTS idx_org_activity_node_time
    ON public.org_activity (org_node_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_org_activity_run
    ON public.org_activity (run_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_org_activity_type
    ON public.org_activity (event_type, occurred_at DESC);

-- §4/§6: the active-run snapshot the Supervisor batch reads (no trace reads).
-- One row per known run; attribution resolved once at the first folded event
-- of the run and reused for every later event of that run. status mirrors the
-- journal terminal events; urgent flags critical events for out-of-band
-- processing.
CREATE TABLE IF NOT EXISTS public.org_active_run (
    project_id    TEXT NOT NULL,
    run_id        TEXT NOT NULL,
    source_id     TEXT NOT NULL DEFAULT '',
    org_node_id   TEXT NOT NULL DEFAULT '',
    org_node_path TEXT NOT NULL DEFAULT '',
    actor_id      TEXT NOT NULL DEFAULT '',
    agent_id      TEXT NOT NULL DEFAULT '',
    task_id       TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'running', -- running|completed|failed|cancelled
    urgent        BOOL NOT NULL DEFAULT FALSE,
    started_at    TIMESTAMPTZ,
    last_event_at TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, run_id)
);

CREATE INDEX IF NOT EXISTS idx_org_active_run_node
    ON public.org_active_run (org_node_id, status);
CREATE INDEX IF NOT EXISTS idx_org_active_run_task
    ON public.org_active_run (task_id);

-- §6.2: supervisor batches — the audit record of what the supervisor was
-- given and when processing succeeded/failed (the per-node window only
-- advances past a completed batch).
CREATE TABLE IF NOT EXISTS public.org_activity_batch (
    id             TEXT PRIMARY KEY,
    org_node_id    TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending', -- pending|processing|completed|failed
    trigger        TEXT NOT NULL DEFAULT 'scheduled', -- scheduled|manual|urgent
    window_from    TIMESTAMPTZ,
    window_to      TIMESTAMPTZ,
    event_count    INT NOT NULL DEFAULT 0,
    decision_count INT NOT NULL DEFAULT 0,
    delivery_count INT NOT NULL DEFAULT 0,
    empty          BOOL NOT NULL DEFAULT FALSE,
    error          TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at     TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_org_activity_batch_node
    ON public.org_activity_batch (org_node_id, created_at DESC);

-- Per-node schedule state: the last completed batch's window end. A failed
-- batch never advances it — the next tick re-processes the same window
-- idempotently (decisions dedupe by key).
CREATE TABLE IF NOT EXISTS public.org_activity_batch_state (
    org_node_id   TEXT PRIMARY KEY,
    last_batch_id TEXT NOT NULL DEFAULT '',
    window_to     TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- §6.4: one row per supervisor decision. ignore rows are kept for the
-- decision distribution but never create deliveries.
CREATE TABLE IF NOT EXISTS public.org_awareness_decision (
    id                TEXT PRIMARY KEY,
    batch_id          TEXT NOT NULL,
    org_node_id       TEXT NOT NULL DEFAULT '',
    source_event_id   TEXT NOT NULL DEFAULT '',
    source_run_id     TEXT NOT NULL DEFAULT '',
    target_run_id     TEXT NOT NULL DEFAULT '',
    decision          TEXT NOT NULL, -- ignore|notify|recommend_lookup|urgent_steer|update_awareness
    relevance         TEXT NOT NULL DEFAULT 'low',
    reason            TEXT NOT NULL DEFAULT '',
    message           TEXT NOT NULL DEFAULT '',
    suggested_lookup  JSONB,
    urgency           TEXT NOT NULL DEFAULT 'normal',
    deduplication_key TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_org_awareness_decision_batch
    ON public.org_awareness_decision (batch_id);
CREATE INDEX IF NOT EXISTS idx_org_awareness_decision_node
    ON public.org_awareness_decision (org_node_id, created_at DESC);

-- The supervisor's repeat suppression at the decision level: a reprocessed
-- batch (failed pass, boundary overlap) can never produce a second decision
-- for the same fact+target. update_awareness carries a fresh revision key.
CREATE UNIQUE INDEX IF NOT EXISTS uq_org_awareness_decision_dedup
    ON public.org_awareness_decision (deduplication_key)
    WHERE deduplication_key <> '' AND decision <> 'ignore';

-- §8: the delivery lifecycle ledger. The (deduplication_key, target_run_id)
-- unique index is the replay/backoff guard: a retried batch or a redelivered
-- decision can never enqueue a second copy for the same run (spec §21
-- «Retry не создаёт повторных доставок»).
CREATE TABLE IF NOT EXISTS public.org_awareness_delivery (
    id                TEXT PRIMARY KEY,
    decision_id       TEXT NOT NULL DEFAULT '',
    batch_id          TEXT NOT NULL DEFAULT '',
    org_node_id       TEXT NOT NULL DEFAULT '',
    project_id        TEXT NOT NULL DEFAULT '',
    source_event_id   TEXT NOT NULL DEFAULT '',
    source_run_id     TEXT NOT NULL DEFAULT '',
    source_summary    TEXT NOT NULL DEFAULT '',
    target_run_id     TEXT NOT NULL,
    target_actor_id   TEXT NOT NULL DEFAULT '',
    decision          TEXT NOT NULL,
    relevance         TEXT NOT NULL DEFAULT 'low',
    urgency           TEXT NOT NULL DEFAULT 'normal',
    reason            TEXT NOT NULL DEFAULT '',
    message           TEXT NOT NULL DEFAULT '',
    detail_level      TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'selected', -- selected|queued|delivered|consumed|follow_up_requested|details_delivered|acted_upon|dismissed|expired|failed
    message_ref       TEXT NOT NULL DEFAULT '',
    attempts          INT NOT NULL DEFAULT 0,
    last_error        TEXT NOT NULL DEFAULT '',
    deduplication_key TEXT NOT NULL DEFAULT '',
    follow_up_refs    JSONB NOT NULL DEFAULT '[]',
    related_action_refs JSONB NOT NULL DEFAULT '[]',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    queued_at         TIMESTAMPTZ,
    delivered_at      TIMESTAMPTZ,
    consumed_at       TIMESTAMPTZ,
    closed_at         TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_org_awareness_delivery_target
    ON public.org_awareness_delivery (target_run_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_org_awareness_delivery_node_status
    ON public.org_awareness_delivery (org_node_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_org_awareness_delivery_source
    ON public.org_awareness_delivery (source_event_id);

CREATE UNIQUE INDEX IF NOT EXISTS uq_org_awareness_delivery_dedup
    ON public.org_awareness_delivery (deduplication_key, target_run_id)
    WHERE deduplication_key <> '';
