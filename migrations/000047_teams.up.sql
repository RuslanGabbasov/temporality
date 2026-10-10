-- Agent teams (docs/agent-teams.md).
-- workspace_team holds the current revision of a team definition; every change
-- produces an immutable workspace_team_version row. The manifest carries the
-- protocol and role slots as data (docs/agent-teams.md §5): slots declare
-- requirements and contracts, agents are bound at launch time (§7).

CREATE TABLE IF NOT EXISTS workspace_team (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    version     TEXT NOT NULL DEFAULT '1.0.0',
    manifest    JSONB NOT NULL DEFAULT '{}',
    org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_team_version (
    team_id         TEXT NOT NULL REFERENCES workspace_team(id) ON DELETE CASCADE,
    version         TEXT NOT NULL,
    manifest        JSONB NOT NULL DEFAULT '{}',
    origin          TEXT NOT NULL DEFAULT 'initial',
    change_summary  TEXT NOT NULL DEFAULT '',
    observed_problem TEXT NOT NULL DEFAULT '',
    proposed_change  TEXT NOT NULL DEFAULT '',
    expected_effect  TEXT NOT NULL DEFAULT '',
    source_runs     TEXT[] NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'active',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, version)
);

CREATE INDEX IF NOT EXISTS idx_workspace_team_org ON workspace_team(org_unit_id);
