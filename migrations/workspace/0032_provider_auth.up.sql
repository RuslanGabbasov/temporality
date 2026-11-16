-- Anthropic subscription providers (docs/subscription-providers-claude-codex.md
-- §3.1): a Claude Code setup-token (sk-ant-oat01-…) is issued for the native
-- /v1/messages surface, not the OpenAI-compat layer, so the provider must
-- carry its credential kind. Empty = an API key on the OpenAI-compatible
-- wire (every existing provider); 'anthropic_oauth' = the Messages wire.
-- Idempotent: the set re-runs on every boot (workspace/store.go MigrateAll).
ALTER TABLE workspace_provider ADD COLUMN IF NOT EXISTS auth text NOT NULL DEFAULT '';
