-- Run execution context snapshot (docs/org-structure.md §34-35): why the
-- operation had the right to use its resources at start time. Stores ids
-- only — the answer must survive org-structure changes without resurrecting
-- whole resource copies.
ALTER TABLE workspace_run ADD COLUMN IF NOT EXISTS exec_context JSONB NOT NULL DEFAULT '{}';
