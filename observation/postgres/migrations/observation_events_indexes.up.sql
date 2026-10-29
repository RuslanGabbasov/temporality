-- Additive hot-path indexes for the observation journal (perf audit, phase 3.7).
-- The journal runner (postgres.Store.Migrate) applies this file with a plain
-- multi-statement Exec, which PostgreSQL wraps in an implicit transaction —
-- so these are ordinary CREATE INDEX statements; CREATE INDEX CONCURRENTLY
-- cannot run inside a transaction block. Everything is IF NOT EXISTS and the
-- set re-runs on every boot.

-- Point lookups of an event by its id alone (the getObservationEvent path
-- without a source_id prefix).
CREATE INDEX IF NOT EXISTS observation_events_event_id_idx
    ON observation_events (event_id);

-- Run timelines slice by run first, then walk occurrences in time order.
CREATE INDEX IF NOT EXISTS observation_events_run_occurred_idx
    ON observation_events (run_id, occurred_at);

-- The listing cursor tuple (occurred_at, received_at, source_id, event_id):
-- global (unprojected) walks and cursor comparisons stop hitting the table
-- for ordering.
CREATE INDEX IF NOT EXISTS observation_events_cursor_idx
    ON observation_events (occurred_at, received_at, source_id, event_id);

-- Knowledge projection scans (the knowledge list, the map and hint retrieval)
-- filter by project: without this index every projection recompute seq-scans
-- the whole event table, which on installations with a long agent history
-- turns the first knowledge/map load after a journal restart into minutes.
CREATE INDEX IF NOT EXISTS observation_events_project_occurred_idx
    ON observation_events (project_id, occurred_at, received_at, source_id, event_id);
