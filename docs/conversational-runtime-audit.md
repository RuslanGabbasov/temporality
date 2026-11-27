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

## Wave 2 status (2026-11-27)

Implemented: normalized `Conversation`/`ConversationMessage`/`ConversationTurn` storage with per-conversation sequence and advisory-lock serialization; durable conversation outbox for turn responses with backoff, terminal-task-result flush and stale-turn redrive after a crash; runtime-first channel intake (conversational messages go through the same turn path, task requests/files stay on the intake engine); Fast Intent Router cascade (deterministic → L2 classifier on the lead's model with 3s timeout and confidence thresholds → lead-first when a task is active); run results projected back into the conversation as task-linked turns; `GET /v1/conversations/{id}/tasks` plus task chips in the chat header linking to the run trace.

Out of scope for this wave, per the ТЗ: §23 shadow-mode router comparisons, §24 conversation branching UI, knowledge/RAG integration in the Context Builder (§12.3 is stubbed to the existing knowledge store path).

Regression corpus (§20) is not yet populated; router confidence thresholds (0.75 adopt / 0.55 clarify) are set from the design targets and need corpus validation before tuning.

## Wave 3 status (2026-11-27): shadow mode + regression corpus

§20 corpus populated: `cmd/agent-kernel/testdata/conversation_corpus.json` covers all 20 minimum categories (23 cases), each pinning initial state, message, the deterministic-pass decision (level 1 + lead-first, no model), the cascade-pass decision with a scripted level-2 classifier, and the classifier-round budget. The harness (`TestRegressionCorpus`) asserts per-case intent/execution/router level in both passes, counts classifier rounds, and fails when a §20 category loses its only case; the corpus found and pinned two real behaviors: RU «запусти …» was added to the deterministic task verbs, and injection phrasing is pinned to route as a plain conversational turn (the anti-injection defenses live at the lead/system-tail and authorization layers, not in the route selector). Side-effect expectations stay declarative — the decision layer is what the corpus pins.

§23 shadow mode live: every inbound channel message the runtime router scores also lands in `conversation_router_shadow` (migration 0046, one row per inbound message, runtime half at dispatch time). Messages that fall through to legacy intake get the intake verdict patched into the same row when the workflow posts `POST /v1/intake/runs/{runID}/decision`; the semantic mapping (`shadowAgreement`) counts legacy task/amendment against runtime agent_execution or lead-first continuation as agreement, and a bare deterministic direct on a legacy task as the disagreement the shadow exists to surface (warned in the kernel log). Shadow scoring is deterministic-only — no model call, no side effects; per-channel opt-out via the `conversation_shadow_enabled` channel setting, default on. Org-level feature flags from §23 are deliberately represented at channel granularity for now: channels are the deployment unit of the kernel loop, and a channel belongs to one organization in practice; per-org router flags remain open work if multi-org channels ever appear. Agreement data now accumulates in SQL (`agreement`, `verdict_at`, partial index) for a future dashboard; thresholds stay untuned until that data exists.

Remaining from the ТЗ after this wave: normalized store as the authoritative chat store (dual-write with legacy transcript continues), legacy intake workflow removal after shadow data justifies it (§22 Phase 7 tail), SLO measurements (§25.2), runtime-specific metrics (§19), and ADRs for §26 items 6/9/11/12 (branching context, cache freshness, budget split, end-to-end observability).
