DROP TABLE IF EXISTS public.org_usage_sync;
DROP TABLE IF EXISTS public.org_month_usage;
ALTER TABLE public.org_policy
    DROP COLUMN IF EXISTS monthly_max_tokens,
    DROP COLUMN IF EXISTS monthly_max_budget_usd;
