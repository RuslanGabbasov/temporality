-- Monthly usage accounting per org unit (docs/org-structure.md §24, policy
-- monthly limits) + the policy columns themselves. The usage row accumulates
-- from model.completed events attributed to the run's org position: tokens,
-- provider cost and the run count — one row per org node per calendar month.
ALTER TABLE public.org_policy
    ADD COLUMN IF NOT EXISTS monthly_max_tokens BIGINT,
    ADD COLUMN IF NOT EXISTS monthly_max_budget_usd DOUBLE PRECISION;

CREATE TABLE IF NOT EXISTS public.org_month_usage (
    org_node_id TEXT NOT NULL,
    month       TEXT NOT NULL,      -- 'YYYY-MM', calendar month in UTC
    runs        BIGINT NOT NULL DEFAULT 0,
    tokens      BIGINT NOT NULL DEFAULT 0,
    cost_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_node_id, month)
);

-- The usage fold watermark: model.completed events are read incrementally,
-- exactly like the org activity projection (org_activity_sync).
CREATE TABLE IF NOT EXISTS public.org_usage_sync (
    id        BOOLEAN PRIMARY KEY DEFAULT TRUE,
    watermark TIMESTAMPTZ NOT NULL
);
