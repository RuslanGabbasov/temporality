# ADR-009: At-least-once outbox delivery with idempotency

## Context

An external effect, Temporal activity completion, Kernel database, and Temporality ingestion cannot share one atomic transaction.

## Decision

Write a stable event intent and operation ID to a transactional Kernel outbox before consequential effects. Persist results and artifact refs after. Publish at least once and rely on `(source.id,event_id)` deduplication. Downstream effects need their own idempotency key or reconciliation; otherwise mark outcome uncertain instead of blindly retrying.

## Alternatives

Best-effort HTTP logging after the tool; claim exactly-once effect delivery; use Temporal history as the only outbox.

## Consequences

Temporary ingestion outages delay visibility but do not erase committed local intents. Adds outbox publisher and lag monitoring. External exactly-once effects are explicitly not guaranteed.
