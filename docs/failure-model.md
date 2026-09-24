# Agent Kernel failure and delivery model

Status: target policy for the first durable run vertical.

## Invariants

1. A consequential effect is never intentionally started before a durable operation intent exists.
2. Model/tool activities may be retried only under an explicit idempotency policy.
3. Workflow history and Temporality event history have different responsibilities; neither substitutes for the other.
4. Event delivery is at least once from a durable outbox and deduplicated by source/event ID.
5. A missing or delayed Temporality projection must be visible as lag/gap, not represented as successful immediate semantic persistence.

## Failure table

| Failure | Runtime behavior | Event/provenance behavior |
|---|---|---|
| Model timeout/provider error | bounded retry for retryable errors; otherwise return failure to workflow, which may request another turn | started + failed events, model metadata; no raw prompt by default |
| MCP/tool timeout/error | the PoC makes one activity attempt and reports a generic uncertain outcome; do not repeat a consequential call until its effect is reconciled | started + failed, operation ID, error class and `outcome: uncertain`; never store raw tool arguments by default |
| Sandbox crash | activity heartbeat/cancellation stops work; create a fresh sandbox only when operation is replay-safe | sandbox lifecycle and failed operation event; preserve log artifact if available |
| Worker/process crash | Temporal replays workflow decisions and retries incomplete activities according to policy | already committed outbox entries republish; stable event/operation IDs deduplicate |
| Temporal unavailable | reject new durable run starts; active worker behavior depends on service connectivity and retries | no claim of new durable progress without Temporal acknowledgements |
| Kernel PostgreSQL/outbox unavailable before read-only call | default fail closed; optional explicitly configured degraded mode may continue and later mark the gap | no event is claimed durable; emit `observability.gap` when storage returns |
| Kernel PostgreSQL/outbox unavailable before write | stop before effect | no unrecorded write is allowed |
| Crash after external write, before effect result saved | do not blindly rerun; reconcile by operation ID or mark uncertain and request repair | intent remains; later result/reconciliation links to same operation |
| Temporality API/database unavailable | outbox retains events and retries; UI shows ingestion lag | delivery is at least once, not synchronous |
| Duplicate event delivery | ingest no-op when key and payload match; conflict when same key has different payload | exactly one semantic event by `(source.id,event_id)` |
| Human approval timeout/rejection | Workflow remains waiting until decision/deadline, then resumes/rejects/cancels under declared policy | requested and granted/rejected/timed-out transitions with actor and policy |
| Run cancellation | workflow cancels children/activities cooperatively; effect already committed is not undone automatically | cancellation and partial execution remain visible; compensation is a separate operation |

## Side-effect boundary

There is no transaction spanning Temporal, PostgreSQL, a model provider, a remote MCP server, a sandbox, object storage and Temporality ingestion. The kernel uses a local transactional outbox for its own event intent and idempotency records. For a remote effect:

1. persist `operation_id`, arguments hash/reference, actor, policy result and `tool.started` event into the outbox;
2. call the downstream system with that stable idempotency key when supported;
3. persist result state and artifact refs in the outbox;
4. publish both semantic events at least once;
5. if the remote system lacks idempotency/read-after-write, transition to `uncertain` and require reconciliation rather than automatic repetition.

Exactly-once external effects are not promised. The PoC configures one attempt for tool Activities, but a worker can still crash after the remote service commits and before Temporal records the Activity result. Such a result is uncertain; the model is instructed not to repeat consequential calls blindly. A future tool adapter should use downstream idempotency keys or a read-after-write reconciliation API. Event deduplication gives exactly-once projection effect for a given source key, not exactly-once transport.

## Ordering and recovery

Every run gets a monotonic sequence allocated by its workflow/outbox transaction. Event IDs remain stable across retry. Delivery can be reordered across runs; within one run the publisher prioritizes sequence but consumers use explicit causal IDs and can buffer missing parents. `occurred_at` is observed producer time and `received_at` is ingestion time. Projection rebuilds are deterministic over causal/sequence order and mark unresolved dependencies rather than inventing order.

## Retention and schema evolution

Temporal workflow history is bounded/continued for long runs independently of the semantic journal. Semantic events and artifacts have separate retention policy and legal hold controls. Event schemas are additive within a major version; unknown data is preserved. A consumer that cannot safely interpret an unknown `knowledge.*` mutation quarantines it and reports projection lag rather than dropping it.
