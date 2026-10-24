-- Egress allowlist (docs/agent-behavior-monitoring.md §7): network
-- destinations an agent may reach beyond the public internet, enforced by the
-- egress proxy. Stored as a JSON array of domain/CIDR/host:port entries.
ALTER TABLE public.workspace_agent ADD COLUMN IF NOT EXISTS egress_allowlist jsonb NOT NULL DEFAULT '[]';
