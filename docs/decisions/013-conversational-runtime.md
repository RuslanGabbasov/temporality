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
