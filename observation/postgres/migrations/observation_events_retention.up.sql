-- Retention escape hatch for the append-only journal. observation_events
-- grows forever (nothing deletes rows), so listings degrade linearly with the
-- table's age. The baseline trigger (observation_events.up.sql) rejects every
-- UPDATE and DELETE; this replacement keeps that guarantee for ordinary
-- clients but lets a DELETE through on exactly one condition: the session has
-- the custom GUC temporality.retention_sweep set to 'on'. Custom GUCs need no
-- superuser rights and current_setting(..., true) reads NULL when unset, so a
-- plain client session keeps hitting the exception. UPDATE stays banned in
-- every case. Store.RetentionSweep sets the GUC on the single pooled
-- connection it holds and RESETs it before the connection returns to the pool
-- (pgxpool hands connections back with their session state intact — a leaked
-- 'on' would poke a permanent hole in append-only). Idempotent CREATE OR
-- REPLACE, re-runs on every boot; must run after the baseline that creates
-- the trigger function.
CREATE OR REPLACE FUNCTION reject_observation_event_mutation() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('temporality.retention_sweep', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'Temporality observation event log is append-only';
END;
$$ LANGUAGE plpgsql;
