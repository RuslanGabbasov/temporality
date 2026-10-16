# Temporality

[Русский](README.md) · **English**

Temporality is an observability and experience accumulation system for AI agents.

An agent performs a task; Temporality records what it knew and did, where
that knowledge came from, how it was applied, what proved true or false, and
how the experience evolved over time. On each new run the system injects
relevant past experience in a compact form without bloating the context;
when needed, the agent can consult the original evidence.

```text
Agent Kernel ──events──▶ Journal ──projection──▶ Knowledge / Hints
     ▲                      │                         │
     └──────── hints ◀──────┴──── Experience Timeline ┘
```

## Components

| Component | Path | Purpose |
|---|---|---|
| Journal | `observation/`, `cmd/temporality-journal` | append-only events, knowledge projection, hint retrieval, bitemporal queries |
| Agent Kernel | `kernel/`, `cmd/agent-kernel` | durable agent harness on Temporal: model gateway, MCP, sandbox, approvals, delegation |
| Workspace | `debugger/src/Workspace.tsx` | agent chat: SSE streaming, reasoning, conversations, branching |
| Debugger | `debugger/` | UI for observing and analyzing agent experience |
| Control Plane | `controlplane/` | auth/RBAC, org structure and access, bearer tokens, project scoping |

### UI pages

| Path | Purpose |
|---|---|
| `/project` | Project overview — sagas and tasks with a budget (tokens, time, cost, approvals), recent runs |
| `/workspace` | Agent chat — SSE streaming, reasoning, Markdown, conversations |
| `/agent-config` | Agent management — CRUD, templates, full configuration |
| `/agents` | Run traces — event timeline, trajectory extraction, inline reconciliation of uncertain operations |
| `/knowledge` | Knowledge explorer — area map, faceted search, evidence dossiers, relation graph, bitemporal history, conflicts, research sessions, agent research replay |
| `/observability` | Observability — knowledge base overview (health, cuts, comparison) and experience patterns |
| `/experience` | Experience Timeline — memory lens, semantic zoom, retrospective audit |
| `/skills` | Living Skills — contract, versions, executions, memory, evolution, skill builder |
| `/artifacts` | Artifacts — reference document registry (regulations, instructions), skill linkage |
| `/mcp` | MCP servers — stdio / SSE / Streamable HTTP, binding to agents |
| `/secrets` | Secrets — store (builtin/env), grants to agents and providers |
| `/providers` | Model providers — OpenAI-compatible endpoints |
| `/users` | Users — RBAC, tokens, notification channels |
| `/channels` | Delivery channels — transport settings (Matrix, Telegram, Mattermost, Slack) |
| `/inbound` | Inbox — channel messages: intake pipeline, statuses, audit |
| `/triggers` | Triggers — cron, webhook, event listeners, execution identity |
| `/org` | Organization — org structure tree, node roles, policies, effective access |

Run, operation and question listings are global: the project acts as a filter,
not a partition (`docs/activity-scoping.md`).

## Getting started

```sh
cp .env.example .env   # configure TEMPORALITY_MODEL_* and optionally the sandbox
docker compose up --build -d
```

This starts PostgreSQL, the Journal (`:8080`), the debugger (`:3000`), the
Temporal dev server (UI on `:8233`), the Agent Kernel (`:8090`), SearXNG
(`127.0.0.1:8889`, the meta-search behind the agents' `web_search` tool;
disable it with an empty `KERNEL_SEARXNG_URL`) and the Lightpanda headless
browser behind the `browser` tool (disable it with an empty
`KERNEL_BROWSER_CDP_URL`).

A fresh install configures itself through the onboarding wizard: project →
admin user → model provider (the key is stored in secrets) → agent → sandbox
build. The main navigation stays hidden until the first project exists.

Stop the stack while keeping data:

```sh
make stack-stop
```

## Model configuration

The kernel calls the provider through an OpenAI-compatible Chat Completions API.

| Variable | Description | Default |
|---|---|---|
| `TEMPORALITY_MODEL_BASE_URL` | API root (with `/v1`) | — |
| `TEMPORALITY_MODEL_ID` | Model ID at the provider | — |
| `TEMPORALITY_MODEL_API_KEY` | Key (empty for Ollama) | — |
| `TEMPORALITY_MODEL_TEMPERATURE` | `0..2` | `0` |
| `TEMPORALITY_MODEL_TIMEOUT` | Go duration — total limit for one blocking attempt (streaming does not use it) | `180s` |
| `TEMPORALITY_MODEL_STREAM_IDLE_TIMEOUT` | Maximum silence between stream chunks; slow but alive generation is not interrupted | `90s` |
| `TEMPORALITY_MODEL_STREAM_NO_PROGRESS_TIMEOUT` | Maximum silence between chunks carrying generated tokens; guards against gateways that hold the connection open with empty pings (0 disables) | `4m` |
| `TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT` | Maximum wait for the first token: the provider accepted the request but stays silent (server-side queueing). The watchdog fails the attempt and the Temporal retry opens a fresh connection — the automatic version of a manual nudge. Adaptive budget: +60s per megabyte of prompt, so a legitimate large-context prefill is not cut down (0 disables) | `2m` |
| `TEMPORALITY_MODEL_ACTIVITY_TIMEOUT` | Ceiling of one `call_model` Temporal activity (shared by the streaming attempt and the blocking fallback with retries); up to 3 attempts within the overall budget (3× timeout + 5 minutes) | `20m` |
| `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | `64..1000000` | `16384` |
| `TEMPORALITY_MODEL_REASONING` | `off` / `exclude` / `low` / `medium` / `high` | — |

For reasoning models, CoT is counted within the same budget — set it with
headroom (e.g. `16384` or more), otherwise the answer may end up empty with
`finish_reason=length`. The default is already `16384`.

### Long runs on a laptop

When the host falls asleep mid-run, both the worker and Temporal freeze. On
wake the model call recovers automatically: the stream watchdog cuts the dead
connection and Temporal retries the activity with a fresh attempt (up to 3
attempts; a model call has no side effects and the prompt prefix is cached
via `prompt_cache_key`). Sleep longer than the overall budget still kills the
run. To keep the laptop awake during long agent runs (macOS), keep this in a
spare terminal:

```sh
caffeinate -dims
```

Examples:

```dotenv
# OpenAI
TEMPORALITY_MODEL_BASE_URL=https://api.openai.com/v1
TEMPORALITY_MODEL_ID=gpt-4.1-mini
TEMPORALITY_MODEL_API_KEY=your-key

# OpenRouter
TEMPORALITY_MODEL_BASE_URL=https://openrouter.ai/api/v1
TEMPORALITY_MODEL_ID=openai/gpt-4.1-mini
TEMPORALITY_MODEL_API_KEY=your-openrouter-key

# Local Ollama
TEMPORALITY_MODEL_BASE_URL=http://host.docker.internal:11434/v1
TEMPORALITY_MODEL_ID=llama3.2
TEMPORALITY_MODEL_API_KEY=
```

## Auth

Bearer tokens with RBAC and project scoping. Empty variables = auth disabled
(local development only).

Format: `token:subject:role:projects`, records separated by `;` or new
lines. Roles: `reader < writer < operator < admin`. Projects — `*` or a
comma-separated list. Tokens: `openssl rand -hex 24`.

```sh
# .env
JOURNAL_AUTH_TOKENS=token1:alice:reader:*;token2:bob:writer:lighthouse
KERNEL_AUTH_TOKENS=token1:alice:reader:*;token2:bob:writer:lighthouse
TEMPORALITY_API_TOKEN=token2   # kernel → journal (optional, see below)
```

When `TEMPORALITY_API_TOKEN` is not set (or the journal rejects it), the
kernel probes the journal at boot and auto-provisions a `temporality-kernel`
service principal (writer, all projects) in the shared database — the journal
picks it up on its next token reload. This keeps a clean install working with
zero manual tokens: after onboarding the journal switches auth on, and the
kernel already holds an accepted token. A deliberately open journal (no
users, no static tokens) stays open — provisioning only happens after an
explicit 401/403.

| Layer | reader | writer | operator | admin |
|---|---|---|---|---|
| journal | read, hints | ingest | + invalidation | everything |
| kernel | GET runs | POST runs | + approvals, reconcile | everything |

In the UI the token is entered via Login in the header (stored in
localStorage).

## Secrets

A secret is a first-class workspace resource (`docs/secrets-management.md`):
metadata in `workspace_secret`, the value envelope-encrypted with AES-256-GCM
in `workspace_secret_value`, decrypted only inside the kernel process. Agents
never see values: `secret://<name>` references in MCP server env/headers and
grants on the agent card are resolved at the execution boundary.

- `KERNEL_SECRET_MASTER_KEY` — 32 bytes hex/base64 (`openssl rand -hex 32`),
  `_FILE` suffix supported. Without it the kernel still starts, but
  builtin secrets fail to resolve.
- Providers: `builtin` (encrypted in the database) and `env` (the kernel
  process's `SECRET_<NAME>` variable — for local development).
- Secrets granted to an agent arrive in `run_command` as `SECRET_<NAME>`;
  the `secret_list` tool shows names and descriptions only.
- `secret.issued` / `secret.denied` events record grants; the journal
  additionally redacts well-known token patterns on ingest (ghp_*, AKIA*,
  xoxb-*, sk-*, Bearer, PRIVATE KEY) as the second line of defense.

## Notification channels

When an agent calls `ask_human`, the kernel delivers the question to the
recipient over their preferred channel (see
`docs/triggers-and-escalations.md` §6-7). Channels are part of the user
profile and are configured by each user in the user menu → "Notification
channels". The built-in channel is the in-app inbox (Runs → questions);
external transports are enabled with kernel variables:

```sh
# .env — canonical names (KERNEL_CHANNEL__<CHANNEL>__<FIELD>)
KERNEL_CHANNEL__MATRIX__HOMESERVER=https://matrix.org   # homeserver of the kernel bot
KERNEL_CHANNEL__MATRIX__ACCESS_TOKEN=syt_...            # bot token (member of user rooms)
KERNEL_CHANNEL__TELEGRAM__BOT_TOKEN=123456:ABC...       # bot token for the Bot API
KERNEL_UI_URL=http://localhost:3000                     # for links in notifications
KERNEL_CHANNEL__MATTERMOST__BASE_URL=https://mm.corp    # self-hosted Mattermost
KERNEL_CHANNEL__MATTERMOST__TOKEN=...                   # bot/personal token (REST v4 + WebSocket)

# Two-way push channels (webhooks arrive at the kernel; the path must be
# public through a reverse proxy):
KERNEL_CHANNEL__SLACKBOT__BOT_TOKEN=xoxb-...            # Slack app: bot token
KERNEL_CHANNEL__SLACKBOT__SIGNING_SECRET=...            # Events API signature (v0:ts:body HMAC-SHA256)
#   events: POST /v1/channels/slackbot/events
# Mattermost receives inbound over a WebSocket stream with the same token
# (direct messages included) — a separate webhook and OUTGOING_TOKEN are no
# longer needed

# Legacy names keep working as aliases (same fields):
# KERNEL_MATRIX_HOMESERVER, KERNEL_MATRIX_ACCESS_TOKEN,
# KERNEL_TELEGRAM_BOT_TOKEN, KERNEL_WEBHOOK_SECRET
```

The same settings are editable in the UI (Settings → Delivery channels)
without a restart: database values override env per field. Secrets never
come back from the API — only a set flag and a 4-character tail.

Channel address: for Matrix — room ID (`!room:matrix.org`), for Telegram —
chat ID. Delivery does not block execution: a transport failure is recorded
as a `notification.sent` event with status `failed`, and the question remains
available in the UI. Answers always happen in the app; external channels
only deliver the question.

## Inbound messages (intake)

A user message sent on their own initiative in a channel starts the intake
pipeline (classification → clarification → task), see `docs/inbound-messages.md`.
It is enabled per project and off by default: a project starts accepting
channel tasks only after the "Accept tasks from channels" toggle in the
project settings. On upgrade only the projects that already routed messages
stay enabled — a working channel does not die silently.

Additional constraints:

- **Org-scope policy**: `intake_mode = deny` in a node policy bans channel
tasks for the whole subtree; a child cannot loosen an ancestor's veto.
Enabling happens on the project only; an "allow" policy value does not
exist.
- **Sender limit**: `KERNEL_INTAKE_MAX_PER_HOUR` (default 10, 0 — off) —
exceeding it is rejected with a polite reply in the channel and an
`intake.rejected` event (reason `rate_limited`) instead of an intake run.
- **Body retention (PII)**: `KERNEL_INTAKE_RETENTION_DAYS` (default 30,
0 — keep forever) — past the deadline bodies and attachments are scrubbed,
the row survives as an audit-chain link (marked in the Inbox).
- **Audit**: `GET /v1/intake/audit?sender=<id>` (operator) — the chain
message → intake run → task run with outcomes, without message bodies.

## Agent Kernel

A durable Temporal workflow: model calls, approvals, `remember` proposals,
auto-captured verification observations, at-least-once outbox, MCP
(stdio / SSE / Streamable HTTP), Docker command sandbox.

Delegation: `delegate` — parallel subagents; `plan` — a DAG plan across
agents with dependencies and predicates (`docs/agent-delegation.md`).

Sagas: explicitly registered long-running efforts that outlive a single run
and cross agent boundaries. Tools `saga_register` / `saga_attach` /
`saga_list`; the binding lives at the task level, so chat follow-ups and
delegated children join the saga automatically. The project overview
(`/project`) shows sagas and tasks with a budget rollup (`docs/sagas.md`).

Web research: `web_fetch` (page → readability → markdown), `web_search`
(self-hosted SearXNG), `view_image` / `fetch_image` (images as vision blocks)
and `browser` — a headless browser over a CDP contract for JS-heavy pages
(`docs/web-research.md`).

Living skills: the `skill_search` / `skill_inspect` / `skill_validate` /
`skill_history` / `skill_executions` / `skill_memory` / `skill_propose`
tools (`docs/living-skills.md`).

Artifact registry: reference documents (regulations, approved instructions,
checklists) that skill manifests point at but never inline into the prompt —
the agent loads bodies on demand through `artifact_search` / `artifact_read`
in chunks, with `artifact.read` journal auditing; url-provider artifacts are
proxied and cached by the kernel itself (`docs/artifact-registry.md`).

Agent quality loop: definition proposals apply only on top of a fresh green
evaluation run (golden cases from production, HITL verdicts, an LLM judge
with calibration, a per-version quality dashboard) —
`docs/agent-quality-loop.md`.

Also: knowledge hints and trajectory-based extraction, triggers
(cron/webhook/listeners), human requests (`ask_human`) and org policies
(model/MCP/token/network/sandbox constraints).

When a provider hangs and a model call silently waits for its first token,
a manual nudge is available: `POST /v1/agent/runs/{runID}/nudge?project=…`
(writer) — the signal cancels the in-flight request and retries the turn
immediately (model calls are pure, the prompt prefix is stable via
`prompt_cache_key`, so a retry is safe). The intervention is journaled as a
`model.nudged` event.

The same scenario is automated by the first-token watchdog
(`TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT`, 2m by default): when the provider
stays silent past the budget, the attempt is failed without the blocking
fallback and the Temporal retry opens a fresh connection. Every recovery is
surfaced in the trace and the chat as a `model.attempt` marker (reason
`stream_timeout`) — long silence no longer looks like a hang.

### Sandbox

`KERNEL_SANDBOX_ROOT` + `KERNEL_SANDBOX_IMAGE` — an isolated container for
`run_command`: no network (or `bridge` when `network_access: true`),
read-only root, dropped capabilities, resource limits, bounded output.
A persistent per-project toolchain cache is mounted at `/cache`
(GOCACHE/GOMODCACHE) — it survives `git clean` and workspace re-creation.
Out of the box the root defaults to `/tmp` (zero setup, but volatile
across host reboots — set `TEMPORALITY_SANDBOX_ROOT` in `.env` for
persistence, see `.env.example`).

Native file tools `write_file` / `read_file` / `edit_file` run on the kernel
side directly over the workspace: no shell, no command argument limits,
atomic writes and symlink checks. Prefer them over heredocs via
`run_command` for file edits.

The image is pinned by sha256 digest and started with `--pull=never`
(`docs/sandbox-security-matrix.md`), so the local Docker must contain exactly
the image the digest points to. After editing `sandbox/Dockerfile` or
pruning images (`docker system prune`), run `./sandbox/build.sh` — it
rebuilds the image, updates `KERNEL_SANDBOX_IMAGE` in `.env` and recreates
agent-kernel. Otherwise `run_command` fails with exit 125 "No such image".

The kernel reaches the Docker socket automatically: the container starts
as root, the entrypoint detects the socket's group (`root:docker` on Linux
hosts, `root:root` on Docker Desktop / OrbStack) and drops to the
unprivileged user with that group attached — nothing to configure.
`TEMPORALITY_DOCKER_GID` in `.env` remains for exotic setups (overrides the
detection). If building the sandbox from onboarding fails with permission
denied, recreate the container with the current image:
`docker compose up -d --build agent-kernel`.

### MCP

The primary way is the MCP server registry in the workspace (`/mcp`): stdio,
SSE or Streamable HTTP transport, binding servers to agents and a tools
panel in the agent card (including availability toggles for MCP-provided
tools).

The `KERNEL_MCP_COMMAND`, `KERNEL_MCP_ARGS` (JSON array), `KERNEL_MCP_ALLOW`
(tools list), `KERNEL_MCP_APPROVAL` (subset requiring human approval)
variables are a legacy option for running the kernel without a workspace.

### Failure reconciliation

`tool.failed` carries the effect semantics: `effect=none` (failure before
execution) vs `effect=uncertain` (failure at the execution boundary).
Read-only tools (read_file, search, grep) automatically get `effect=none`.

Unreconciled operations surface inline in the run trace on `/agents` — verdict cards at failed steps, run-list markers and a filter for affected runs.

```sh
curl 'http://localhost:8090/v1/agent/operations?project=repo-a' -H "Authorization: Bearer $TOKEN"
curl -X POST http://localhost:8090/v1/agent/operations/reconcile \
  -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"project":"repo-a","run_id":"...","operation_id":"...","effect":"occurred","note":"verified","actor_id":"ops","started_event_id":"..."}'
```

## Trajectory & Experience

Trajectory extraction builds structured data from the event stream: turns,
tool calls, knowledge events, repeating patterns. The experience projection
aggregates trajectories across runs: tool patterns, knowledge lifecycle,
scopes, success rates.

| Endpoint | Description |
|---|---|
| `GET /v1/workspace/runs/{id}/trajectory` | Per-run extraction |
| `GET /v1/workspace/projects/{id}/projection` | Cross-run aggregation |
| `GET /v1/workspace/runs/compare?a=X&b=Y` | Side-by-side diff |
| `GET /v1/workspace/runs/{id}/artifacts` | Reusable tool sequences |
| `GET /v1/workspace/knowledge/{id}/chain` | Activation chain |
| `GET /v1/workspace/projects/{id}/chains` | All chains |
| `GET /v1/workspace/runs/{id}/stream` | SSE event stream; `?tail=1` — skip the full history replay: the last ~50 events plus only new ones (for reconnecting to a long run) |
| `GET /v1/workspace/runs/{id}/stream` | + model.text_delta, model.reasoning |
| `GET /v1/agent/cost/task?project=P&task=T` | Task budget: every run (chat follow-ups + delegation tree) with summed time/tokens/cost |
| `GET /v1/agent/cost/project-budget?project=P` | Project budget: totals plus per-saga and per-task rows (time/tokens/cost/approvals) |

## Workspace

Agent chat over SSE streaming. Each message → task + run. Follow-up messages
reuse the same task (PATCH prompt). Conversation history is passed as
context. Agent selector when creating a chat. Branching — fork from any
point.

Features:
- SSE streaming: model.text_delta, model.reasoning, tool events
- Markdown rendering for replies (including subagent replies)
- Reasoning/thinking block (collapsible)
- Conversation persistence (localStorage)
- Agent templates (Coder, Reviewer, Researcher, DevOps, QA)
- Project settings (default agent, default model)
- Delegation tree and DAG plan: subagent progress right in the chat
- Run titles: the task title (the chat's first message line) → a short name via a lightweight model call → a heuristic first line of the prompt; a slow provider never leaves a run nameless

## Control Plane

- **Auth**: bearer tokens `token:subject:role:projects`
- **Org structure**: org tree, inherited top-down resource visibility, node roles, explicit project membership, execution identities, policies — `docs/org-structure.md`
- **Quotas**: `KERNEL_RUN_QUOTAS="*:50,project:200"` — runs/day limit
- **Secrets**: the `<VAR>_FILE` convention — the value is read from a file
- **Providers**: OpenAI-compatible model endpoints, API key masking
- **Users**: CRUD with token generation, RBAC roles

## Experience Timeline

`/experience` — a visualization of the agent's memory life:

- **Runs** horizontally, **knowledge lanes** vertically
- **Lifecycle**: appeared → recalled → injected → reused → validated → contradicted → invalidated
- **Memory lens filters**: strength, recency, activated, cross-scope, terminal state
- **Semantic zoom**: scope lanes → knowledge rows → episode detail
- **Retrospective audit panel**: formation evidence, activation chain, lineage
- **Conflicts lens**: supersession arrows (K82 → K73)
- **Population lane**: active memory count over time
- **Shareable URLs**: full investigation state in the URL

## Development

```sh
make fmt          # gofmt
make test         # go test ./...
make race         # go test -race ./...
make build        # bin/temporality-journal + bin/agent-kernel
make debugger-test
make debugger-build
```

The debugger dev server (`npm --prefix debugger run dev`) proxies `/api` to
`localhost:8080` (journal) and `/kernel-api` to `localhost:8090`.

## Schemas and contracts

- Event schema: [`schemas/temporality.event.v1.schema.json`](schemas/temporality.event.v1.schema.json)
- Contracts: [`docs/temporality-contract.md`](docs/temporality-contract.md)
- Knowledge model: [`docs/knowledge-model.md`](docs/knowledge-model.md)
- Event model: [`docs/event-model.md`](docs/event-model.md)
- Architecture: [`docs/architecture.md`](docs/architecture.md)
- Secrets management: [`docs/secrets-management.md`](docs/secrets-management.md)
- Agent quality loop: [`docs/agent-quality-loop.md`](docs/agent-quality-loop.md)
- Artifact registry: [`docs/artifact-registry.md`](docs/artifact-registry.md)
- Sagas: [`docs/sagas.md`](docs/sagas.md)
- Web research: [`docs/web-research.md`](docs/web-research.md)
- Activity scoping: [`docs/activity-scoping.md`](docs/activity-scoping.md)
- Roadmap: [`docs/ROADMAP.md`](docs/ROADMAP.md)
