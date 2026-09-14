CREATE TABLE IF NOT EXISTS procedures (
    procedure_id UUID PRIMARY KEY,
    episode_id UUID NOT NULL,
    protocol TEXT NOT NULL CHECK (protocol='frp'),
    version TEXT NOT NULL CHECK (version='0.3'),
    semantic_trigger TEXT NOT NULL,
    successes INT NOT NULL CHECK (successes >= 0),
    failures INT NOT NULL CHECK (failures >= 0),
    success_rate DOUBLE PRECISION NOT NULL CHECK (success_rate BETWEEN 0 AND 1),
    projection_version TEXT NOT NULL,
    data JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS procedure_evidence_memberships (
    procedure_id UUID NOT NULL REFERENCES procedures(procedure_id) ON DELETE CASCADE,
    execution_id UUID NOT NULL REFERENCES executions(execution_id),
    PRIMARY KEY (procedure_id, execution_id)
);

CREATE INDEX IF NOT EXISTS procedures_episode_idx ON procedures(episode_id, procedure_id);
CREATE INDEX IF NOT EXISTS procedure_evidence_execution_idx ON procedure_evidence_memberships(execution_id);
