DROP TABLE IF EXISTS snapshots;
DROP INDEX IF EXISTS events_episode_branch_seq_idx;
DROP INDEX IF EXISTS events_cursor_identity_key;
DROP INDEX IF EXISTS events_event_seq_key;
ALTER TABLE events DROP COLUMN IF EXISTS event_seq;
