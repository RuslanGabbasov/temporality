-- Filesystem policy dimension (docs/filesystem-policy.md §3.3): glob rules
-- over workspace-relative paths per policy row. NULL = no filesystem
-- restriction — the pre-filesystem semantics for every existing row (§3.5).
-- Shape: {"read": [...], "write": [...], "deny": [...]}; empty dimensions
-- normalize to absent at the store layer.
ALTER TABLE org_policy ADD COLUMN IF NOT EXISTS filesystem jsonb;
