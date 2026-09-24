# Architecture Decision Records

Initial decisions for the Agent Kernel. Each record is intentionally revisable after the PoC.

| ADR | Decision |
|---|---|
| [001](001-temporal-execution.md) | Temporal as durable execution substrate |
| [002](002-separate-temporality-stream.md) | Separate semantic event stream |
| [003](003-postgres-first.md) | PostgreSQL first for outbox and projections |
| [004](004-artifacts.md) | Artifact interface; S3 API outside event payloads |
| [005](005-model-gateway.md) | Provider-neutral model interface; optional LiteLLM |
| [006](006-mcp.md) | Official MCP Go SDK behind tool policy |
| [007](007-sandbox.md) | Replaceable sandbox interface; no host shell for untrusted code |
| [008](008-knowledge-model.md) | Event-sourced minimal knowledge model |
| [009](009-event-delivery.md) | At-least-once outbox delivery and idempotency |
| [010](010-language.md) | Go Agent Kernel |
| [011](011-fast-path.md) | One Workflow per managed run, not per turn |
| [012](012-memory-projection.md) | Memory is an authorized Temporality projection |
