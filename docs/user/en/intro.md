---
order: 1
section: Getting started
title: Introduction
description: What Temporality is, who it is for and what it is made of — a ten-minute overview of the system.
---

# Introduction

**Temporality** is an open observability and experience-compounding system for AI agents.

A conventional chat bot forgets everything between sessions, and its "memory" is a junk drawer of notes stuffed into context. Temporality works differently: the agent runs tasks in a durable runtime while the system records **what it knew and did** — where the knowledge came from, how it was applied, what held up and what turned out to be wrong, and how that experience changed over time. On the next task, verified experience comes back to the agent in a compact form that does not flood the context.

```text
Agent Kernel ──events──▶ Journal ──projection──▶ Knowledge / Hints
     ▲                      │                         │
     └──────── hints ◀──────┴──── Experience Timeline ┘
```

## Key ideas

- **A run** is a unit of agent work: a durable workflow on Temporal that survives restarts, deploys and laptop sleep. Every step is journaled.
- **Knowledge with provenance.** Statements extracted from trajectories carry an author, a source, a lifecycle status (proposed → confirmed → challenged…) and a bitemporal history. Knowledge can be challenged, corrected, invalidated — and you can see who and when.
- **Hints** — compact excerpts of relevant experience the system feeds the agent before a task. The agent can dig into full primary sources through research tools.
- **Sagas** — explicitly tracked long-running work that outlives a single run: chat continuations, delegated agents and amendments join the saga automatically.
- **Artifacts** — a registry of reference documents (policies, runbooks, checklists) agents load on demand instead of carrying them in the prompt.
- **Living skills** — reusable procedures with a contract (what it does, when it applies, what to verify), versions, quality evaluations and human-governed evolution.
- **The quality loop** — agent changes are applied only on top of a fresh green evaluation run: golden cases from production, human verdicts, a calibrated LLM judge.
- **Secure by default** — commands run in a Docker sandbox with no network and no privileges, consequential actions require human approval, secrets are never visible to the agent, with RBAC and an org structure on top.

## What the system is made of

| Component | What it does |
|---|---|
| **Agent Kernel** | The agent's durable runtime: model gateway, tools, sandbox, delegation, approvals, scheduling |
| **Journal** | Append-only event log; knowledge projection, hint selection, bitemporal queries |
| **Workspace (chat)** | Dialogue with the agent: streaming, attachments, approvals, amendments to running tasks |
| **Debugger UI** | Dashboard, run traces, the knowledge map, experience timeline, skills, artifacts, triggers, administration |
| **Control Plane** | Users, RBAC, org structure, tokens |

All of it starts with a single `docker compose up` — PostgreSQL, Temporal, Journal, Kernel, the UI, plus optional SearXNG and a headless browser for web research.

## Who it is for

- **Teams operating agents** — see what the agent did and why, answer "who changed what", find and fix recurring mistakes.
- **Teams growing agents** — turn experience into verified knowledge and skills, change agent definitions safely through the quality loop.
- **Organizations** — constrain agents with policies, keep data on your own infrastructure, manage access through the org structure.

## Where to look next

- [Quick start](quickstart.html) — bring the system up and run the first task.
- [Chat and runs](chat.html) — conversations, approvals, amendments.
- [Knowledge](knowledge.html) — how the agent gains and loses experience.
- The "In depth" note in each article links to the matching engineering document in the repository's `docs/`.
