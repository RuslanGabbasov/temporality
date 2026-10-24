---
order: 12
section: Reference
title: Reference
description: Environment variables, ports, UI pages and links to the in-depth documents.
---

# Reference

## Environment variables (essentials)

| Variable | Default | Description |
|---|---|---|
| `TEMPORALITY_MODEL_BASE_URL` / `_ID` / `_API_KEY` | — | The default provider (required) |
| `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | `16384` (code) / `1024` (compose) | Generation limit; leave head room for reasoning models. The compose stack defaults to 1024 |
| `TEMPORALITY_MODEL_TIMEOUT` | `180s` | Base HTTP request timeout for the provider |
| `TEMPORALITY_MODEL_STREAM_IDLE_TIMEOUT` | `90s` | Silence in the stream between tokens |
| `TEMPORALITY_MODEL_STREAM_NO_PROGRESS_TIMEOUT` | `4m` | The stream is alive, but overall progress has stalled |
| `TEMPORALITY_MODEL_TEMPERATURE` / `_REASONING` | — | Overrides for request parameters |
| `TEMPORALITY_MODEL_ACTIVITY_TIMEOUT` | `20m` | Ceiling for one model activity |
| `TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT` | `2m` | First-token wait (adaptive, +60s per MB of prompt) |
| `TEMPORALITY_TOOL_ACTIVITY_TIMEOUT` | `16m` | Ceiling for a `run_tool` activity |
| `KERNEL_TOOL_MAX_SECONDS` | `900` | Ceiling for a single sandbox command |
| `KERNEL_EMBEDDINGS_MODEL` | `intfloat/multilingual-e5-small` | Model of the knowledge semantic layer (in-process, rembed) |
| `KERNEL_EMBEDDING_WORKERS` | — | Parallelism of background embedding computations |
| `KERNEL_PROVIDER_MODELS_REFRESH` | `24h` | Background refresh cycle of the provider model catalog (`0` — disable) |
| `KERNEL_SUPERVISOR_TRIAGE_INTERVAL` | `2m` | How often failed runs are triaged (values outside the valid range fall back to the default — there is no off switch) |
| `KERNEL_SANDBOX_ROOT` | `./.sandbox` (code) / `/srv/temporality/sandbox` (compose) | Root of the persistent workspaces. Do not use `/tmp` — cleanup on node reboot will destroy the workspaces |
| `KERNEL_SANDBOX_PIDS` / `KERNEL_SANDBOX_SCRATCH_SIZE` | — | Sandbox container limits: processes and scratch size |
| `KERNEL_DOCKER_BINARY` | `docker` | Path to the docker CLI |
| `KERNEL_SESSION_TTL` | `12h` | Sliding TTL of a browser session |
| `KERNEL_SESSION_ABSOLUTE_TTL` | `168h` | Absolute ceiling for a session |
| `KERNEL_COOKIE_SECURE` | `false` | Secure flag on the session cookie (enable it behind HTTPS) |
| `KERNEL_WEB_SEARCH_PER_MIN` / `KERNEL_BROWSER_PER_MIN` | `30` / `60` | Rate limits for search and the browser |
| `KERNEL_WEB_FETCH_ALLOW_PRIVATE` | `false` | Allow `web_fetch` into private networks (SSRF protection; enable deliberately) |
| `KERNEL_SECRET_CACHE_TTL_SECONDS` | — | How long a resolved secret value lives in the kernel's memory |
| `KERNEL_MCP_COMMAND` / `_ARGS` / `_ALLOW` / `_APPROVAL` / `_SERVER_NAME` | — | External MCP server: command, arguments, allowed tools, approval |
| `KERNEL_STORAGE_DEFAULT_QUOTA_BYTES` / `KERNEL_STORAGE_SWEEPER` | — | Sandbox storage quotas and sweeping |
| `TEMPORALITY_API_TOKEN` | auto-provisioned | Bearer token kernel → journal |
| `TEMPORALITY_OPERATIONS_WINDOW` | `7d` | Window for operational rollups |
| `KERNEL_UI_URL` | — | Base URL of the UI (emails/links) |
| `ARTIFACT_SUBMIT_PER_DAY` | `10` | Daily quota of artifact review submissions per run |
| `ARTIFACT_REVIEW_TIMEOUT` | `72h` | Artifact review deadline: one reviewer reminder, escalation to the unit head after a second timeout; `0` disables the sweep |
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
| `/supervisor` | Supervisor queue: triage of failed runs, cases, skill candidates |
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
| `/mcp` | External MCP server status |

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
`failure-model.md`, `failure-reconciliation.md`, `resilience-and-scale.md`
(fault tolerance and scale bounds: replay, workflow history, Continue-As-New),
`runbook.md` (operations) and others.
