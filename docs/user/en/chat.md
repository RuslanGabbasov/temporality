---
order: 3
section: Working with the agent
title: Chat and runs
description: Dialogue with the agent — from a message to a result: streaming, amendments, approvals, delegation.
---

# Chat and runs

The agent's working surface is **Workspace** (`/workspace`). Every message
starts a durable run: it lives in Temporal, is journaled step by step, and
survives restarts.

## How a task is submitted

- A regular message starts a **new run** in the conversation's context.
- If a run is already executing, your message becomes an **addendum to the
  running task** — the input placeholder says so ("Will be added to the
  running run…"). The run is not duplicated.
- Files can be attached to a message — they land in the project's
  `/workspace/inbox` and are available to the agent.

The reply streams in chunks; reasoning-model "thinking" expands in a separate
block. Markdown (including tables and code) is rendered.

## Approvals (human in the loop)

For consequential actions (writing files, commands with outward effects —
depending on the agent's policy) the agent must get your decision. The
question appears as a card in the chat and under **Runs → questions**. If the
agent has notification channels configured, the question goes there too (see
[Inbound and channels](inbound.html)).

Answers always happen in the app; external channels are delivery only.
You can cancel a pending approval — the run continues another way or ends
gracefully.

## Delegation and plans

For larger tasks the agent can decompose the work:

- **`delegate`** — a subtask to another agent with its own prompt and turn budget.
- **`plan`** — a task DAG with dependencies: independent branches run in
  parallel, dependent results are appended to followers' prompts, review tasks
  act as acceptance gates (a rework verdict sends work back with feedback).

Plan progress is visible right in the chat: tasks, statuses, dependencies.
The full mechanism — `docs/agent-delegation.md`.

## Amendments

Via channels (and the intake pipeline) you can send an **amendment to a task
that is running or finished**: the intake run asks a clarifying question when
unsure which run you mean, then starts a continuation on the same context.
See [Inbound and channels](inbound.html).

## Watching the work

- **Runs** (`/agents`) — the feed of runs and each run's trace: model calls,
  tools with arguments and results, tokens and cost, questions and approvals.
- **Uncertain operations.** If a tool's effect could not be confirmed (a
  transport failure, a restart), the operation lands in the reconcile queue —
  you manually confirm "happened / did not happen" and the run continues
  correctly.
- **Sagas** (`/dashboard → Sagas`) — long multi-agent efforts: a summary of
  budget, tokens, time and approvals; you can rename a saga, merge duplicates
  or bind an orphan task to it.

## Branching

Any message in a conversation can spawn a branch — continue the conversation
from that point along a different path; the original branch stays untouched.
