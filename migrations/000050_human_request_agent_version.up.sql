-- HITL verdicts as an eval signal (specs/quality-loop.md §3): every human
-- request remembers which agent definition version was live when the ask was
-- created, so each verdict attributes to the snapshot that produced the
-- question. Existing rows keep 0 = "version unknown": they do not block
-- anything and never participate in per-version metrics. Mechanism mirrors
-- migration 000031 (workspace_run.agent_version).
ALTER TABLE human_request ADD COLUMN IF NOT EXISTS agent_version INT NOT NULL DEFAULT 0;

-- Verdict lookups are per run (§8.1); the current schema has no index for a
-- run_id-only scan.
CREATE INDEX IF NOT EXISTS idx_human_request_run ON human_request (run_id);
