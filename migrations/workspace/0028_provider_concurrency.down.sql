ALTER TABLE public.workspace_provider
  DROP CONSTRAINT IF EXISTS workspace_provider_max_concurrent_requests_check;
ALTER TABLE public.workspace_provider
  DROP COLUMN IF EXISTS max_concurrent_requests;
