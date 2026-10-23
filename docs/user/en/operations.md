---
order: 13
section: Operations
title: Harness Operations
description: How to deploy, configure and operate Temporality as a working harness — the stack, upgrades, providers, authentication, knowledge, skills, sandboxes, observability, backups and troubleshooting.
---

# Harness Operations

This guide is for whoever is responsible for a working deployment: rolling it
out, upgrading it, configuring providers and access, observability, and
troubleshooting typical failures. Day-to-day user work is covered in the
neighboring sections; exact variable values — in the [Reference](reference.html).

## 1. The stack: what runs where and where state lives

Compose brings up seven services:

| Service | Image | Role |
|---|---|---|
| `postgres` | pgvector/pgvector:pg17 | The only persistent store: events, knowledge, users, configs, secrets, reviews |
| `journal` | temporality/journal | Event store API: event reads/search, SSE streams of runs |
| `agent-kernel` | temporality/agent-kernel | The kernel: a Temporal worker (long-lived runs), the `:8090` API, sandboxes, MCP, channels |
| `debugger` | temporality/debugger | Web UI `:3000`, a proxy to the kernel and journal |
| `temporal` | temporalio/auto-setup | The durable execution engine (its own store) |
| `searxng` | searxng | Meta-search for the `web_search` tool |
| `lightpanda` | lightpanda/browser | Headless browser (CDP) for the `browser` tool |

**Where state lives** — this matters for backups and migrations:

- **Postgres** — the source of truth. Observation events (`observation_events`),
  knowledge and its history, users/sessions/IdP configs, secrets (encrypted
  with `KERNEL_SECRET_MASTER_KEY`), artifact reviews, quotas. The kernel writes
  to it (via outbox → journal); everyone reads from it.
- **`KERNEL_SANDBOX_ROOT`** — persistent sandbox workspaces (the files the
  agent edits between runs). Defaults to `/srv/temporality/sandbox` in compose.
- **Temporal** — the state of active workflows. Completed runs live in Postgres
  as events; Temporal is needed to resume unfinished ones.

Nothing important lives inside the containers: they can be recreated freely.

## 2. Deployment and upgrades

### Moving skills and agents between stands

The behavioral configuration set transfers in one JSON file in the internal
`temporality.transfer/v1` format:

```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  "https://stand-a.example/kernel-api/v1/workspace/export?type=all" -o transfer.json
curl -H "Authorization: Bearer $ADMIN_TOKEN" -F file=@transfer.json \
  "https://stand-b.example/kernel-api/v1/workspace/import?mode=merge"
```

- `type=skills|agents|all`, `ids=ci-fix,dev-1` select what to export.
- The import creates what is missing and never silently overwrites: a skill
  with changes lands as a **draft** version (a human applies it); an agent is
  skipped under the default `mode=merge` and updated only with
  `mode=overwrite`.
- The response carries a per-item report (created/updated/skipped/errors).
  The Skills and Agents pages expose the same as «Export…/Import…» buttons.
- Transferred inside: manifest, body, versions with provenance (skills);
  model, prompts, definition, tools, secret names, sandbox (agents).
  Stand-specific references — org unit binding, provider id — are preserved
  verbatim: on the target stand make sure a provider with that id and secrets
  with those names exist.

Upgrading the stack:

```bash
TEMPORALITY_VERSION=2026.2.66 docker compose up -d
```

The upgrade procedure:

1. Read the release notes (the tag annotation) — migrations and breaking
   changes are written there.
2. `docker compose pull journal debugger agent-kernel`
3. `docker compose up -d` — Postgres migrations apply automatically at kernel
   startup (`migrations/workspace`, quota migrations); the migration log is
   idempotent.
4. Check health: `docker compose ps` (all healthy), the `/dashboard` page, one
   test run.

Rollback — bring up the previous `TEMPORALITY_VERSION`. Schema migrations
**do not roll back**: take a dump before major upgrades (§9). Building locally
from sources instead of GHCR:

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build agent-kernel debugger
```

(`docker-compose.dev.yml` switches the images to builds from the repository;
it overrides only the listed services.) A common hiccup: on the first
simultaneous build of the two targets, buildx sometimes trips over a cache
lock — rerunning the build goes through.

## 3. Providers and models

Three default provider variables are the minimum to get going:

```bash
TEMPORALITY_MODEL_BASE_URL=https://api.provider.com/v1
TEMPORALITY_MODEL_ID=mimo-v2.6-flash
TEMPORALITY_MODEL_API_KEY=...
```

Full setup lives on the `/providers` page: several providers, model catalogs,
assigning models to roles.

### Model roles and where each one lands

- **chat** — the agents' main model (dialogue, tools, plans).
- **embedding** — the knowledge semantic layer (§6).
- **asr** — audio transcription; it must not participate in chat roles.

The model catalog is synced from the provider on a schedule
(`KERNEL_PROVIDER_MODELS_REFRESH`, 24h by default, `0` disables the cycle)
plus a manual refresh and a probe check in the UI.

**The judge** — a separate chat model for evaluations. It is used more widely
than it seems: the quality loop, knowledge area normalization, supervisor
passes and cases, the `worth_skill` verdict when mining skills, model
selection in the draft wizards (agent/skill/team):
env model → judge model → the first chat candidate in the catalog. Non-chat
catalog entries (ASR, embedding, image) are filtered out of the candidates —
if a wizard "picked the first ASR model it saw", refresh the catalog and
assign the roles explicitly.

### Streaming and timeouts

| Variable | Default | Meaning |
|---|---|---|
| `TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT` | `2m` | Waiting for the first token; adaptively +60s per MB of prompt |
| `TEMPORALITY_MODEL_STREAM_IDLE_TIMEOUT` | `90s` | Silence in the stream between tokens |
| `TEMPORALITY_MODEL_STREAM_NO_PROGRESS_TIMEOUT` | `4m` | The stream is alive, but overall progress has stalled |
| `TEMPORALITY_MODEL_TIMEOUT` | `180s` | The base HTTP request timeout |
| `TEMPORALITY_MODEL_ACTIVITY_TIMEOUT` | `20m` | Ceiling for one model activity |
| `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | `16384` (code) / `1024` (compose) | Generation limit. The compose stack deliberately sets 1024 — raise it consciously |
| `TEMPORALITY_MODEL_TEMPERATURE` / `_REASONING` | — | Overrides for request parameters |

Buffering gateways (they return the whole response as one chunk) are not a
malfunction. In the trace this shows up as the "provider does not stream
(a single chunk)" verdict on long calls; stream retries follow a ladder with a
growing interval ("↻ stream retry after N s"). The "token channel did not
connect (transport, not model)" diagnostic means an SSE transport problem on
the way to the provider, not a model choice. A hung call can be pushed
manually — the `↻` button on the live model-call line cancels the request and
restarts the turn.

## 4. Authentication and access

Four modes, from simple to enterprise:

1. **No authentication** — `KERNEL_AUTH_TOKENS` empty. For local experiments
   only.
2. **Bearer tokens** — `KERNEL_AUTH_TOKENS=token:user:role:projects,...`. For
   service agents and scripts.
3. **Password + sessions** — the main UI mode. Login is password-first, the
   "Password | API token" form. Sessions: `KERNEL_SESSION_TTL` (sliding, 12h),
   `KERNEL_SESSION_ABSOLUTE_TTL` (7d); `KERNEL_COOKIE_SECURE=true` behind
   HTTPS.
4. **OIDC SSO** — federation (Keycloak/Entra/Authentik), JIT provisioning,
   group-to-role mappings, the `deny` role = "login succeeds, access denied"
   until a manual decision. The config lives on `/users` (admin-only); every
   change writes an audit event.

Self-service: a user changes their password via the account menu
(`POST /v1/auth/password`). An account with a password confirms the current
one; an SSO account without a local password sets its first one with an empty
current field — minimum 8 characters, the bearer token survives the change.
Break-glass: with SSO enabled, password login remains for the admin only (IdP
unavailability does not block operations).

Project scoping: a role plus a project list in the token; for SSO — claim
mappings; `*` means all projects. The org structure (`/org`) additionally
restricts visibility by department.

## 5. Secrets

- `KERNEL_SECRET_MASTER_KEY` (`openssl rand -hex 32`) — the key that encrypts
  secrets in Postgres. **Losing the key means losing every secret.** Keep it
  off the deployment; rotation is only possible with re-encryption.
- Secrets are created in `/secrets` and referenced from configs as
  `secret://…`; the value is resolved at the point of use.
- `KERNEL_SECRET_CACHE_TTL_SECONDS` — how long a resolved value lives in the
  kernel's memory.
- Issuing a secret to a run is the `secret.issued` event in the trace (shown
  explicitly — an audit fact, not noise).

## 6. Knowledge, hints and embeddings

The semantic layer (knowledge similarity, hints drawn from similar experience,
conflict prediction, semantic search) requires an embeddings model:

```bash
KERNEL_EMBEDDINGS_MODEL=intfloat/multilingual-e5-small
```

The model loads in-process (rembed) from Hugging Face. If it fails to load,
the kernel keeps working, but with a "not all tools are configured" banner and
without semantics — retries follow a growing interval; check reachability of
huggingface.co. The model cache is on disk; for ownership problems see the log
(there is a fallback to the OS user).

Operating knowledge:

- Lifecycle: `proposed → verified → (challenged/invalidated/superseded)`.
  Verification is manual or by the judge; unverified knowledge never reaches
  hints.
- Challenge/invalidate straight from the run trace (⚑ on the event carrying
  knowledge) and from the dossier.
- Priming: before a turn the agent receives hints drawn from similar
  experience (`hint.query` → `memory.read`); usage facts (`hint.used` /
  ignored) are visible in the trace's knowledge group — they are how you
  evaluate how useful the experience was.
- Knowledge area normalization is performed by the judge; the area map is
  `/knowledge`.

## 7. Skills and artifacts

**Skills** — versioned procedures with a contract. Creation paths: describing
in the UI, import, manual, and **mining** — the supervisor looks for repeated
tool-call trigrams across runs, a deterministic gate filters out read-only
sequences, the judge issues a `worth_skill` verdict with a rationale; the
candidate lands in the queue and is pre-filled as a draft on `/skills`.
Versions: a draft is `0.x`, applying/rejecting revisions is a human decision;
the skill list shows `draft` and "↺ N" badges — pending draft revisions. Do
not let unapplied revisions pile up: go through the queue by badge.

**Artifacts** — reference documents agents load on demand. Statuses:
`draft / in_review / rejected / approved`. The review cycle: submit
(idempotent per version) → reviewers are notified → the decision is
**human-only** (self-approval is forbidden at the API level) →
approved/rejected. Reviewers: the `all` mode (everyone must decide) or `any`
(one is enough). The per-run submission quota is `ARTIFACT_SUBMIT_PER_DAY`.
Artifact bodies render markdown with mermaid diagrams (typical LLM errors in
erDiagram are fixed automatically).

## 8. Agents, teams and the plan DAG

- **Agent** (`/agent-config`): model, system prompt, tools and their
  approvals, sandbox. `max_turns` caps the run's length — for child runs it is
  set from the task.
- **Team** (`/teams`): a pipeline of lead → executors → QA. The team manifest
  pins the roles and limits.
- **Plan** — a DAG of tasks with dependencies. Key task fields: `on_failure`
  (`continue | stop | abort` — what happens to the wave when a task fails),
  `mode: wave` (staged execution in ready batches instead of full DAG
  ordering), `max_turns`. Review tasks work as gates: the next stage does not
  start without a verdict.
- **Supervisor** — periodic triage of failed runs
  (`KERNEL_SUPERVISOR_TRIAGE_INTERVAL`, 2m; a value below the minimum is
  treated as the default — there is no off switch). It proposes restarts and
  cases; contested operations wait for a human reconcile decision in the run
  panel.

## 9. Sandboxes, tools and limits

- **`KERNEL_SANDBOX_ROOT`** — the root of the workspaces. The code default is
  `./.sandbox`, in compose — `/srv/temporality/sandbox`. Do not use `/tmp`:
  tmpfs cleanup on a node reboot will destroy the agents' workspaces.
- `KERNEL_SANDBOX_IMAGE` — the sandbox image; `TEMPORALITY_DOCKER_GID` — the
  gid of the docker group on the host (the socket). `KERNEL_SANDBOX_PIDS`,
  `KERNEL_SANDBOX_SCRATCH_SIZE` — container limits.
- `KERNEL_TOOL_MAX_SECONDS` (900) — the ceiling for one command;
  `TEMPORALITY_TOOL_ACTIVITY_TIMEOUT` (16m) — the ceiling for a `run_tool`
  activity.
- Web: `KERNEL_SEARXNG_URL` (search), `KERNEL_WEB_SEARCH_PER_MIN` /
  `KERNEL_BROWSER_PER_MIN` (rate limits), `KERNEL_WEB_FETCH_ALLOW_PRIVATE`
  (private networks are forbidden by default — SSRF protection; enable it
  deliberately), `KERNEL_BROWSER_CDP_URL` (lightpanda; empty = browser off).
- **MCP**: `KERNEL_MCP_COMMAND/_ARGS` — an external MCP server, `_ALLOW` — the
  allowed tools, `_APPROVAL` — require approval.
- Run quotas: `KERNEL_RUN_QUOTAS` (`project:limit`, `*` — the default);
  exceeding one = 429. Prices for cost reporting: `KERNEL_MODEL_PRICES`
  (`model:prompt:completion`).

## 10. Operational observability

- **The run trace** (`/agents`) — a tree of turns, newest first; a turn
  expands in full: tool calls, approvals, answers. The window is 50 events;
  older ones lazy-load on scroll. The live model-call line shows
  elapsed/tokens/speed and the channel state; `↻` pushes a hung call. A
  running `run_command` has a live console tail right in its line (a 2000-line
  / 256 KB buffer, kept 6h after activity; after eviction — fallback to the
  event journal, ≤1 KB).
- **The dashboard** (`/dashboard`) — health, models, projects, quality;
  `/supervisor` — the triage queue; `/mcp` — the MCP server state.
- **The event bus**: the kernel writes through the outbox
  (`kernel_event_outbox`) into the journal; the UI listens on SSE + polls
  every 10s as a safety net. Kernel→journal authentication is
  `TEMPORALITY_API_TOKEN` (provisioned automatically).
- UI lag usually = journal stream lag: check restarts of
  `temporality-journal-1` and the reconnection backoff.

## 11. Backup and restore

The minimum — nightly:

```bash
docker exec temporality-postgres-1 pg_dump -U temporality temporality | gzip > backup-$(date +%F).sql.gz
tar -C /srv/temporality/sandbox -czf sandbox-$(date +%F).tgz .
```

Restore: bring up an empty stack of the same version → restore the dump → put
back the sandbox root → start the kernel. Active (unfinished) runs continue
after a restore only if the Temporal DB was restored too — for most cases it
is acceptable to accept that unfinished runs die and get restarted. Verify
restores periodically on a separate deployment — a backup that has never been
restored is not a backup.

## 12. Troubleshooting typical failures

| Symptom | Where to look | What to do |
|---|---|---|
| A run "hangs" on a model call | the trace's live line: waiting for the first token | if "the token channel did not connect" — transport to the provider (SSE/proxy), not the model; wait for the retry ladder or push `↻` |
| "Provider does not stream (a single chunk)" | `model.completed` details | a gateway buffers the response; not an error — pick another provider/endpoint for streaming |
| Answers get cut off mid-way | `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | raise the limit (the compose default of 1024 is small for large turns) |
| A command's console shows "Output unavailable: 404" | the console buffer | the buffer was evicted (6h idle / kernel restart); output ≤1 KB is recovered from the event journal |
| The "not all tools are configured: embeddings" banner | the kernel log | reachability of huggingface.co, correctness of `KERNEL_EMBEDDINGS_MODEL`; retries are automatic |
| A run failed and nothing picks it up | `/supervisor` | check `KERNEL_SUPERVISOR_TRIAGE_INTERVAL`; contested operations — reconcile in the run panel |
| 429 on launches | quotas | `KERNEL_RUN_QUOTAS` — raise the project's limit or the `*` default |
| A sandbox does not start | the kernel log, `docker compose ps` | the docker socket gid (`TEMPORALITY_DOCKER_GID`), presence of `KERNEL_SANDBOX_IMAGE` |
| The UI does not see everything (projects) | scoping | the token's role+projects / SSO mappings / org departments |

Practice: the trace first (it shows both the transport and the retry
verdicts), then the kernel logs, then the journal. Most "hangs" are transport
to the provider, and the trace names it outright.

## 13. Change discipline

- Everything that changes behavior ships as a `v2026.2.N` tag with an
  annotation — read it before upgrading.
- Keep the deployment config (`.env`, compose overlays) in version control;
  secrets only as `secret://` references.
- The DB schema only migrates forward; between versions with major migrations
  take a dump.
- Audit events (logins, secret issuances, IdP config changes) are part of
  operations: they are searchable in the journal like any other events.
