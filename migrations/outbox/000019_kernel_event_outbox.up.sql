CREATE TABLE IF NOT EXISTS kernel_event_outbox (
    source_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    delivered_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    claimed_until TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (source_id, event_id)
);

CREATE INDEX IF NOT EXISTS kernel_event_outbox_pending_idx
    ON kernel_event_outbox (created_at, source_id, event_id)
    WHERE delivered_at IS NULL;
