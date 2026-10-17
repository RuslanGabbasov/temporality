-- Custom sandbox profile build recipes (docs/sandbox-profiles.md §5.1): a
-- custom profile either references an external digest-pinned image directly
-- or composes on top of a known base recipe (base + extra packages); the
-- built digest lands in image either way.
ALTER TABLE workspace_sandbox_profile
    ADD COLUMN IF NOT EXISTS base TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS packages TEXT[] NOT NULL DEFAULT '{}';
