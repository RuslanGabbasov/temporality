-- Bound in-flight model requests per provider to avoid upstream 429 storms.
ALTER TABLE public.workspace_provider
  ADD COLUMN IF NOT EXISTS max_concurrent_requests integer NOT NULL DEFAULT 4;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'workspace_provider_max_concurrent_requests_check'
  ) THEN
    ALTER TABLE public.workspace_provider
      ADD CONSTRAINT workspace_provider_max_concurrent_requests_check
      CHECK (max_concurrent_requests > 0 AND max_concurrent_requests <= 128);
  END IF;
END $$;
