# Side-effect and event reconciliation (Phase 2 audit)

## Current guarantee

The Kernel persists event intent to `kernel_event_outbox` before the workflow advances past each `ActivityRecordEvent`. Outbox rows are unique by `(source_id,event_id)`. The publisher retries Temporality delivery, accepts `accepted` and `repeated`, and marks rows delivered only after acknowledgement. Temporality's idempotency key makes same-payload redelivery a semantic no-op; a different payload under the same key is a conflict. This is at-least-once publication with idempotent ingestion, not exactly-once transport.

For a tool side effect, `tool.started` is committed to the local outbox before `ActivityRunTool` is scheduled. The workflow configures one attempt for that activity. On a normal returned error it records `tool.failed` and, for MCP, marks the outcome uncertain. A successful Activity result is held in Temporal history; then `tool.completed` is enqueued.

## Unresolved crash window

A worker can lose contact after a remote effect commits but before Temporal records the Activity completion. The tool may have executed even though the Activity is retried/timed out and no `tool.completed` event exists. The outbox can prove intent, not the remote outcome. MCP currently has no generic idempotency/read-after-write contract, and `ToolRequest.OperationID` is not passed as a standardized idempotency key to every MCP server. Docker command effects are confined to the mounted workspace but are not transactionally rolled back.

Therefore the current policy is: do not automatically retry a consequential operation; preserve `tool.started` plus failed/uncertain status when observable; require operator reconciliation before repeating. This is not yet a complete reconciliation mechanism: after a hard worker crash, an automated reconciler does not query the downstream service or create a durable `operation.reconciled` event.

## Failure matrix

| Boundary | Durable before next step | Failure interpretation | Recovery available |
|---|---|---|---|
| Event record → Kernel PostgreSQL | Outbox row | No row means workflow cannot safely proceed past that event activity | Temporal activity retries up to its configured attempts |
| PostgreSQL → Temporality | Outbox row | Projection may lag while local event intent remains | Publisher retries; same event ID is idempotent |
| Before tool Activity | `tool.started` outbox row | Intent exists; effect may not have started | Inspect Temporal Activity state |
| During remote MCP effect | `tool.started`; possibly no result | Outcome can be uncertain | Operator reconciles over `operation.reconciled` |
| During sandbox command | `tool.started`; command may have modified workspace | Partial workspace effect is possible | Inspect workspace, rerun validation, then record a new operation |
| After Activity completion, before `tool.completed` enqueue | Temporal Activity result | Effect/result may exist without semantic completion event | Listing surfaces it; operator reconciles |

## Implemented (2026-09-25)

The remaining-work list below is now implemented; the design decision along the
way was that **the ledger is a projection, not a table**: the event stream stays
the single source of truth, and operation state is derived from `tool.*`
events plus Temporal liveness on demand.

- **Effect semantics on `tool.failed`** — `effect=none` marks pre-execution
  rejections (non-retryable argument validation: safe to re-issue),
  `effect=uncertain` marks failures at or after the execution boundary, and
  `effect=occurred` marks a failed call whose effects fully landed and are
  settled by the journal (currently only delegation). Legacy
  events without the field but with `error_type=activity_failed` count as
  uncertain.
- **Delegation classification** — the `delegate` tool is internal, so its
  failures are classified precisely instead of defaulting to uncertain:
  * `delegated_run_start_failed` — the child execution was rejected before it
    began (duplicate workflow id, namespace shutdown): `effect=none`, nothing
    ran.
  * `delegated_run_failed` — the child started and failed. Right after the
    failure the `kernel.summarize_child_run` activity replays the child's own
    events from the journal: if every child operation is settled, the parent's
    `tool.failed` carries `effect=occurred` (or `none` when the child performed
    zero operations) with `child_ops_total`/`child_ops_unresolved` fields;
    only genuinely unresolved child operations keep `effect=uncertain`. The
    operations listing surfaces `child_run_id` and the summary so the UI can
    point at the child trajectory instead of "check the external system".
- **MCP idempotency keys** — every `mcpclient.Call` propagates the operation id
  via the protocol's `_meta` field; servers that support idempotent execution
  deduplicate retries. The test MCP server persists the key for `create_issue`,
  so the contract survives the per-call stdio process restart that mimics the
  retry window.
- **Operations listing** — `GET /v1/agent/operations?project=` (reader)
  projects the journal into operations whose effect state is unresolved:
  `crash_window` (no terminal event, workflow no longer running),
  `stale_in_flight` (no terminal event after 10 minutes inside a running
  workflow) and `failed_uncertain` (uncertain failure). Completed, blocked,
  rejected (`effect=none`) and already-reconciled operations are settled and do
  not resurface.
- **Reconciliation** — `POST /v1/agent/operations/reconcile` (operator) writes
  a durable derived `operation.reconciled` event: `effect` is a closed
  vocabulary `none|occurred|unknown` (`unknown` is a legitimate terminal
  record), the note is bounded to 2000 bytes, and provenance links back to the
  `tool.started` event via `parent_event_id`. A recorded verdict settles the
  operation — including verdicts over previously uncertain failures.
- **Fault injection** — `KERNEL_FAULT_AFTER_EFFECT` arms a live drill. The value
  is either an exact operation id or a tool name (`run_command`,
  `mcp__create_issue`), which faults the first matching execution per run —
  operation ids embed model-generated call ids and cannot be predicted before
  the run starts. The effect commits for real, then the worker reports a
  crash-style failure, producing an honest `tool.failed(effect=uncertain)`.

Live drill (2026-09-25, compose stack): a run with `KERNEL_FAULT_AFTER_EFFECT=run_command`
wrote `drill-proof.txt` in the workspace, recorded `tool.failed(effect=uncertain)`,
retried the command under a new operation id (success), and completed. The
operator then verified the file, recorded `operation.reconciled(effect=occurred)`
with the workspace file as evidence, and the listing cleared.

## Phase 2 remaining work

The mechanism above is implemented; what remains open on this front:
automated downstream read-after-write probes (the operator still decides),
and surfacing uncertain operations in the debugger UI. Do not call this
exactly-once. Do not infer successful execution from an agent-authored final
answer.
