-- Rollback of observation_events_retention.up.sql: restores the unconditional
-- append-only trigger body from observation_events.up.sql. The trigger itself
-- (created by the baseline migration) stays in place; after this rollback no
-- DELETE ever passes, including the retention sweep.
CREATE OR REPLACE FUNCTION reject_observation_event_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'Temporality observation event log is append-only';
END;
$$ LANGUAGE plpgsql;
