-- quality-loop.md §4 (M2): golden eval-case candidates harvested from
-- production runs. One candidate per run (unique run_id) so re-processing a
-- run never duplicates rows (§10.1г); a candidate becomes a suite case only
-- after a human review decision (§4.4). project_id is stored (not part of the
-- §4.1 column list but required by the §4.5 anti-contamination rule: at most
-- 70 % of an agent's accepted goldens may come from one project).
-- PII masking (§4.3) is enforced by the write path: the only INSERT goes
-- through redactCandidateText (kernel's redactApprovalText primitive, copied
-- into the workspace layer); redaction_applied/redactions record what it did.
-- Rejected rows are reaped after 90 days by the harvesting job (§10.2).

CREATE TABLE IF NOT EXISTS workspace_eval_case_candidate (
    id               BIGSERIAL PRIMARY KEY,
    agent_id         TEXT NOT NULL REFERENCES workspace_agent(id) ON DELETE CASCADE,
    run_id           TEXT NOT NULL,
    task_id          TEXT NOT NULL DEFAULT '',
    project_id       TEXT NOT NULL DEFAULT '',
    agent_version    INT NOT NULL DEFAULT 0,
    input            TEXT NOT NULL DEFAULT '',
    expected_output  TEXT NOT NULL DEFAULT '',
    source           TEXT NOT NULL DEFAULT 'succeeded_run'
                     CONSTRAINT eval_case_candidate_source CHECK (source IN ('succeeded_run', 'human_corrected')),
    verdict          TEXT NOT NULL DEFAULT 'pending'
                     CONSTRAINT eval_case_candidate_verdict CHECK (verdict IN ('pending', 'accepted', 'rejected')),
    redaction_applied BOOLEAN NOT NULL DEFAULT FALSE,
    redactions       INT NOT NULL DEFAULT 0,
    reviewed_by      TEXT NOT NULL DEFAULT '',
    reviewed_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Retry-safe harvesting (§10.1г): a duplicate delivery of the same run
-- updates nothing (INSERT ... ON CONFLICT (run_id) DO NOTHING).
CREATE UNIQUE INDEX IF NOT EXISTS idx_eval_case_candidate_run
    ON workspace_eval_case_candidate (run_id);

-- Review queue projection (§4.1).
CREATE INDEX IF NOT EXISTS idx_eval_case_candidate_review
    ON workspace_eval_case_candidate (agent_id, verdict, created_at DESC);
