-- Rollback of observation_events_indexes.up.sql (perf audit, phase 3.7).
DROP INDEX IF EXISTS observation_events_event_id_idx;
DROP INDEX IF EXISTS observation_events_run_occurred_idx;
DROP INDEX IF EXISTS observation_events_cursor_idx;
