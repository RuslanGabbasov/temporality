CREATE TABLE IF NOT EXISTS affordance_definitions (
    affordance_id TEXT PRIMARY KEY,
    protocol TEXT NOT NULL CHECK (protocol='frp'),
    version TEXT NOT NULL CHECK (version='0.3'),
    data JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS affordance_requests (
    request_id UUID PRIMARY KEY,
    episode_id UUID NOT NULL,
    affordance_id TEXT NOT NULL REFERENCES affordance_definitions(affordance_id),
    data JSONB NOT NULL,
    requested_event UUID NOT NULL UNIQUE REFERENCES events(event_id)
);

CREATE TABLE IF NOT EXISTS executions (
    execution_id UUID PRIMARY KEY,
    request_id UUID NOT NULL UNIQUE REFERENCES affordance_requests(request_id),
    episode_id UUID NOT NULL,
    affordance_id TEXT NOT NULL REFERENCES affordance_definitions(affordance_id),
    status TEXT NOT NULL CHECK (status IN ('created','running','completed','failed','cancelled')),
    data JSONB NOT NULL,
    created_event UUID NOT NULL UNIQUE REFERENCES events(event_id)
);

CREATE INDEX IF NOT EXISTS executions_active_idx ON executions(episode_id, execution_id)
    WHERE status IN ('created','running');

CREATE OR REPLACE FUNCTION reject_affordance_definition_mutation() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'FRP affordance definitions are frozen'; END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION reject_affordance_request_mutation() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'FRP affordance requests are immutable'; END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS affordance_definitions_frozen ON affordance_definitions;
CREATE TRIGGER affordance_definitions_frozen BEFORE UPDATE OR DELETE ON affordance_definitions
FOR EACH ROW EXECUTE FUNCTION reject_affordance_definition_mutation();
DROP TRIGGER IF EXISTS affordance_requests_immutable ON affordance_requests;
CREATE TRIGGER affordance_requests_immutable BEFORE UPDATE OR DELETE ON affordance_requests
FOR EACH ROW EXECUTE FUNCTION reject_affordance_request_mutation();
