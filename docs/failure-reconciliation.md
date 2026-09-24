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
| During remote MCP effect | `tool.started`; possibly no result | Outcome can be uncertain | Manual downstream reconciliation only |
| During sandbox command | `tool.started`; command may have modified workspace | Partial workspace effect is possible | Inspect workspace, rerun validation, then record a new operation |
| After Activity completion, before `tool.completed` enqueue | Temporal Activity result | Effect/result may exist without semantic completion event | Not automated; inspect Temporal history and append a future reconciliation event |

## Phase 2 remaining work

Add a durable operation ledger (intent, hash, actor, policy, effect state, result reference), propagate idempotency keys where downstreams support them, expose an `uncertain` state and reconciliation endpoint/event, and fault-inject crashes at each boundary. Do not call this exactly-once. Do not infer successful execution from an agent-authored final answer.
