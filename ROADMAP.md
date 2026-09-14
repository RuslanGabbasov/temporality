# Temporality delivery roadmap

The milestone order follows FRP v0.3 section 38.

- [x] **M0 foundation:** Event protocol, append-only log, deterministic replay, PostgreSQL and memory adapters, HTTP API.
- [ ] **M0 hardening:** PostgreSQL integration tests, cursor pagination, replay manifests, metrics, crash-recovery test.
- [ ] **M1:** Claims and evidence relations.
- [ ] **M2:** Immutable Frame and deterministic reducer.
- [ ] **M3:** RenderPacket and Objective.
- [ ] **M4:** Deliberate and ambient Attention.
- [ ] **M5:** Affordance registry and deterministic Executor.
- [ ] **M6:** Adaptive Planner.
- [ ] **M7:** Time travel, snapshots, and blame.
- [ ] **M8:** Fork and A/B cognition.
- [ ] **M9:** Procedures.
- [ ] **M10:** Human Cognitive Debugger.

## Architectural constraints

1. PostgreSQL is the canonical source of truth; projections are rebuildable.
2. Events represent runtime facts, never model intentions.
3. Replay never repeats irreversible side effects.
4. Models cannot access the substrate directly.
5. Execution cannot mutate Frames.
6. Protocol objects remain JSON/JSON Schema versioned as `frp/0.3`.
