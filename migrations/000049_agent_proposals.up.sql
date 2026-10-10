-- docs/plan-evaluable-agent.md §2.2 stage 10: agent evolution proposals.
-- Mirrors the living-skills proposal anatomy (observed problem / proposed change /
-- expected effect + evidence), pinned to the definition version it was based on.
-- Status is pending until a human decides: apply goes through the regular
-- update path (version++, snapshot), reject keeps the row for history.

CREATE TABLE IF NOT EXISTS workspace_agent_proposal (
    id              BIGSERIAL PRIMARY KEY,
    agent_id        TEXT NOT NULL REFERENCES workspace_agent(id) ON DELETE CASCADE,
    base_version    INT NOT NULL,
    definition      JSONB NOT NULL DEFAULT '{}',
    description     TEXT NOT NULL DEFAULT '',
    problem         TEXT NOT NULL,
    change          TEXT NOT NULL,
    effect          TEXT NOT NULL DEFAULT '',
    evidence        JSONB NOT NULL DEFAULT '[]',
    status          TEXT NOT NULL DEFAULT 'pending',
    author          TEXT NOT NULL DEFAULT '',
    generator_model TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at      TIMESTAMPTZ,
    decided_by      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_workspace_agent_proposal
    ON workspace_agent_proposal(agent_id, status, created_at DESC);
