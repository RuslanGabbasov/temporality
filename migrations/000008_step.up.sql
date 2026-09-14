CREATE TABLE IF NOT EXISTS cognitive_steps (
    emission_id TEXT PRIMARY KEY,
    parent_frame_id UUID NOT NULL REFERENCES frames(frame_id),
    next_frame_id UUID NOT NULL UNIQUE,
    emission JSONB NOT NULL,
    emission_hash TEXT NOT NULL CHECK (emission_hash ~ '^[0-9a-f]{64}$'),
    committed_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS cognitive_steps_parent_idx ON cognitive_steps(parent_frame_id);

CREATE OR REPLACE FUNCTION reject_cognitive_step_mutation() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'FRP cognitive steps are immutable'; END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS cognitive_steps_immutable ON cognitive_steps;
CREATE TRIGGER cognitive_steps_immutable BEFORE UPDATE OR DELETE ON cognitive_steps
FOR EACH ROW EXECUTE FUNCTION reject_cognitive_step_mutation();
