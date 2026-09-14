ALTER TABLE events
    ADD COLUMN IF NOT EXISTS event_seq BIGINT GENERATED ALWAYS AS IDENTITY;

CREATE UNIQUE INDEX IF NOT EXISTS events_event_seq_key ON events(event_seq);
CREATE UNIQUE INDEX IF NOT EXISTS events_cursor_identity_key ON events(event_seq, event_id);
CREATE INDEX IF NOT EXISTS events_episode_branch_seq_idx
    ON events(episode_id, branch_id, event_seq);

CREATE TABLE IF NOT EXISTS snapshots (
    snapshot_id UUID PRIMARY KEY,
    protocol TEXT NOT NULL CHECK (protocol = 'frp'),
    protocol_version TEXT NOT NULL CHECK (protocol_version = '0.3'),
    snapshot_version TEXT NOT NULL CHECK (snapshot_version = '1'),
    episode_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    through_event_seq BIGINT NOT NULL,
    through_event_id UUID NOT NULL,
    through_tx_time TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^sha256:[0-9a-f]{64}$'),
    FOREIGN KEY (through_event_seq, through_event_id) REFERENCES events(event_seq, event_id),
    UNIQUE (episode_id, through_event_seq, snapshot_id)
);

CREATE INDEX IF NOT EXISTS snapshots_selection_idx
    ON snapshots(episode_id, through_event_seq DESC, created_at DESC, snapshot_id);
