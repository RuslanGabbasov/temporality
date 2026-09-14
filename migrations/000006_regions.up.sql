CREATE TABLE IF NOT EXISTS regions (
 region_id UUID PRIMARY KEY, episode_id UUID NOT NULL, branch_id UUID NOT NULL, kind TEXT NOT NULL, label TEXT NOT NULL,
 created_ts TIMESTAMPTZ NOT NULL, retired_ts TIMESTAMPTZ NULL, projection_version TEXT NOT NULL,
 UNIQUE(episode_id,branch_id,kind,label,projection_version)
);
CREATE TABLE IF NOT EXISTS region_versions (
 region_id UUID NOT NULL REFERENCES regions(region_id) ON DELETE CASCADE, version INT NOT NULL, valid_from TIMESTAMPTZ NOT NULL,
 valid_to TIMESTAMPTZ NULL, label TEXT NOT NULL, activation REAL NOT NULL CHECK(activation BETWEEN 0 AND 1), projection_version TEXT NOT NULL,
 PRIMARY KEY(region_id,version)
);
CREATE TABLE IF NOT EXISTS region_memberships (
 region_id UUID NOT NULL, region_version INT NOT NULL, event_id UUID NOT NULL REFERENCES events(event_id),
 PRIMARY KEY(region_id,region_version,event_id), FOREIGN KEY(region_id,region_version) REFERENCES region_versions(region_id,version) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS regions_scope_idx ON regions(episode_id,branch_id);
