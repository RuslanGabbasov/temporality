-- M14: entity/knowledge-graph projection (down).
DROP TABLE IF EXISTS entity_relations;
DROP TABLE IF EXISTS entities;
DROP INDEX IF EXISTS claims_subject_idx;
ALTER TABLE claims DROP COLUMN IF EXISTS object;
ALTER TABLE claims DROP COLUMN IF EXISTS predicate;
ALTER TABLE claims DROP COLUMN IF EXISTS subject;
