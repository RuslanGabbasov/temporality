CREATE TABLE IF NOT EXISTS claims (
    claim_id UUID PRIMARY KEY,
    protocol TEXT NOT NULL CHECK (protocol = 'frp'),
    version TEXT NOT NULL CHECK (version = '0.3'),
    proposition TEXT NOT NULL CHECK (length(trim(proposition)) > 0),
    confidence REAL NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    status TEXT NOT NULL CHECK (status IN ('candidate','supported','refuted','superseded')),
    created_event UUID NOT NULL REFERENCES events(event_id),
    valid_from TIMESTAMPTZ NOT NULL,
    valid_to TIMESTAMPTZ NULL CHECK (valid_to IS NULL OR valid_to >= valid_from)
);

CREATE TABLE IF NOT EXISTS claim_relations (
    src_claim UUID NOT NULL REFERENCES claims(claim_id),
    dst_claim UUID NOT NULL REFERENCES claims(claim_id),
    type TEXT NOT NULL CHECK (type IN ('supports','contradicts','derived_from','supersedes')),
    weight REAL NOT NULL CHECK (weight BETWEEN 0 AND 1),
    evidence_event UUID NOT NULL REFERENCES events(event_id),
    PRIMARY KEY (src_claim, dst_claim, type, evidence_event),
    CHECK (src_claim <> dst_claim)
);

CREATE INDEX IF NOT EXISTS claims_created_event_idx ON claims(created_event);
CREATE INDEX IF NOT EXISTS claims_status_idx ON claims(status);
CREATE INDEX IF NOT EXISTS claim_relations_dst_idx ON claim_relations(dst_claim);
