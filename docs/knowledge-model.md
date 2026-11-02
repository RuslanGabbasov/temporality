# Knowledge model

Phase 2 audit: knowledge proposals now come from two channels. The model may still call `remember` for high-confidence conclusions (`kind: claim`, with operation ID, frame, actor, evidence refs and causal parent in the event). Independently, the Kernel applies the deterministic heuristic `kernel-heuristic/execution-observation.v1`: a sandbox `run_command` classified as a verification or build command (test/vet/lint/check, build/tsc, and common tool+subcommand forms) that exits 0 produces an execution observation with evidence referencing the producing `operation_id`. To keep the stream clean, the observation identity is project-scoped and run-independent (`auto/` + hash of project, workspace and command), and the Kernel consults the authoritative projection before emitting: an unknown outcome is `knowledge.proposed` (`kind: observation`) exactly once, a repeated success of a proposed or challenged observation is `knowledge.confirmed` (`execution-reverification.v1`), and later successes are `knowledge.used` (`execution-reuse.v1`) so reuse telemetry grows instead of duplicate nodes. Terminal states (corrected, superseded, invalidated) and unavailable lookups emit nothing — the runtime `tool.completed` event already records the fact. The projection treats an identical re-proposal (same ID and proposition, as can happen when concurrent runs race past their lookups) as idempotent and still rejects the same ID with a different proposition. The heuristic records execution facts only — it never asserts conclusions from free text, failures stay runtime events, and there is no truth score. Beyond it, the lifecycle runs automatically on evidence: the aging sweep challenges stale items (aging.v1), extraction refutes what a run disproves (contradicts → knowledge.challenged/disproved), and judged hint outcomes move state directly (helpful confirms a proposal, a harmful verdict challenges with its reason recorded, two unsupported harmful verdicts invalidate the node).

## Small semantic core

Do not start with a domain ontology. A `KnowledgeNode` is a stable project-scoped identity plus proposition, kind, current lifecycle state, validity interval, and pointers to evidence and transitions. Supported kinds: `claim` and `observation` (claims are model-curated conclusions, observations are execution facts from the heuristic). Kind is descriptive and does not by itself imply truth or confidence.

```text
KnowledgeNode(id, project, proposition, kind, state, valid_from, valid_to)
  <- KnowledgeEvent(event_id, transition, actor, time, reason, caused_by[])
  <- Evidence(id, type, source, timestamp, hash, artifact_ref, metadata)
  <- Relationship(from_id, relation, to_id, event_id)
```

State is projected from immutable events. The transition vocabulary is `proposed -> confirmed|challenged -> corrected|superseded|invalidated` (plus `knowledge.disproved`, which lands as invalidated); terminal states never resurrect — a later re-proposal creates a fresh node. `knowledge.used` is an activity and does not change belief state, but judged hint outcomes do: `hint.outcome: helpful` confirms a proposal, `harmful` challenges it (one verdict) or invalidates it (two unsupported harmful verdicts). `forgotten` is a retrieval/retention policy event, not deletion from the source journal. Conflicting branches may both remain visible with challenged state; no scalar confidence is treated as truth.

## Provenance

Every knowledge transition names its actor, event time, execution context and causal source events. `derived_from`, `supports`, `contradicts`, `depends_on`, `supersedes`, `related_to` are explicit relationship events. Evidence stores stable ID, evidence type, source URI, observed time, content hash, optional metadata and artifact reference. Large data is an object-store artifact; an event contains its immutable reference and hash.

Creation has two automatic channels (the model calling `remember`, and the kernel's execution-observation heuristic) plus extraction from completed runs; all of them point at the observations/events that caused the item. `knowledge.used` records activation/reuse. Challenge and correction are append-only events with actor and reason. When a dependency is retired, downstream items are marked `at_risk`; they are not silently invalidated absent their own evidence.

## Actors, executions and ownership

Actor IDs are issuer-scoped opaque strings with types human, agent, system, service. A run is a durable attempt; a frame is a bounded semantic step within a run; a task is a project work item and may span runs. Parent run/frame links describe delegation and inherited context. A Project scopes query and authorization; sharing across projects is an explicit relationship/transfer event, never implicit by vector similarity.

## Retrieval

Retrieval is a projection consumer and must return provenance, state, temporal validity, project scope and authorization decision alongside text. The caller should request a bounded set and receive a separate context block. The lexical matcher is one tier: a semantic tier (per-knowledge embeddings, cosine admission with a reserved budget share) sits below it, and corpus-wide tokens that match everything are filtered out as non-discriminative (IDF-style). Staleness is part of retrieval too: items nothing re-verified for a long time surface with a caution and a demotion.

## SQL shape for the first PostgreSQL projection

The implemented MVP uses `observation_events` as the journal and rebuilds current knowledge from that journal. A later materialized projection can use:

```sql
CREATE TABLE knowledge_nodes (
  project_id text NOT NULL,
  knowledge_id text NOT NULL,
  proposition text NOT NULL,
  kind text NOT NULL,
  state text NOT NULL,
  valid_from timestamptz NOT NULL,
  valid_to timestamptz,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (project_id, knowledge_id)
);
CREATE TABLE knowledge_event_links (
  project_id text NOT NULL,
  knowledge_id text NOT NULL,
  source_id text NOT NULL,
  event_id text NOT NULL,
  transition text NOT NULL,
  actor_id text,
  occurred_at timestamptz NOT NULL,
  PRIMARY KEY (source_id, event_id)
);
```

This projection is disposable/rebuildable; the DDL is a target shape and is not yet a migration. Postgres JSONB and relational indexes cover the MVP. No Neo4j or standalone vector service is needed before measured query/retrieval pressure.
