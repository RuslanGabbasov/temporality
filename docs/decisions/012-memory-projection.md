# ADR-012: Memory is an authorized Temporality projection

## Context

Agent memory needs temporal validity, evidence, provenance, lifecycle and project authorization. A standalone vector store would lose these guarantees.

## Decision

Knowledge events are the source data. Build a scoped current/temporal projection and retrieve only after project/actor policy checks. Return state, evidence, provenance and caution with each hint. Embeddings are an optional index, never the canonical memory store.

## Alternatives

Prompt-only memory; vector DB source of truth; automatically inject all project knowledge.

## Consequences

Every retrieved hint can be inspected and attributed. Retrieval must enforce authorization before ranking; vector similarity cannot grant access.
