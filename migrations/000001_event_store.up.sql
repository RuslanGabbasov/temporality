CREATE TABLE IF NOT EXISTS events (
    event_id UUID PRIMARY KEY,
    protocol TEXT NOT NULL CHECK (protocol = 'frp'),
    version TEXT NOT NULL CHECK (version = '0.3'),
    tx_time TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    valid_time TIMESTAMPTZ NOT NULL,
    agent_id UUID,
    episode_id UUID,
    branch_id UUID,
    parent_id UUID REFERENCES events(event_id),
    type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    provenance JSONB NOT NULL DEFAULT '{}'::jsonb,
    source_trust REAL CHECK (source_trust BETWEEN 0 AND 1),
    evidence_strength REAL CHECK (evidence_strength BETWEEN 0 AND 1),
    freshness REAL CHECK (freshness BETWEEN 0 AND 1),
    agent_trust REAL CHECK (agent_trust BETWEEN 0 AND 1),
    consensus REAL CHECK (consensus BETWEEN 0 AND 1),
    contradiction REAL CHECK (contradiction BETWEEN 0 AND 1)
);

CREATE INDEX IF NOT EXISTS events_replay_idx
    ON events (episode_id, branch_id, valid_time, tx_time, event_id);
CREATE INDEX IF NOT EXISTS events_type_idx ON events (type);

CREATE OR REPLACE FUNCTION reject_event_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'FRP event log is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS events_append_only ON events;
CREATE TRIGGER events_append_only
BEFORE UPDATE OR DELETE ON events
FOR EACH ROW EXECUTE FUNCTION reject_event_mutation();
