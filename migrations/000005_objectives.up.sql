CREATE TABLE IF NOT EXISTS objectives (
    objective_id UUID PRIMARY KEY,
    protocol TEXT NOT NULL CHECK (protocol='frp'),
    version TEXT NOT NULL CHECK (version='0.3'),
    episode_id UUID NOT NULL,
    data JSONB NOT NULL,
    created_event UUID NOT NULL UNIQUE REFERENCES events(event_id)
);
CREATE INDEX IF NOT EXISTS objectives_episode_idx ON objectives(episode_id);
CREATE OR REPLACE FUNCTION reject_objective_mutation() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'FRP objectives are immutable'; END; $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS objectives_immutable ON objectives;
CREATE TRIGGER objectives_immutable BEFORE UPDATE OR DELETE ON objectives FOR EACH ROW EXECUTE FUNCTION reject_objective_mutation();
