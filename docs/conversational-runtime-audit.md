# Conversational Runtime: repository audit

Date: 2026-11-27  
Scope: Phase 0 of [conversational-runtime.md](conversational-runtime.md).

## Current component map

| Concern | Current implementation | Notes |
|---|---|---|
| Channel ingress and intake | `kernel/agent/channelinbound.go`, `cmd/agent-kernel/channelinbound.go`, `cmd/agent-kernel/intake.go` | `InboundMessage` normalizes transport input. The kernel loop handles polling, identity, reply matching, intake and routing. Intake owns the channel dialog/classification flow. |
| Interactive replies | `kernel/agent/channelinbound.go`, `cmd/agent-kernel/channelinbound.go` | `MatchChannelReplies` matches exact `ThreadRef` first, then falls back to the latest open request in an owned chat. Buttons/widgets are handled separately by the callback/widget paths. |
| Human requests | `workspace/models.go`, `workspace/humanrequest.go`, `migrations/workspace/0009_human_request_schema.up.sql` | `HumanRequest` is durable and tied to `run_id`; team fan-out is stored in `human_request_delivery`. Thread identity is an optional string on the request. |
| Artifact approvals | `workspace/artifactlifecycle.go`, `cmd/agent-kernel/artifactreviewreply.go`, `cmd/agent-kernel/artifactnotify.go` | Review rounds are the source of truth. Channel replies resolve through `ResolveArtifactReview`, which checks reviewer membership and an open round. A synthetic human request row supplies channel delivery and inbox state. |
| Workspace chat persistence | `workspace/chats.go`, `cmd/agent-kernel/chats.go`, `debugger/src/Workspace.tsx`, `debugger/src/workspaceApi.ts` | The backend stores the UI transcript as opaque JSON. Chat/task identity and ownership exist, but the model is separate from channel conversations and messages. |
| Workflow execution | `cmd/agent-kernel/main.go`, `kernel/agent/workflow.go` and related agent workflow files | `AgentRun` executes on Temporal. HTTP/task entry points start workflows; channel intake has its own routing and workflow-start path. Human requests signal waiting workflows. |
| Delivery | Channel drivers under `kernel/agent` and `cmd/agent-kernel`; delivery and notification code under `cmd/agent-kernel` | Transport polling and protocol details are driver-owned; the kernel loop orchestrates send, reply, callbacks, widgets, cursors and retries. Workspace chat delivery is browser/API based. |

## Dependency outline

```text
channel driver -> InboundMessage -> channelInboundLoop
                                   |-> MatchChannelReplies -> HumanRequest -> Temporal signal
                                   |-> artifact review resolver -> ArtifactReview transition
                                   `-> intake classification/routing -> task/run -> Temporal AgentRun

Workspace UI -> Workspace API -> workspace_chat (opaque transcript)
```

The shared persistence boundary is the workspace store, but the conversation models and execution paths are distinct. Interaction callbacks use opaque tokens and a separate resolution path; plain text replies use `PendingAsk` projections and thread/chat matching.

## Existing entities and repeated mechanisms

- Existing durable entities: `HumanRequest`, `HumanRequestDelivery`, `ArtifactReview`, `Artifact`, `workspace_chat`, inbound-message rows, run/task records, channel cursor, callback/widget records.
- Existing address fields: human request ID, artifact review ID, channel `ThreadRef`, transport message IDs in normalized inbound messages, callback `SourceMessageID` and opaque `CallbackData`.
- Existing response paths: human-request workflow signal, artifact review state transition, widget/callback resolution, workspace chat transcript update. These paths have separate validation and lifecycle behavior.
- Existing concurrency controls include database transactions/row locks for artifact review transitions, workflow identity/deduplication, and per-loop/channel cursor processing. There is no unified per-conversation sequence or keyed conversation lock in the inspected ingress path.

## Risks and migration points

1. Reply matching now prefers the transport reply target and uses the latest open request in the chat when no target is present. This follows the clarified product rule; adapters must preserve reply identifiers, and transports whose reply metadata is not available will use the latest-request fallback.
2. `ThreadRef` is optional and transport-specific. A common interaction reference needs to preserve existing thread matching while supporting source message IDs and typed domain IDs.
3. Artifact reviews currently piggyback on `HumanRequest` delivery. Migration must retain channel delivery, inbox counts, cancellation behavior and reviewer authorization while making the review ID explicit.
4. Workspace transcript JSON is browser-shaped and task-linked. Introducing normalized `Conversation`/`ConversationMessage` storage needs dual-read/write or a deliberate migration path to avoid losing existing chats.
5. Channel intake and Workspace chat have different execution and delivery entry points. A unified runtime will need adapter boundaries around these existing paths rather than assuming they already share a message contract.

## Baseline and audit limits

Repository baseline is the current commit/worktree state on 2026-11-27. No performance measurements or regression-corpus scores are recorded in this audit. Per the session instruction not to run or add tests unless requested, test suites and benchmarks were not run. Existing test files were located alongside the affected components; baseline numbers remain a Phase 0 follow-up before performance claims are made.

## Phase 0 exit

The current component map, dependencies, existing entities, repeated interaction paths, and migration risks are recorded above. `MatchChannelReplies` now prefers exact reply/thread references and otherwise selects the latest open request. Telegram inbound normalization now preserves `reply_to_message.message_id`; Slack, Mattermost and Matrix already populate `ReplyTo` from their reply/thread metadata.
