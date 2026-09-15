CREATE TABLE IF NOT EXISTS worlds (
    world_id TEXT PRIMARY KEY,
    state_version INTEGER NOT NULL CHECK (state_version > 0),
    data JSONB NOT NULL,
    updated_event UUID NOT NULL REFERENCES events(event_id)
);

CREATE INDEX IF NOT EXISTS worlds_state_idx ON worlds(state_version);
