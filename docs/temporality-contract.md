# Temporality event contract

Status: `temporality.event/1` is implemented for universal observation ingestion. This document defines its intended semantic use by the new Agent Kernel and records the next compatible evolution.

## Envelope and identity

The v1 wire envelope is:

```json
{
  "schema": "temporality.event/1",
  "event_id": "01J...",
  "occurred_at": "2026-09-24T10:00:00Z",
  "source": {"id": "kernel-worker-3", "integration": "temporality-kernel", "version": "0.1"},
  "context": {
    "project": "repo-a", "run": "run-123", "task": "fix-auth",
    "actor": {"id": "coder-1", "type": "agent"},
    "parent_event_id": "event-parent"
  },
  "type": "tool.completed",
  "data": {"operation_id": "op-77", "tool": "tests", "status": "failed"},
  "evidence": [{"ref": "artifact://sha256/...", "type": "test-result"}]
}
```

`event_id` is stable across retries. The storage idempotency key is `(source.id,event_id)`. `occurred_at` is producer time; server `received_at` is assigned by Temporality. Their distinction enables valid-time (`as_of`) and transaction-time (`known_at`) queries. External identifiers are opaque strings. `source.id` identifies a producer installation, not a user or tenant.

## Causality and frames

`parent_event_id` expresses one immediate parent in v1. For kernel emissions, events also carry `data.frame_id`, `data.parent_frame_id`, `data.sequence`, and `data.caused_by` (an array of event IDs) until the contract gets a first-class versioned causality object. IDs are run-scoped except `event_id`, which is source-scoped. No global ordering is promised; sequence is monotonic per run and `caused_by` defines edges across child runs.

The next wire-compatible envelope should be `temporality.event/2` with first-class `frame_id`, `parent_frame_id`, `sequence`, `causality.caused_by`, and `operation.type`; retain v1 decode and normalize both versions into the same internal event model. Do not silently reinterpret v1 fields. A V2 migration is not part of the initial Kernel vertical.

## Event families

Runtime events describe what the system did. Initial names: `agent.started/completed/failed`; `run.started/completed/failed/cancelled`; `turn.started/completed`; `model.started/completed/failed`; `tool.started/completed/failed`; `mcp.call.started/completed/failed`; `sandbox.created/destroyed`; `delegation.started/completed/failed`; `approval.requested/granted/rejected`; `context.created/compacted`; `memory.read/write`.

Knowledge events describe semantic state changes: `knowledge.proposed`, `knowledge.used`, `knowledge.confirmed`, `knowledge.challenged`, `knowledge.corrected`, `knowledge.superseded`, `knowledge.invalidated`, `knowledge.disproved`, and `knowledge.linked`. V1 maps `knowledge.proposed` to the current lifecycle projection. Events unknown to a consumer are retained. Unknown `knowledge.*` events require a versioned schema registry or are rejected until a projection handles them; silently ignoring semantic mutations would create false state.

`tool.completed` never implies `knowledge.created`. An explicit inference/curation action emits a separate knowledge event with evidence refs and causality pointing to model/tool events.

## Required fields by operation

All events: schema, stable event ID, occurred time, source ID/integration, event type. Kernel events additionally require project, run, actor, frame, sequence and operation ID where applicable. Start/finish events require status/outcome. Model events require provider/model, input/output artifact refs (or redacted content hashes), latency and token usage when available. Tool/MCP events require server/tool name, argument hash or artifact ref, result ref/status, policy decision and stable operation ID. Knowledge events require project, stable knowledge ID, actor, transition, reason for invalidation/challenge/correction, and evidence refs when a factual claim is changed.

## Privacy and artifact references

Do not put prompts, model output, tool arguments/results, credentials, or file contents in `data` by default. Store bounded metadata and references: `artifact_id`, URI, SHA-256, size, media type, created time, access classification. A URI is not an authorization token; authorization is checked when reading the artifact. Redaction occurs before artifact persistence. Event schema evolution must preserve unknown `data` fields and version the artifact format independently.

## Projection and replay

The event stream is immutable source of truth. Runtime trajectory, knowledge current state, provenance edges and UI summaries are rebuildable projections. `state(T)` filters by occurred time and received time where supplied. Replay is a semantic reconstruction from Temporality events; it is distinct from Temporal Workflow replay, which reconstructs deterministic orchestration state.

## Delivery

Kernel outbox publishes at least once. Duplicate delivery has no additional effect. Ordering is guaranteed per run/sequence by the producer; ingestion stores a partial batch independently, and consumers order causal data by sequence/caused_by rather than database insertion order. Missing parents are allowed during ingestion but flagged in projection until they arrive. An event may never be updated; correction is a new event.
