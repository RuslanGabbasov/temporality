# Replay model (Phase 2 audit)

## Two different replay mechanisms

**Temporal workflow replay** re-executes deterministic workflow code against Temporal's history to recover orchestration state. It is not a user-facing semantic reconstruction and does not recreate external side effects.

**Temporality semantic replay** must reconstruct an execution/knowledge view from immutable observation events. It must be read-only and must never invoke a model, tool, MCP server, or sandbox.

## Current state

The FRP subsystem has persisted-frame replay and time-travel APIs. Agent Kernel currently emits stable event IDs, per-run sequence, frame IDs, parent-frame IDs, operation IDs where known, `caused_by`, and parent event links. The observation API can query by project/run/type/time and knowledge projections support historical valid/known-at reads. There is not yet an AgentRun semantic replay endpoint that produces an ordered run state, nor a generic `diff(T1,T2)` for execution plus knowledge. Temporal history remains required to recover the Kernel's final `RunResult`.

## Target semantics

- `run_state(T)`: latest run/delegation/operation transitions whose event time is at or before T, retaining unresolved `started` operations and surfacing missing causal parents.
- `knowledge_state(T)`: lifecycle projection from knowledge events at T, distinct from transaction-time `known_at`.
- `diff(T1,T2)`: events and projected state changes between two cutoffs, ordered by producer sequence/causality within a run; no cross-run total order is invented.
- replay input is a fixed event snapshot/cursor; projection version and schema versions are returned with output.

## Acceptance still required

Create an event corpus with approval, code change, MCP, failure and recovery; rebuild the same view twice and compare deterministic hashes; test a late-arriving causal parent; test state/diff boundaries; prove replay performs no external calls. Current implementation does not yet pass these tests.
