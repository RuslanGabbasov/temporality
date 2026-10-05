-- Org policies (docs/org-structure.md §24): inheritable execution
-- constraints attached to org units. A row with NULL org_unit_id is the
-- installation-wide policy; a row bound to a unit constrains the unit's
-- entire subtree. Empty allowed-lists mean "no restriction"; effective
-- values merge restrictively along the ancestor chain.
CREATE TABLE IF NOT EXISTS org_policy (
    id TEXT PRIMARY KEY,
    org_unit_id TEXT REFERENCES org_unit(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    allowed_models JSONB NOT NULL DEFAULT '[]',
    allowed_mcp JSONB NOT NULL DEFAULT '[]',
    max_tokens INTEGER CHECK (max_tokens IS NULL OR max_tokens > 0),
    max_budget_usd NUMERIC(12,4) CHECK (max_budget_usd IS NULL OR max_budget_usd > 0),
    timeout_seconds INTEGER CHECK (timeout_seconds IS NULL OR timeout_seconds > 0),
    network_mode TEXT CHECK (network_mode IS NULL OR network_mode IN ('', 'allow', 'deny')),
    sandbox_mode TEXT CHECK (sandbox_mode IS NULL OR sandbox_mode IN ('', 'standard', 'read_only')),
    approval_mode TEXT CHECK (approval_mode IS NULL OR approval_mode IN ('', 'auto', 'tools')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS org_policy_unit_idx ON org_policy(org_unit_id);
