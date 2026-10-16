---
order: 12
section: Administration
title: Reference
description: Environment variables, ports, UI pages and links to the in-depth documents.
---

# Reference

## Environment variables (essentials)

| Variable | Default | Description |
|---|---|---|
| `TEMPORALITY_MODEL_BASE_URL` / `_ID` / `_API_KEY` | — | The default provider (required) |
| `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | `16384` | Generation limit; leave head room for reasoning models |
| `TEMPORALITY_MODEL_ACTIVITY_TIMEOUT` | `20m` | Ceiling for one model activity |
| `TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT` | `2m` | First-token wait (adaptive, +60s per MB of prompt) |
| `TEMPORALITY_TOOL_ACTIVITY_TIMEOUT` | `16m` | Ceiling for a `run_tool` activity |
| `KERNEL_TOOL_MAX_SECONDS` | `900` | Ceiling for a single sandbox command |
| `KERNEL_SANDBOX_ROOT` | `/tmp` | Root of the persistent workspaces |
| `KERNEL_DOCKER_BINARY` | `docker` | Path to the docker CLI |
| `KERNEL_SECRET_MASTER_KEY` | — | Secret encryption key (`openssl rand -hex 32`) |
| `KERNEL_AUTH_TOKENS` / `JOURNAL_AUTH_TOKENS` | empty | Bearer tokens (`token:user:role:projects`; empty = auth off) |
| `KERNEL_RUN_QUOTAS` | empty | Daily run quotas: `project:limit`, `*` — default |
| `KERNEL_MODEL_PRICES` | empty | Token prices: `model:prompt:completion` |
| `KERNEL_INTAKE_MAX_PER_HOUR` | `10` | Per-sender limit for channel messages |
| `KERNEL_INTAKE_RETENTION_DAYS` | `30` | How long inbound bodies are kept |
| `KERNEL_SEARXNG_URL` | `http://searxng:8080` | Meta-search for `web_search` (empty = off) |
| `KERNEL_BROWSER_CDP_URL` | lightpanda | Headless browser for `browser` (empty = off) |
| `KERNEL_CHANNEL__MATRIX__*`, `__TELEGRAM__*`, `__MATTERMOST__*`, `__SLACKBOT__*` | — | Channel transports |

The full list with semantics is in the root `README.md` and `docs/runbook.md`.

## Ports

| Service | Port |
|---|---|
| UI (debugger) | `3000` |
| Journal API | `8080` |
| Agent Kernel API | `8090` |
| Temporal UI | `8233` |
| SearXNG | `127.0.0.1:8889` |

## UI pages

| Path | Section |
|---|---|
| `/dashboard` | Dashboard: health, models, projects, quality, sagas, sandboxes |
| `/workspace` | Agent chat |
| `/agents` | Runs and traces |
| `/knowledge` | Knowledge: the map, search, dossiers, contradictions |
| `/experience` | The experience timeline |
| `/storage` | Sandbox storage (files) |
| `/agent-config` · `/teams` | Agents and teams |
| `/skills` · `/artifacts` | Skills and artifacts |
| `/inbound` · `/triggers` · `/channels` | Inbound, triggers, channels |
| `/providers` · `/secrets` · `/sandbox` | Providers, secrets, sandboxes |
| `/users` · `/org` | Users and the org structure |

## Glossary

- **Run** — one agent execution over a task (a durable workflow).
- **Addendum** — a user message appended to a running run.
- **Amendment** — a channel message continuing an existing task.
- **Saga** — tracked long-running work across several runs and agents.
- **Knowledge** — a statement with provenance, a status and a history.
- **Hint** — a compact excerpt of experience fed to the agent.
- **Skill** — a versioned procedure with a contract and evaluations.
- **Artifact** — a reference document in the registry, loaded on demand.
- **Reconcile** — manually resolving an uncertain operation (happened / did not happen).
- **Judge** — the LLM model evaluating answers in the quality loop.

## Engineering documents

The architecture and internals are described in the repository's `docs/`:
`architecture.md` (overview), `event-model.md`, `knowledge-model.md`,
`agent-quality-loop.md`, `living-skills.md`, `agent-delegation.md`,
`inbound-messages.md`, `sandbox-security-matrix.md`,
`failure-reconciliation.md`, `runbook.md` (operations) and others.
