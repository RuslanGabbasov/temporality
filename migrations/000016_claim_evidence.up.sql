-- M13: claims may cite pre-existing observation events as evidence so that
-- imported knowledge keeps its provenance chain (observation -> extraction -> claim).
CREATE TABLE IF NOT EXISTS claim_evidence (
    claim_id UUID NOT NULL REFERENCES claims(claim_id) ON DELETE CASCADE,
    evidence_event UUID NOT NULL REFERENCES events(event_id),
    PRIMARY KEY (claim_id, evidence_event)
);

CREATE INDEX IF NOT EXISTS claim_evidence_event_idx ON claim_evidence(evidence_event);
