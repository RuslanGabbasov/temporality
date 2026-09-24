DROP TRIGGER IF EXISTS observation_events_append_only ON observation_events;
DROP FUNCTION IF EXISTS reject_observation_event_mutation();
DROP TABLE IF EXISTS observation_events;
