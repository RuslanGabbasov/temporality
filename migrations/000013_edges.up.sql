CREATE TABLE IF NOT EXISTS region_edges (
 edge_id UUID PRIMARY KEY,
 episode_id UUID NOT NULL,
 branch_id UUID NOT NULL,
 source_region_id UUID NOT NULL REFERENCES regions(region_id) ON DELETE CASCADE,
 target_region_id UUID NOT NULL REFERENCES regions(region_id) ON DELETE CASCADE,
 edge_type TEXT NOT NULL CHECK(edge_type IN ('shared_event','sequence_adjacency')),
 weight REAL NOT NULL CHECK(weight > 0 AND weight <= 1),
 evidence_count INT NOT NULL CHECK(evidence_count > 0),
 projection_version TEXT NOT NULL,
 CHECK(source_region_id < target_region_id),
 UNIQUE(episode_id,branch_id,source_region_id,target_region_id,edge_type,projection_version)
);
CREATE INDEX IF NOT EXISTS region_edges_scope_idx ON region_edges(episode_id,branch_id);
CREATE INDEX IF NOT EXISTS region_edges_source_idx ON region_edges(source_region_id);
CREATE INDEX IF NOT EXISTS region_edges_target_idx ON region_edges(target_region_id);
