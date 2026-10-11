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
| `/workspace` | Agent chat — SSE streaming, reasoning, Markdown, conversations |
| `/agent-config` | Agent management — CRUD, templates, full configuration |
| `/agents` | Run traces — event timeline, trajectory extraction |
| `/operations` | Uncertain operations — batch reconcile, verdict buttons |
| `/observability` | Knowledge — lifecycle, activation chains, patterns, projection |
| `/experience` | Experience Timeline — memory lens, semantic zoom, retrospective audit |
| `/skills` | Living Skills — contract, versions, executions, memory, evolution, skill builder |
| `/mcp` | MCP servers — stdio / SSE / Streamable HTTP, binding to agents |
| `/providers` | Model providers — OpenAI-compatible endpoints |
| `/users` | Users — RBAC, tokens, notification channels |
| `/triggers` | Triggers — cron, webhook, event listeners, execution identity |
| `/org` | Organization — org structure tree, node roles, policies, effective access |

## Getting started

```sh
cp .env.example .env   # configure TEMPORALITY_MODEL_* and optionally the sandbox
docker compose up --build -d
```

This starts PostgreSQL, the Journal (`:8080`), the debugger (`:3000`), the
Temporal dev server (UI on `:8233`), the Agent Kernel (`:8090`) and SearXNG
(`127.0.0.1:8889`, the meta-search behind the agents' `web_search` tool; disable
it with an empty `KERNEL_SEARXNG_URL`).

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
| `TEMPORALITY_MODEL_ACTIVITY_TIMEOUT` | Ceiling of one `call_model` Temporal activity (shared by the streaming attempt and the blocking fallback with retries) | `20m` |
| `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | `64..1000000` | `16384` |
| `TEMPORALITY_MODEL_REASONING` | `off` / `exclude` / `low` / `medium` / `high` | — |

For reasoning models, CoT is counted within the same budget — set it with
headroom (e.g. `16384` or more), otherwise the answer may end up empty with
`finish_reason=length`. The default is already `16384`.

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
TEMPORALITY_API_TOKEN=token2   # kernel → journal
```

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
# .env
KERNEL_MATRIX_HOMESERVER=https://matrix.org       # homeserver of the kernel bot
KERNEL_MATRIX_ACCESS_TOKEN=syt_...                # bot token (member of user rooms)
KERNEL_TELEGRAM_BOT_TOKEN=123456:ABC...           # bot token for the Bot API
KERNEL_UI_URL=http://localhost:3000               # for links in notifications
```

Channel address: for Matrix — room ID (`!room:matrix.org`), for Telegram —
chat ID. Delivery does not block execution: a transport failure is recorded
as a `notification.sent` event with status `failed`, and the question remains
available in the UI. Answers always happen in the app; external channels
only deliver the question.

## Agent Kernel

A durable Temporal workflow: model calls, approvals, `remember` proposals,
auto-captured verification observations, at-least-once outbox, MCP
(stdio / SSE / Streamable HTTP), Docker command sandbox.

Delegation: `delegate` — parallel subagents; `plan` — a DAG plan across
agents with dependencies and predicates (`docs/agent-delegation.md`).

Living skills: the `skill_search` / `skill_inspect` / `skill_validate` /
`skill_history` / `skill_executions` / `skill_memory` / `skill_propose`
tools (`docs/living-skills.md`).

Also: knowledge hints and trajectory-based extraction, triggers
(cron/webhook/listeners), human requests (`ask_human`) and org policies
(model/MCP/token/network/sandbox constraints).

### Sandbox

`KERNEL_SANDBOX_ROOT` + `KERNEL_SANDBOX_IMAGE` — an isolated container for
`run_command`: no network (or `bridge` when `network_access: true`),
read-only root, dropped capabilities, resource limits, bounded output.
A persistent per-project toolchain cache is mounted at `/cache`
(GOCACHE/GOMODCACHE) — it survives `git clean` and workspace re-creation.

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

Unreconciled operations are visible on `/operations` with batch reconcile.

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
| `GET /v1/workspace/runs/{id}/stream` | SSE event stream |
| `GET /v1/workspace/runs/{id}/stream` | + model.text_delta, model.reasoning |
| `GET /v1/agent/cost/task?project=P&task=T` | Task budget: every run (chat follow-ups + delegation tree) with summed time/tokens/cost |

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
- Auto-generated run titles — a short name is generated by a separate lightweight model call

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
- Roadmap: [`docs/ROADMAP.md`](docs/ROADMAP.md)
