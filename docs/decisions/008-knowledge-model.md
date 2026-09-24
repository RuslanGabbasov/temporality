# ADR-008: Event-sourced minimal knowledge model

## Context

The product must explain what a team believed, when, who introduced it, evidence, reuse, challenge and correction. A graph/vector index alone cannot do this.

## Decision

Represent knowledge nodes and evidence relationships as projections of immutable Temporality events. Keep identity, proposition, kind, lifecycle, actor, validity, causal references and evidence. Retrieval is scoped and provenance-bearing. Tool output does not automatically create knowledge.

## Alternatives

Mutable graph as source of truth; vector memory as canonical store; infer claims from all tool text automatically.

## Consequences

Temporal reconstruction and provenance remain possible. Knowledge assertion/curation is an explicit operation, and semantic retrieval needs independent evaluation.
