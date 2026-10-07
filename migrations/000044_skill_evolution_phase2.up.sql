-- docs/living-skills.md Phase 2: evolution proposals become first-class.
-- A draft version row now carries the full proposal anatomy (§21): the
-- observed problem, the proposed change and the expected effect, alongside
-- the existing evidence/knowledge provenance. Evaluation suites and runs
-- back skill.evaluate (§24, §29): a suite is a list of cases stored per
-- skill; a run records per-case outcomes for the tested version.

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS observed_problem TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS proposed_change TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS expected_effect TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS workspace_skill_evaluation_suite (
    skill_id   TEXT PRIMARY KEY REFERENCES workspace_skill(id) ON DELETE CASCADE,
    cases      JSONB NOT NULL DEFAULT '[]',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_skill_evaluation_run (
    id            BIGSERIAL PRIMARY KEY,
    skill_id      TEXT NOT NULL REFERENCES workspace_skill(id) ON DELETE CASCADE,
    skill_version TEXT NOT NULL,
    passed        INT NOT NULL DEFAULT 0,
    failed        INT NOT NULL DEFAULT 0,
    cases         JSONB NOT NULL DEFAULT '[]',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_skill_evaluation_run ON workspace_skill_evaluation_run(skill_id, created_at DESC);
