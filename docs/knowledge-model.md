# Knowledge model

## Small semantic core

Do not start with a domain ontology. A `KnowledgeNode` is a stable project-scoped identity plus proposition, kind, current lifecycle state, validity interval, and pointers to evidence and transitions. Supported initial kinds: fact, claim, hypothesis, decision, constraint, assumption, solution, observation, requirement, conclusion. Kind is descriptive and does not by itself imply truth or confidence.

```text
KnowledgeNode(id, project, proposition, kind, state, valid_from, valid_to)
  <- KnowledgeEvent(event_id, transition, actor, time, reason, caused_by[])
  <- Evidence(id, type, source, timestamp, hash, artifact_ref, metadata)
  <- Relationship(from_id, relation, to_id, event_id)
```

State is projected from immutable events. A minimal transition vocabulary is `proposed -> asserted -> confirmed|challenged -> corrected|superseded|invalidated|expired`; `used`/`reused` is an activity and does not change belief state. `forgotten` is a retrieval/retention policy event, not deletion from the source journal. Conflicting branches may both remain visible with challenged state; no scalar confidence is treated as truth.

## Provenance

Every knowledge transition names its actor, event time, execution context and causal source events. `derived_from`, `supports`, `contradicts`, `depends_on`, `supersedes`, `related_to` are explicit relationship events. Evidence stores stable ID, evidence type, source URI, observed time, content hash, optional metadata and artifact reference. Large data is an object-store artifact; an event contains its immutable reference and hash.

Creation is not inferred from tool outputs. An agent/human curation operation explicitly asserts knowledge and points to the observations that caused it. `knowledge.used` records activation/reuse. Challenge and correction are append-only events with actor and reason. When a dependency is retired, downstream items are marked `at_risk`; they are not silently invalidated absent their own evidence.

## Actors, executions and ownership

Actor IDs are issuer-scoped opaque strings with types human, agent, system, service. A run is a durable attempt; a frame is a bounded semantic step within a run; a task is a project work item and may span runs. Parent run/frame links describe delegation and inherited context. A Project scopes query and authorization; sharing across projects is an explicit relationship/transfer event, never implicit by vector similarity.

## Retrieval

Retrieval is a projection consumer and must return provenance, state, temporal validity, project scope and authorization decision alongside text. The caller should request a bounded set and receive a separate context block. The initial lexical matcher is a baseline only. Embeddings/pgvector may be added behind this API after evaluating recall, stale/contradicted knowledge suppression, authorization filtering and useful-context rate against a fixed dataset.

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
