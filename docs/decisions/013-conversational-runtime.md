# ADR-013: Conversational Runtime foundations

## Status

Proposed for implementation with the Conversational Runtime migration.

## Context

Workspace chats currently persist a browser-shaped transcript as opaque JSON, while channel conversations are inferred from chat addresses, human-request rows and optional transport thread IDs. Tasks and AgentRuns have durable lifecycles of their own. The runtime needs stable conversation/message identity and exact human-interaction references without replacing existing domain records or adding a broker.

## Decisions

1. Store normalized Conversation and ConversationMessage records in the existing workspace Postgres store and package. Keep the current `workspace_chat` API as a compatibility adapter during migration; do not make transcript JSON the runtime source of truth.
2. Represent an interaction reference as `{type, id, version?, source_message_id?}`. The type and domain ID are mandatory; version is mandatory for versioned artifact reviews. Transport adapters may provide a source message ID, but domain handlers remain responsible for authorization and state transitions.
3. Treat absent references as unresolved input. Exact source-message/thread matches may resolve directly. A unique pending interaction may be offered for explicit confirmation according to policy; multiple candidates must be clarified. FIFO must never commit a consequential decision.
4. Bind tasks and AgentRuns to conversations by foreign identity/reference fields; do not duplicate their lifecycle rows in Conversation storage.
5. Use an in-process keyed lock per conversation for initial serialization, with database sequence allocation as the cross-process correctness boundary. Do not serialize unrelated conversations globally.
6. Extend the existing transactional outbox/event-delivery pattern for durable runtime effects. Do not introduce a message broker for the first release.
7. Preserve existing HumanRequest, ArtifactReview and Approval stores and handlers. A shared Interaction Runtime resolves the reference and delegates to those domain handlers.

## Consequences

The initial implementation reuses PostgreSQL, workspace migrations and existing outbox infrastructure. Browser chat transcripts need a compatibility import/read path until normalized messages are authoritative. In-process locks improve local throughput but do not provide distributed exclusion; database sequence/uniqueness and idempotency constraints remain mandatory. Interaction migration must retain current reviewer, recipient, organization, status and audit checks.

## Alternatives considered

- Reuse `workspace_chat.transcript` as the normalized runtime store: rejected because it is UI-shaped, opaque to the backend and currently couples messages to task-oriented chat behavior.
- Introduce a separate event broker: rejected for the first release because the current database/outbox mechanisms can support durable dispatch with fewer operational dependencies.
- Make Temporal workflow history the conversation store: rejected because conversation lifetime and availability must remain independent of task execution.

## Wave 2 additions (implemented)

1. Conversation turn responses deliver through the durable conversation outbox (`migrations/workspace/0045_conversation_outbox.*`, `workspace/outbox.go`), not the lossy in-memory run watcher. The legacy watcher remains as a fallback for stores that do not implement the conversation outbox interface; the outbox loop owns live retry with a backoff ladder, terminal events that still produce a task result, and a two-hour give-up for events with no transport.
2. Channels are runtime-first: channel intake keeps the intake engine for consequential work (task requests, files, agentless messages bypass the classifier), while conversational messages flow through the Conversation Runtime turn path with the same durable recovery (`redispatchStaleTurn`) as workspace chat.
3. The Fast Intent Router cascade runs L2 on the conversation lead's model with a 3-second timeout (adopt at confidence ≥ 0.75, clarify below 0.55 or when `requires_clarification`), falling back to the lead on malformed or missing output; L3 is lead-first — an active task in the conversation routes directly without any classifier call. The web `/route` endpoint stays deterministic-only and never calls a model.
4. Lead binding is fill-only: `AttachConversationLead` never overwrites an existing lead assignment.
5. A crashed turn (kernel restart mid-turn) is recovered by the outbox loop's stale-turn redrive: `ListStaleConversationTurns` includes `processing` rows, the transport source is resolved from the conversation's channel binding, and a permanent failure (no lead, agent disabled, conversation outside a project) marks the turn `failed` instead of retrying forever.
6. Shadow mode (§23) is deterministic-only: the shadow router scores every inbound channel message with level 1 + lead-first and never calls a model — the corpus validates the classifier offline (§20), the shadow validates the deterministic layer against the legacy intake verdict in production. Agreement is semantic, not literal: a legacy «amendment» served by a lead-first continuation is agreement; a legacy «task» that the runtime would have answered with a bare direct turn is the disagreement the shadow exists to find. Rows live in `conversation_router_shadow` (migration 0046), one per inbound message, keyed by the stable channel message id; nothing in the table drives behavior. Opt-out is a per-channel setting (`conversation_shadow_enabled`), default on.
7. The regression corpus (§20) is a committed JSON fixture (`cmd/agent-kernel/testdata/conversation_corpus.json`) with a two-pass harness: the deterministic pass must hold with no model, the cascade pass scripts the classifier and additionally pins the classifier-round budget (lead-first and high-confidence deterministic outcomes must cost zero rounds). Corpus changes and router changes must land in the same commit.
