-- specs/quality-loop.md §5.3, §10.4 (M3 eval gate): a run started by the gate
-- is linked to the proposal it gates, so a repeated apply finds the run it
-- already started — (agent_id, proposal_id, trigger='gate') — and answers the
-- same 202 with the same evaluation_run_id instead of starting a second run.
-- The gate lookup index itself is deliberately not repeated here: migration
-- 000056 already creates idx_workspace_agent_evaluation_run_gate on
-- (agent_id, agent_version, verdict, created_at DESC).
ALTER TABLE workspace_agent_evaluation_run
    ADD COLUMN IF NOT EXISTS proposal_id BIGINT REFERENCES workspace_agent_proposal(id);
