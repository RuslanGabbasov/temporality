# ADR-001: Temporal as durable execution substrate

## Context

Agent runs can include provider calls, tool effects, long waits, approvals and delegated agents. Process-local loops cannot recover reliably after worker restarts. The project explicitly requires durable execution and distinguishes it from semantic knowledge history.

## Decision

Use Temporal for managed `AgentRun` workflows, retryable Activities, timers, cancellation, Signals/Updates and child workflows. Keep the Agent Kernel workflow code small and deterministic.

## Alternatives

Restate, DBOS, Hatchet, Inngest, Trigger.dev, in-process loop only. DBOS is the closest lower-ops Go/PostgreSQL fallback; a comparison is in `docs/oss-research.md`.

## Consequences

Adds a Temporal service and operational knowledge. Activity effects must be idempotent/reconcilable. The workflow history is execution recovery state, not the Temporality journal. Verify latency and local setup in the PoC before expanding.
