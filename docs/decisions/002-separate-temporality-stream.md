# ADR-002: Keep Temporality semantic events separate from Temporal history

## Context

Temporal history records orchestration decisions needed for deterministic recovery. Temporality answers project knowledge, provenance, evidence, actor and valid/transaction-time questions across runs and harnesses.

## Decision

Keep `observation_events` and the portable HTTP/JSON event contract as the semantic source of truth. Kernel emits through an outbox. IDs are opaque strings. Temporal Workflow IDs are correlation metadata, not knowledge IDs.

## Alternatives

Use Temporal history as the only event store; couple event ingestion to FRP UUID/frame IDs; emit only OpenTelemetry spans.

## Consequences

There are two histories with explicit correlation IDs and retention. This duplicates a small amount of operational metadata but preserves cross-harness semantic queries and protects Temporal from becoming a knowledge database.
