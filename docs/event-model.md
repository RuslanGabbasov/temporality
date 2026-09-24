# Event model audit (Phase 2)

## Current envelope

The implemented universal envelope is `temporality.event/1`: event ID/time, source, context (project/run/task/actor/parent event), type, data and evidence refs. Kernel events currently place `frame_id`, `parent_frame_id`, `sequence`, `caused_by`, operation IDs and payload-specific fields in `data`. The outbox and Temporal workflow provide a source-scoped deduplication identity and orchestration history, respectively.

## Audit against Phase 2 queries

| Query need | Current representation | Audit |
|---|---|---|
| What happened? | `type`, `data` | Available, but event payload contracts are not centrally versioned |
| Who did it? | `context.actor` | Run actor is present; approval events also include approver in data |
| When? | `occurred_at`, server `received_at` | Available; ordering must use sequence/causality per run |
| In which execution/frame? | `context.run`, `data.frame_id`, `data.parent_frame_id` | Available for current Kernel emissions; not first-class envelope fields |
| What operation? | `data.operation_id`, tool name/hash | Approval identity is now hash-bound; no shared operation entity/lifecycle projection yet |
| What caused it? | `context.parent_event_id`, `data.caused_by` | Immediate chain is emitted; cross-run parent links exist for delegated events |
| What evidence appeared? | top-level `evidence[]`, `evidence.observed` data | Refs can be carried, but Kernel has no artifact store and tool results are not generally addressable evidence artifacts |
| What knowledge changed? | `knowledge.*` event family | Two proposal channels: model `remember` (claims) and the kernel execution-observation heuristic (`kernel-heuristic/execution-observation.v1` proposes kind `observation` after successful verification/build commands; repeats confirm via `execution-reverification.v1`); challenge/correct beyond this is not automatic |
| Schema compatibility? | fixed envelope `temporality.event/1` | `data` is extensible; per-event data schema versions are not yet validated |

## Decision for Phase 2

Do not change the universal envelope before collecting the real code-change/MCP corpus. Keep operation and frame fields in the current extension data for compatibility. Add event-family schema docs and validators only for fields backed by concrete queries. The audit identifies the missing artifact/result linkage and explicit operation record as the largest provenance gaps.
