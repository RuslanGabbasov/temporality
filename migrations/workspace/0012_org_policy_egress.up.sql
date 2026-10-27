-- Centralized egress baseline (docs/agent-behavior-monitoring.md §7, warm
-- centralization): org_policy gains an egress allowlist dimension. Semantics
-- at merge: UNION with the agent's own list — the policy is the baseline every
-- agent gets for free, the agent adds its specific exceptions on top. (The
-- intersect semantics of models/MCP does not fit here: a centrally blessed
-- domain must not disappear because one agent's private list omits it.)
ALTER TABLE org_policy ADD COLUMN IF NOT EXISTS egress_allowlist jsonb NOT NULL DEFAULT '[]'::jsonb;
