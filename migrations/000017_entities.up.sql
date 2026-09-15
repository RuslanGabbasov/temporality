-- M14: entity/knowledge-graph projection. Claims may carry an optional
-- subject/predicate/object triple binding them to entities; entities and typed
-- relations are a rebuildable global projection over those claims.
ALTER TABLE claims ADD COLUMN IF NOT EXISTS subject TEXT;
ALTER TABLE claims ADD COLUMN IF NOT EXISTS predicate TEXT;
ALTER TABLE claims ADD COLUMN IF NOT EXISTS object TEXT;

CREATE INDEX IF NOT EXISTS claims_subject_idx ON claims(subject) WHERE subject IS NOT NULL;

CREATE TABLE IF NOT EXISTS entities (
    entity_id UUID PRIMARY KEY,
    type TEXT NOT NULL,
    name TEXT NOT NULL,
    mention_count INT NOT NULL DEFAULT 0,
    confidence REAL NOT NULL DEFAULT 0,
    projection_version TEXT NOT NULL,
    UNIQUE (type, name)
);

CREATE INDEX IF NOT EXISTS entities_type_idx ON entities(type);

CREATE TABLE IF NOT EXISTS entity_relations (
    source_id UUID NOT NULL REFERENCES entities(entity_id) ON DELETE CASCADE,
    target_id UUID NOT NULL REFERENCES entities(entity_id) ON DELETE CASCADE,
    predicate TEXT NOT NULL,
    claim_id UUID NOT NULL REFERENCES claims(claim_id) ON DELETE CASCADE,
    confidence REAL NOT NULL DEFAULT 0,
    projection_version TEXT NOT NULL,
    PRIMARY KEY (source_id, target_id, predicate, claim_id)
);

CREATE INDEX IF NOT EXISTS entity_relations_source_idx ON entity_relations(source_id);
CREATE INDEX IF NOT EXISTS entity_relations_target_idx ON entity_relations(target_id);
