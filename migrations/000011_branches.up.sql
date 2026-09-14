CREATE TABLE IF NOT EXISTS fork_groups (
    fork_group_id UUID PRIMARY KEY,
    episode_id UUID NOT NULL,
    objective_id UUID NOT NULL,
    source_frame_id UUID NOT NULL REFERENCES frames(frame_id),
    source_branch_id UUID NOT NULL,
    source_event_seq BIGINT NOT NULL,
    source_event_id UUID NOT NULL,
    source_tx_time TIMESTAMPTZ NOT NULL,
    data JSONB NOT NULL,
    FOREIGN KEY (source_event_seq, source_event_id) REFERENCES events(event_seq, event_id)
);

CREATE TABLE IF NOT EXISTS branches (
    branch_id UUID PRIMARY KEY,
    fork_group_id UUID NOT NULL REFERENCES fork_groups(fork_group_id),
    parent_branch_id UUID NOT NULL,
    episode_id UUID NOT NULL,
    objective_id UUID NOT NULL,
    source_frame_id UUID NOT NULL REFERENCES frames(frame_id),
    source_event_seq BIGINT NOT NULL,
    source_event_id UUID NOT NULL,
    source_tx_time TIMESTAMPTZ NOT NULL,
    root_frame_id UUID NOT NULL REFERENCES frames(frame_id),
    head_frame_id UUID NOT NULL REFERENCES frames(frame_id),
    status TEXT NOT NULL CHECK (status IN ('active','completed','failed','abandoned')),
    model_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    data JSONB NOT NULL,
    FOREIGN KEY (source_event_seq, source_event_id) REFERENCES events(event_seq, event_id)
);
CREATE INDEX IF NOT EXISTS branches_group_idx ON branches(fork_group_id);

CREATE TABLE IF NOT EXISTS branch_comparisons (
    comparison_id UUID PRIMARY KEY,
    fork_group_id UUID NOT NULL REFERENCES fork_groups(fork_group_id),
    left_branch_id UUID NOT NULL REFERENCES branches(branch_id),
    right_branch_id UUID NOT NULL REFERENCES branches(branch_id),
    left_frame_id UUID NOT NULL REFERENCES frames(frame_id),
    right_frame_id UUID NOT NULL REFERENCES frames(frame_id),
    created_event UUID NOT NULL UNIQUE REFERENCES events(event_id),
    data JSONB NOT NULL,
    CHECK (left_branch_id <> right_branch_id)
);

CREATE OR REPLACE FUNCTION reject_branch_durable_mutation() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'FRP branch history is immutable'; END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS fork_groups_immutable ON fork_groups;
CREATE TRIGGER fork_groups_immutable BEFORE UPDATE OR DELETE ON fork_groups FOR EACH ROW EXECUTE FUNCTION reject_branch_durable_mutation();
DROP TRIGGER IF EXISTS branch_comparisons_immutable ON branch_comparisons;
CREATE TRIGGER branch_comparisons_immutable BEFORE UPDATE OR DELETE ON branch_comparisons FOR EACH ROW EXECUTE FUNCTION reject_branch_durable_mutation();
