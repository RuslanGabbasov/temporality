-- Triggers created through the UI used to store their config double-encoded:
-- the form sent JSON.stringify(config) as the config field, so the JSONB
-- column ended up holding a JSON *string* node with the document inside
-- ("\"{\\\"path\\\":\\\"pr-created\\\"}\""). Every consumer that unmarshals the
-- config into a struct broke: webhook path matching (deliveries answered
-- 404), per-trigger delivery tokens (422 "trigger config is not valid
-- webhook config") and schedule firing (cron never parsed). Unwrap
-- string-typed configs in place; proper object configs are untouched.
UPDATE workspace_trigger
SET config = (config #>> '{}')::jsonb
WHERE jsonb_typeof(config) = 'string';
