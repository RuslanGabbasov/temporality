-- Canary honeypot secrets (docs/agent-behavior-monitoring.md): a canary
-- secret is handed to agents like any other, but its value exists only to be
-- caught — any use anywhere is a confirmed exfiltration signal.
ALTER TABLE public.workspace_secret ADD COLUMN IF NOT EXISTS canary boolean NOT NULL DEFAULT false;
