CREATE TABLE IF NOT EXISTS observation_events (
    source_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    schema TEXT NOT NULL CHECK (schema = 'temporality.event/1'),
    occurred_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    integration TEXT NOT NULL,
    source_version TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    actor_id TEXT NOT NULL DEFAULT '',
    actor_type TEXT NOT NULL DEFAULT '',
    parent_event_id TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL,
    data JSONB NOT NULL DEFAULT '{}'::jsonb,
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
    PRIMARY KEY (source_id, event_id)
);

CREATE INDEX IF NOT EXISTS observation_events_project_time_idx
    ON observation_events (project_id, occurred_at, received_at, source_id, event_id);
CREATE INDEX IF NOT EXISTS observation_events_run_time_idx
    ON observation_events (project_id, run_id, occurred_at, received_at, source_id, event_id);
CREATE INDEX IF NOT EXISTS observation_events_type_time_idx
    ON observation_events (type, occurred_at);

CREATE OR REPLACE FUNCTION reject_observation_event_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'Temporality observation event log is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS observation_events_append_only ON observation_events;
CREATE TRIGGER observation_events_append_only
BEFORE UPDATE OR DELETE ON observation_events
FOR EACH ROW EXECUTE FUNCTION reject_observation_event_mutation();
