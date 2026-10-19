---
order: 12
section: Integrations
title: External agents (experience layer)
description: How to wire any framework or pipeline into the experience loop — events, knowledge extraction, hints.
---

# External agents: Temporality as an experience layer

Temporality is not just a harness. The experience journal is available
over HTTP, so the loop "events → knowledge → hints" can be enabled for an
agent running in any framework — LangGraph, LangChain, a custom pipeline —
without moving execution into the kernel.

## The three-step contract

1. **Report the run's events** to the journal: `POST /v1/observations/events`
   (batches of up to 100), schema `temporality.event/1`.
2. **When the run reaches a terminal event** (`run.completed` / `run.failed` /
   `run.cancelled`), call extraction: `POST /v1/agent/runs/{run}/extract?project=…`
   (a kernel API; idempotent — a repeat call answers 409).
3. **Ask for hints** before new tasks: `POST /v1/observations/hints` with the
   task text — the project's relevant knowledge comes back with state and
   cautions.

The minimal event set extraction understands: `run.started`,
`turn.started`, `model.started`, `model.completed` (`output_tokens` and
`content` are useful), `tool.started` / `tool.completed` / `tool.failed`
(with `tool` and the result), `run.completed`. Anything unknown is
ignored — report as much as you find useful.

## Authentication

The journal accepts workspace user bearer tokens (the same ones the
kernel uses). For a service integration, create a dedicated user with the
writer role on the target project.

## A ready example

[`examples/external-agent/external_agent.py`](https://github.com/RuslanGabbasov/temporality/blob/main/examples/external-agent/external_agent.py) —
~100 lines of Python with no SDK: a mini agent on an OpenAI-compatible
API that reports events, calls extraction and queries hints.

```sh
pip install requests
export TEMPORALITY_URL=http://localhost:8080
export TEMPORALITY_TOKEN=<journal bearer>
export KERNEL_URL=http://localhost:8090
export MODEL_BASE_URL=https://api.openai.com/v1
export MODEL_API_KEY=sk-…
export MODEL_NAME=gpt-4o-mini
python3 external_agent.py my-project "Set up CI for the monorepo"
```

The first run performs the task and extracts knowledge. A second run with
a similar task already receives hints from the accumulated experience.

## What an external agent gets

| Capability | Endpoint |
|---|---|
| Run event reporting | `POST /v1/observations/events` |
| Knowledge extraction (kickoff) | `POST /v1/agent/runs/{run}/extract` |
| Hints before a task | `POST /v1/observations/hints` |
| Knowledge listing and dossiers | `GET /v1/observations/knowledge` |
| Correct/challenge/promote knowledge | `POST …/knowledge/{invalidate,challenge,promote}` |
| Semantic neighbors of knowledge | `GET /v1/observations/knowledge/embeddings/neighbors` |
| Run traces and comparison | `GET /v1/observations/runs/{state,compare}` |

Limitation: durable execution, HITL, sandboxes and delegation are part of
the kernel harness. If the agent needs those, move execution into the
kernel (see Quick start); the experience survives — the event types are
the same.

## How it looks in the interface

External runs appear in the same lists and timelines (project + run id
tie the events together), extracted knowledge shows up in the knowledge
explorer with provenance back to the originating run, and conflicts land
in the supervisor queue.
