# Temporality delivery roadmap

The milestone order follows FRP v0.3 section 38.

- [x] **M0 foundation:** Event protocol, append-only log, deterministic replay, PostgreSQL and memory adapters, HTTP API.
- [x] **M0 hardening:**
  - [x] PostgreSQL integration test for persistence, duplicate rejection, and append-only enforcement.
  - [x] Versioned replay manifests included in replay digests.
  - [x] Opaque cursor pagination for memory and PostgreSQL event streams.
  - [x] Prometheus-compatible HTTP, append, and replay metrics.
  - [x] Full PostgreSQL crash-recovery killer test with exact RenderPacket bytes/hash, model provenance, parent replay and committed child Frame.
- [x] **M1 — Claims and evidence relations:**
  - [x] Versioned Claim and ClaimRelation protocol objects and JSON Schemas.
  - [x] PostgreSQL schema with referential and confidence constraints.
  - [x] Atomic Event + Claim + relations commit in PostgreSQL and memory adapters.
  - [x] Claim create/read HTTP API and transactional rollback integration test.
  - [x] Claim lifecycle transitions (`supported`, `refuted`, `superseded`) and temporal validity invariants.
- [x] **M2 — Immutable Frame and deterministic reducer:**
  - [x] Versioned Frame value object and JSON Schema.
  - [x] Deterministic, side-effect-free reducer with content-derived frame IDs.
  - [x] Deep immutability and transition graph tests.
  - [x] Atomic Frame + `frame.transitioned` Event persistence with PostgreSQL restart/replay coverage.
  - [x] CognitiveEmission validation and deterministic reduction into Frame operations.
- [x] **M3 — RenderPacket and Objective:**
  - [x] Immutable Objective persistence with canonical `episode.started` Event.
  - [x] Deterministic RenderPacket and content-derived render IDs.
  - [x] Canonical sections, provenance, memory version, and token budget trimming.
  - [x] `/v1/render` integrated into the runnable smoke workflow.
- [x] **M4 — Deliberate and ambient Attention:**
  - [x] Versioned deterministic ambient scoring over normalized features.
  - [x] Stable tie-breaking, trust filters, explicit pins, and ambient-only hysteresis.
  - [x] Deliberate semantic jumps remain independent from ambient hysteresis.
  - [x] RenderPacket scored map, periphery, outside-frame count, and attention provenance.
  - [x] Rebuildable versioned Region projection and region-based ambient candidates.
  - [x] Rebuildable typed graph edges and graph-proximity scoring.
  - [x] Attention entropy, focus switches, ambient hit rate, missed candidates, and collapse metrics.
  - [x] Canonical `attention.selected` Events in the atomic Step transaction.
  - [x] Canonical `attention.suggested` Events when a RenderPacket is consumed by Model Step.
- [x] **M5 — Affordance registry and deterministic Executor:**
  - [x] Versioned affordance definitions/requests, capability and limit validation.
  - [x] Execution state machine, normalized failures, canonical event helpers, declarative workflows.
  - [x] PostgreSQL frozen registry, durable requests/executions, atomic intent Events, lifecycle API and restart tests.
  - [x] Persist-before-effect smoke lifecycle through the separate internal Executor boundary.
  - [x] Atomic CognitiveEmission Step integration across Frame, Claims, Attention and Executions.
  - [x] Separate executor process with declarative workflows, effect-boundary validation, and safe filesystem adapter.
- [x] **M6 — Adaptive Planner:**
  - [x] Bounded planner context/state, capability checks, step limits, recorded replay adapter.
  - [x] Durable planner runs/steps, proposal-before-effect persistence, restart resume, and executor integration.
- [x] **M7 — Time travel, snapshots, and blame:**
  - [x] Pure snapshot metadata/hash/selection, replay purity contract, deterministic blame traversal.
  - [x] Strict event cursors, inline PostgreSQL snapshots, corruption fallback, frame replay and blame APIs.
- [x] **M8 — Fork and A/B cognition:**
  - [x] Branch/ForkGroup models, deterministic immutable fork roots, trajectory comparator.
  - [x] PostgreSQL branch heads, atomic fork transaction, CAS, branch APIs, persisted comparisons and isolation tests.
- [x] **M9 — Procedures:**
  - [x] Deterministic evidence-backed procedure projection from terminal Executions.
  - [x] Smoothed success rates, failure penalties, minimum evidence and poisoning mitigation.
  - [x] PostgreSQL rebuild/list/get/match API and deterministic RenderPacket integration.
- [x] **M10 — Human Cognitive Debugger:**
  - [x] React/TypeScript episode timeline and Frame/RenderPacket inspector.
  - [x] Attention, outside-frame, provenance, Claims, Events and Executions views.
  - [x] Time travel, replay, blame, fork and branch inspection operations.
  - [x] Production static container and full Compose deployment.
- [x] **M11 — World interface (read-only):**
  - [x] Frozen capability registry (`observe/read/write/execute/communicate/navigate`) separated from affordances; worlds grant capabilities, definitions only reference them.
  - [x] Versioned World protocol object (resources, capabilities, identities, credential refs, limits, policies) with `world.registered`/`world.state_updated` Events carrying full snapshots; worlds table is a projection.
  - [x] Read-only world adapter: filesystem stat/list/read with path containment, pure-Go git status/log (no shell-out), and HTTP reads restricted to declared endpoints.
  - [x] Standard read affordances (`inspect_environment`, `inspect_workspace`, `list_files`, `read_file`, `git_status`, `git_log`, `inspect_http`) as deterministic Executor workflows.
  - [x] `world.observation` Events committed atomically with execution transitions; observations enter memory only through the Execution/Event pipeline.
  - [x] Intent-time and effect-time world re-authorization: capability denial fails the execution (`permission_denied`) and world version divergence is recorded.
  - [x] World CRUD HTTP API, `world_id` on executions and steps, `WORLD_ID` executor binding, World JSON Schema, and smoke coverage.
- [x] **M13 — Bootstrap knowledge:**
  - [x] Generic ingestion pipeline: bounded read-only observation of declared world resources through the world adapter, committed as canonical `world.observation` Events with `ingestion` provenance (no context-injection path, no separate knowledge base).
  - [x] Deterministic extractors (`gomod.v1`, `npm.v1`, `cargo.v1`, `python.v1`, `readme.v1`, `git.v1`, `http.v1`, `listing.v1`) turn observations into candidate Claims with confidence below 1.
  - [x] Claims cite the observation Events as evidence: `Commit.Evidence` with a `claim_evidence` table, unknown evidence rejected, provenance chain readable via `GET /v1/claims/{id}/evidence`.
  - [x] `POST /v1/ingest` bootstrap flow over filesystem/git/http resources with depth and event budgets, truncation reporting, and smoke coverage against an empty substrate.
- [x] **M14 — Entity / knowledge graph:**
  - [x] Claim triples: optional `subject`/`predicate`/`object` (entity refs like `module:example.com/app`) validated all-or-none against frozen registries of entity types and predicates; persisted in the claims table and exposed through `GET /v1/claims/{id}`.
  - [x] Ingest extractors emit triples: manifests bind packages/modules to files (`declared_in`), dependencies (`depends_on`, bounded per manifest), toolchains (`targets`), and root ownership (`contains`); git status binds repositories to branches (`on_branch`).
  - [x] Entity projection (`projection.BuildEntities`): global rebuildable graph over triple-bearing claims with deterministic content-derived ids, mention counts, max-confidence entity trust, and refuted/superseded claims excluded; `entities`/`entity_relations` tables replaced atomically on rebuild.
  - [x] Entity-aware attention (M14.3): `entity` refs in frame focus/working set, entity candidates in the ambient pool gated by frame relevance (focus/objective overlap, graph proximity, pins) and bounded so a large world graph cannot flood every render.
  - [x] Entity HTTP API (`POST /v1/projections/entities/rebuild`, `GET /v1/entities?type=`, `GET /v1/entities/{id}` with relations) plus postgres/memory store coverage, integration tests, and smoke coverage asserting entities appear in the render map.

## Architectural constraints

1. PostgreSQL is the canonical source of truth; projections are rebuildable.
2. Events represent runtime facts, never model intentions.
3. Replay never repeats irreversible side effects.
4. Models cannot access the substrate directly.
5. Execution cannot mutate Frames.
6. Protocol objects remain JSON/JSON Schema versioned as `frp/0.3`.
