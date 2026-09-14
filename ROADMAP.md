# Temporality delivery roadmap

The milestone order follows FRP v0.3 section 38.

- [x] **M0 foundation:** Event protocol, append-only log, deterministic replay, PostgreSQL and memory adapters, HTTP API.
- [ ] **M0 hardening:**
  - [x] PostgreSQL integration test for persistence, duplicate rejection, and append-only enforcement.
  - [x] Versioned replay manifests included in replay digests.
  - [x] Opaque cursor pagination for memory and PostgreSQL event streams.
  - [x] Prometheus-compatible HTTP, append, and replay metrics.
  - [ ] Full crash-recovery killer test (blocked by Frame/Reducer/RenderPacket in M2–M3; durable Event Store restart is covered).
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
- [ ] **M4 — Deliberate and ambient Attention:**
  - [x] Versioned deterministic ambient scoring over normalized features.
  - [x] Stable tie-breaking, trust filters, explicit pins, and ambient-only hysteresis.
  - [x] Deliberate semantic jumps remain independent from ambient hysteresis.
  - [x] RenderPacket scored map, periphery, outside-frame count, and attention provenance.
  - [x] Rebuildable versioned Region projection and region-based ambient candidates.
  - [ ] Graph proximity projection and edges.
  - [x] Attention entropy, focus switches, ambient hit rate, missed candidates, and collapse metrics.
  - [x] Canonical `attention.selected` Events in the atomic Step transaction.
  - [ ] `attention.suggested` recording when a rendered packet is consumed by a model step.
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

## Architectural constraints

1. PostgreSQL is the canonical source of truth; projections are rebuildable.
2. Events represent runtime facts, never model intentions.
3. Replay never repeats irreversible side effects.
4. Models cannot access the substrate directly.
5. Execution cannot mutate Frames.
6. Protocol objects remain JSON/JSON Schema versioned as `frp/0.3`.
