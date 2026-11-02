-- Memory quota dimensions (docs/org-structure.md §24): per-unit caps for the
-- memory gate. max_concurrent_runs caps how many runs of this unit may
-- execute at once; memory_budget_mb caps the SUM of their sandbox memory
-- requirements. 0/absent = no restriction — the pre-quota semantics for
-- every existing row. Merge is restrictive: the MINIMUM of the configured
-- values wins down the org tree.
ALTER TABLE org_policy ADD COLUMN IF NOT EXISTS max_concurrent_runs int NOT NULL DEFAULT 0;
ALTER TABLE org_policy ADD COLUMN IF NOT EXISTS memory_budget_mb int NOT NULL DEFAULT 0;
