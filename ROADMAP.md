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
- [ ] **M5 — Affordance registry and deterministic Executor:**
  - [x] Versioned affordance definitions/requests, capability and limit validation.
  - [x] Execution state machine, normalized failures, canonical event helpers, declarative workflows.
  - [x] PostgreSQL frozen registry, durable requests/executions, atomic intent Events, lifecycle API and restart tests.
  - [x] Persist-before-effect smoke lifecycle through the separate internal Executor boundary.
  - [x] Atomic CognitiveEmission Step integration across Frame, Claims, Attention and Executions.
  - [ ] Separate physical executor worker.
- [ ] **M6 — Adaptive Planner:**
  - [x] Bounded planner context/state, capability checks, step limits, recorded replay adapter.
  - [ ] Durable child planner episode and executor integration.
- [ ] **M7 — Time travel, snapshots, and blame:**
  - [x] Pure snapshot metadata/hash/selection, replay purity contract, deterministic blame traversal.
  - [ ] Event cursors, PostgreSQL snapshots/provenance edges, replay/blame APIs.
- [ ] **M8 — Fork and A/B cognition:**
  - [x] Branch/ForkGroup models, deterministic immutable fork roots, trajectory comparator.
  - [ ] PostgreSQL branch heads/fork transaction, branch APIs, persisted comparisons and isolation tests.
- [ ] **M9:** Procedures.
- [ ] **M10:** Human Cognitive Debugger.

## Architectural constraints

1. PostgreSQL is the canonical source of truth; projections are rebuildable.
2. Events represent runtime facts, never model intentions.
3. Replay never repeats irreversible side effects.
4. Models cannot access the substrate directly.
5. Execution cannot mutate Frames.
6. Protocol objects remain JSON/JSON Schema versioned as `frp/0.3`.
