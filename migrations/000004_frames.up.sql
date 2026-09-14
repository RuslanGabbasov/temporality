CREATE TABLE IF NOT EXISTS frames (
    frame_id UUID PRIMARY KEY,
    protocol TEXT NOT NULL CHECK (protocol = 'frp'),
    version TEXT NOT NULL CHECK (version = '0.3'),
    parent_frame_id UUID NULL REFERENCES frames(frame_id),
    agent_id UUID NOT NULL,
    episode_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    objective_id UUID NOT NULL,
    as_of TIMESTAMPTZ NOT NULL,
    revision BIGINT NOT NULL CHECK (revision >= 0),
    reducer_version TEXT NOT NULL,
    data JSONB NOT NULL,
    created_event UUID NOT NULL UNIQUE REFERENCES events(event_id)
);

ALTER TABLE frames DROP CONSTRAINT IF EXISTS frames_parent_frame_id_revision_key;

CREATE INDEX IF NOT EXISTS frames_episode_branch_revision_idx ON frames(episode_id, branch_id, revision);
CREATE INDEX IF NOT EXISTS frames_parent_idx ON frames(parent_frame_id);

CREATE OR REPLACE FUNCTION reject_frame_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'FRP frames are immutable';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS frames_immutable ON frames;
CREATE TRIGGER frames_immutable
BEFORE UPDATE OR DELETE ON frames
FOR EACH ROW EXECUTE FUNCTION reject_frame_mutation();
