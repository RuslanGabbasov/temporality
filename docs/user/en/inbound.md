---
order: 9
section: Automation
title: Inbound and channels
description: Connect Matrix, Telegram, Slack, Mattermost and webhooks — the agent receives tasks from your team's chat.
---

# Inbound and channels

Temporality accepts tasks from messengers: you write in the team chat — the
intake pipeline parses the message and starts the right agent on it. Pages:
**Inbound** (`/inbound`) — the pipeline and statuses; **Channels**
(Settings → Delivery channels) — transports.

## Delivery channels

Agent questions (`ask_human`) are delivered to the recipient in their
preferred channel; configured in the user profile. The built-in channel is
the in-app question mailbox (Runs → questions). External transports:

| Channel | Direction | Setup |
|---|---|---|
| Matrix | question delivery | homeserver + bot token; the address is a room ID |
| Telegram | question delivery | bot token; the address is a chat ID |
| Mattermost | delivery + inbound (WebSocket) | base URL + bot token |
| Slack | delivery + inbound (Events API) | bot token + signing secret |
| Webhook | inbound | signing secret |

Channels are edited in the UI without a restart (DB values override env);
secrets are never returned — only an "is set" marker. Delivery never blocks a
run: a transport failure is recorded as an event and the question stays in the
UI. A channel question can be answered right in the chat: a reply in a two-way
channel (Matrix, Mattermost, Slack) is routed to the waiting run as the
answer; for team questions the first reply wins. Operation approval
decisions happen in the UI.

## The intake pipeline

A user-initiated message goes through classification:

| Class | What happens |
|---|---|
| `task` | A new task: resolve the project (by mention, `search_projects`, a clarifying question) → `start_task` |
| `amendment` | An amendment: `run_status` lists your earlier runs, intake clarifies which one it belongs to and starts a continuation |
| `status` | A progress question — answered from data without starting work |
| `cancel` | Cancels an earlier run started by the same sender |
| `chatter` | No work — a polite reply |
| `reply` | An answer to the agent's question — delivered to the waiting run |

The pipeline is enabled **per project** with the "Accept tasks from channels"
toggle and constrained by org policies, a per-sender limit
(`KERNEL_INTAKE_MAX_PER_HOUR`) and body retention (`KERNEL_INTAKE_RETENTION_DAYS`).
The audit trail "message → intake run → task run" is kept without message bodies.

## What the user sees

The **Inbound** section lists messages with statuses (received → classified →
clarifying → routed) and outcomes. In the channel the bot replies: where the
task landed, what clarification it needs, status on request.
