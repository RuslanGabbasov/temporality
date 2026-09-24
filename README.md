# Temporality

Reference implementation of **Frame Runtime Protocol (FRP) v0.3**.

## Status

Implemented milestones span **M0 — Event Log + replay** through **M15 — Agent bootstrap / first contact** (see [`ROADMAP.md`](ROADMAP.md)):

- versioned FRP `Event` protocol object and JSON Schema;
- append-only PostgreSQL event store;
- deterministic replay ordered by valid time, transaction time, and event ID;
- versioned deliberate/ambient attention with stable scoring and hysteresis;
- replay boundaries by episode, branch, and `as_of`;
- versioned replay manifests included in deterministic digests;
- HTTP/JSON API with opaque cursor pagination;
- Prometheus-compatible runtime, render, and attention metrics;
- in-memory adapter for deterministic tests;
- versioned Claims and evidence relations;
- atomic Event + Claim + relations persistence.

See [`ROADMAP.md`](ROADMAP.md) for subsequent milestones.

The first-party durable Agent Kernel is being added incrementally. Its target architecture, event contract, knowledge model, failure behavior, OSS research and ADRs are documented in [`docs/architecture.md`](docs/architecture.md), [`docs/temporality-contract.md`](docs/temporality-contract.md), [`docs/knowledge-model.md`](docs/knowledge-model.md), [`docs/failure-model.md`](docs/failure-model.md), [`docs/oss-research.md`](docs/oss-research.md) and [`docs/decisions/`](docs/decisions/README.md). The universal observation API below remains usable directly by any external harness.

Phase 2 validation is tracked in [`docs/validation-report.md`](docs/validation-report.md), with the approval binding, event audit, side-effect reconciliation and semantic replay contracts in the linked documents. The deliberately failing code-change fixture is [`examples/code-change`](examples/code-change). Its baseline failure is intentional; a successful live Lead/Coder/Reviewer/QA acceptance run is still required before calling Phase 2 complete.

## Run the complete stack

```sh
docker compose up --build -d
```

Open the Human Cognitive Debugger at [http://localhost:3000](http://localhost:3000). The Runtime API remains available at `http://localhost:8080`; the debugger accesses it only through its `/api` reverse proxy and never connects to PostgreSQL directly.

Stop application processes while retaining PostgreSQL data:

```sh
docker compose stop runtime executor debugger
```

## Run locally

Requirements: Go 1.23+, Docker Compose.

```sh
docker compose up -d postgres
DATABASE_URL=postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable go run ./cmd/temporality-runtime
```

## Universal observation ingestion (preview)

Harnesses can send ordinary runtime events without creating FRP Frames, Objectives, or UUID identifiers. The envelope is defined by [`schemas/temporality.event.v1.schema.json`](schemas/temporality.event.v1.schema.json); external project/run/task/actor IDs remain opaque strings. The observation journal is separate from the FRP event log.

```sh
curl -X POST http://localhost:8080/v1/observations/events \
  -H 'content-type: application/json' \
  -d '{"events":[{
    "schema":"temporality.event/1",
    "event_id":"run-42-tool-1",
    "occurred_at":"2026-09-24T10:00:00Z",
    "source":{"id":"worker-1","integration":"example-harness","version":"1"},
    "context":{"project":"repo-a","run":"run-42","task":"task-7","actor":{"id":"agent-a","type":"agent"}},
    "type":"tool.completed",
    "data":{"tool":"tests","status":"failed"}
  }]}'

curl 'http://localhost:8080/v1/observations/events?project=repo-a&run=run-42&limit=100'
# Pass the returned next_cursor to retrieve the next page.
```

Batch requests accept 1–100 events. Re-delivery of the same `(source.id, event_id)` with identical content is reported as `repeated`; reusing that identity for different content is rejected. Unknown non-knowledge event types are retained. Event retrieval is ordered by occurrence time and uses an opaque `next_cursor` for pagination.

Knowledge lifecycle events use `knowledge.proposed` with `data.knowledge_id` and `data.proposition`, followed by `knowledge.used`, `knowledge.confirmed`, `knowledge.challenged`, `knowledge.corrected`, `knowledge.superseded`, `knowledge.invalidated`, or `knowledge.disproved` referencing the same string ID. Each may carry top-level evidence references. Current state and provenance history are projected from those events:

```sh
curl 'http://localhost:8080/v1/observations/knowledge?project=repo-a'
# Restore the state by observed time and exclude events received later.
# knowledge_id narrows the result to one item.
curl 'http://localhost:8080/v1/observations/knowledge?project=repo-a&as_of=2026-09-24T10:00:00Z&known_at=2026-09-24T10:01:00Z&knowledge_id=claim-42'

curl -X POST http://localhost:8080/v1/observations/knowledge/invalidate \
  -H 'content-type: application/json' \
  -d '{"knowledge_id":"claim-42","project":"repo-a","run":"run-42","actor":{"id":"reviewer-1","type":"human"},"reason":"A later test disproved this claim","evidence":[{"ref":"test-run-918","type":"execution"}]}'
```

Manual invalidation appends `knowledge.invalidated`; it does not mutate or delete prior claims/events. For a deterministic automatic rule, a producer can append `knowledge.disproved` with `data.knowledge_id`, `data.reason`, and at least one evidence reference. The projection then moves that knowledge to `invalidated` under `explicit-evidence-disproof.v1`, retaining the disproof event as provenance. Temporality does not infer disproof from free text.

Knowledge graph edges arrive as `knowledge.linked` with `data.knowledge_id`, `data.target_id`, and a relation (`supports`, `contradicts`, `derived_from`, `depends_on`, `supersedes`, or `related_to`). If a node referenced by `derived_from`/`depends_on` is invalidated, dependent nodes are marked `at_risk` with the retired source IDs. They remain visible for review and are returned as cautioned hints; Temporality does not silently invalidate a derived claim without direct evidence.

The recorder can request reusable context after a tool result without modifying that result:

```sh
curl -X POST http://localhost:8080/v1/observations/hints \
  -H 'content-type: application/json' \
  -d '{"project":"repo-a","run":"run-42","task":"debug login","query":"Synapse login returns 401 with PAT","tool":"test-runner","tool_result":"authentication test failed with status 401","limit":8}'
```

The response includes a separate `context_block` and structured `hints`, each with lifecycle state, matched terms/entities/topics, evidence, history, and a `hint_id`. Matching is deterministic lexical/entity/topic overlap; it emits no truth score and excludes corrected, superseded, and invalidated knowledge. Proposed and challenged knowledge are labeled with cautions. The harness decides how to pass the context block to the model. It can later report `hint.used`, `hint.ignored`, or `hint.outcome` observations referring to `data.hint_id` so usefulness can be measured; the projection follows the hint ID back to its knowledge item and exposes offer/use/outcome counts.

Dependency-free Python and JavaScript clients are in [`examples/python/temporality_client.py`](examples/python/temporality_client.py) and [`examples/javascript/temporality.mjs`](examples/javascript/temporality.mjs). Both return the supplemental block while leaving the tool result unchanged; a harness can attach it through its own context hook. The independent project timeline and knowledge view are available at `/observability` in the debugger.

If `DATABASE_URL` is omitted, the runtime uses an ephemeral in-memory store.

## Agent Kernel PoC (early)

The Kernel currently implements a durable Temporal AgentRun workflow, OpenAI-compatible model calls, approval signals, `remember` knowledge proposals, automatic execution-observation capture for successful verification/build commands (project-stable identity: first success proposes, repeats confirm or mark reuse — no duplicate nodes), a derived `agent.summary` event that records the final answer with `derived_from` frame provenance and never replaces the execution record, knowledge hint retrieval, an at-least-once PostgreSQL outbox publisher, an optional MCP stdio adapter, and an optional Docker command sandbox. MCP starts one child process per call in this PoC. Generic runs and the four-stage Lead/Coder/Reviewer/QA workflow completed live smoke runs across Temporal, PostgreSQL, Temporality, the model and Docker sandbox on 2026-09-24. A real code-change task with successful independent review and QA remains unverified; the example is opt-in under a build tag.

Start PostgreSQL, the Temporality runtime and debugger with `docker compose up -d postgres runtime debugger`, then start a Temporal development server in another terminal (for example, `temporal server start-dev`). Configure the same PostgreSQL database and model endpoint and launch the worker/API:

```sh
DATABASE_URL='postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable' \
TEMPORALITY_URL=http://localhost:8080 \
TEMPORALITY_MODEL_BASE_URL=https://api.openai.com/v1 \
TEMPORALITY_MODEL_ID=gpt-4.1-mini \
TEMPORALITY_MODEL_API_KEY="$OPENAI_API_KEY" \
go run ./cmd/agent-kernel
```

Start a run and inspect its status (use a project-scoped run ID):

```sh
curl -X POST http://localhost:8090/v1/agent/runs \
  -H 'content-type: application/json' \
  -d '{"run_id":"demo-1","project":"repo-a","task_id":"hello","prompt":"Say hello and explain what you can do."}'
curl 'http://localhost:8090/v1/agent/runs/demo-1?project=repo-a'
```

The Lead → Coder → Reviewer → QA example lives in [`examples/lead_coder_reviewer_qa`](examples/lead_coder_reviewer_qa) and is excluded from the generic binary. To enable new example runs, start the Kernel with `go run -tags agent_examples ./cmd/agent-kernel`; this adds `POST /v1/agent/examples/lead-coder-reviewer-qa`, a result endpoint at `/v1/agent/examples/lead-coder-reviewer-qa/runs/{run_id}?project=...`, and a child approval endpoint. The result includes each role's handoff. The coordinator and four role runs are distinct Temporal workflows, and each `delegation.*` event links to the child run. Set `KERNEL_SOURCE_ID` to a stable unique identifier for this Kernel installation when multiple installations publish into the same Temporality instance.

Open [http://localhost:3000/agents](http://localhost:3000/agents) to inspect Agent Kernel runs, final answers, event traces, and pending human approvals. It loads the latest verification project, `temporality-live-verification`, by default; use the Project ID supplied to your own run for another project. Sandbox `run_command` calls are auto-approved by the server policy only when Docker sandboxing is configured; each emits `approval.auto_granted` with policy and operation identity. Configured MCP side effects and explicit approval requests still appear for human review. The screen polls running workflows and can approve or reject pending operations; previews are bounded and secret-redacted. The page proxies Kernel requests to `localhost:8090` and observation queries to Runtime `localhost:8080`. Knowledge history and manual invalidation remain at [http://localhost:3000/observability](http://localhost:3000/observability). The Kernel API binds to `:8090` by default. Its POST returns after Temporal accepts the workflow; poll GET for status, and it includes the `RunResult` once the workflow completes. Generic runs and the four-child delegation pipeline have completed live smoke runs. The full repository-changing coding task remains unverified: the live Reviewer could not run `git` in the sandbox image, and QA did not complete a build check.

To enable MCP, set `KERNEL_MCP_COMMAND` to the server executable, `KERNEL_MCP_ARGS` to a JSON string array if needed, and `KERNEL_MCP_ALLOW` to the comma-separated server tool names the Kernel may expose. Optionally set `KERNEL_MCP_APPROVAL` to the subset that must wait for a human signal. Startup fails if the server does not advertise an allowed tool. The approval list must be a subset of the allowlist. Treat the MCP process and its configured credentials as trusted deployment inputs; untrusted code still requires a real sandbox.

Approval waits default to one hour. A run request may set `approval_timeout_seconds` (maximum 24 hours); expiry records `approval.timed_out` and the guarded tool is not executed.

The optional command sandbox requires `KERNEL_SANDBOX_ROOT` (an administrator-managed directory containing allowed workspaces) and `KERNEL_SANDBOX_IMAGE` pinned as `name@sha256:<64 hex chars>` and preloaded in the Docker daemon (`--pull=never`). With both set, requests must include an absolute `workspace_path` beneath that root, and the model receives an approval-gated `run_command` tool. It runs with no network, a read-only container root, dropped capabilities, `no-new-privileges`, resource limits, bounded output and a read-only mount for Reviewer/QA. A uniquely named container is forcibly removed after completion, timeout or cancellation. A live command smoke test passed on 2026-09-24; this does not validate isolation against adversarial workloads. Rootless Docker is recommended. Container isolation still depends on the Docker daemon and host kernel.

The repository's separate AML agent loop also has an optional native adapter in [`aml/temporality/client.go`](aml/temporality/client.go). Configure it as the runner's `Hooks`; after each tool result it requests supplemental hints and the runner appends them as a separate user-context message. API failures degrade to no hints. Set `BaseURL`, `Project`, and a stable unique `SourceID`. Task outcome events are labeled as correlated with hint use, not as proof that a hint caused the outcome.

## Настройка модели (OpenAI-compatible)

Runtime вызывает провайдера через OpenAI-compatible Chat Completions API. Скопируйте `.env.example` в `.env` и настройте переменные:

```sh
cp .env.example .env
```

- `TEMPORALITY_MODEL_BASE_URL` — корень API, обычно с суффиксом `/v1`;
- `TEMPORALITY_MODEL_ID` — идентификатор модели у провайдера;
- `TEMPORALITY_MODEL_API_KEY` — ключ (может быть пустым для Ollama; ключ никогда не возвращается в provenance);
- `TEMPORALITY_MODEL_TEMPERATURE` — температура `0..2`, по умолчанию `0`;
- `TEMPORALITY_MODEL_TIMEOUT` — Go duration, например `180s` (по умолчанию `180s`). Proxy ждёт до 10 минут, поэтому Runtime успевает вернуть структурированную JSON-ошибку вместо HTML `504`;
- `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` — максимум токенов ответа, отправляемый провайдеру как OpenAI-compatible `max_tokens`; по умолчанию `1024`, допустимо `64..65536`. Для reasoning-моделей помните: CoT считается в этом же бюджете, поэтому ставьте с запасом (например `16384`), иначе ответ может остаться пустым с `finish_reason=length`;
- `TEMPORALITY_MODEL_REASONING` — управление ризонингом модели: `off` (запросить отключение, отправляется как `reasoning: {"enabled": false}`), `exclude` (ризонинг считается, но не возвращается в ответе — токены всё равно биллятся), `low`/`medium`/`high` (усилие ризонинга `reasoning: {"effort": ...}`). Пусто (по умолчанию) — поле не отправляется вовсе. Актуально прежде всего для OpenRouter; другие OpenAI-compatible серверы обычно игнорируют поле. Важно: у моделей, которые рассуждают всегда (DeepSeek-R1 и т.п.), ризонинг выключить нельзя — выберите неризонинг-вариант модели (например, `deepseek/deepseek-chat-v3.1` вместо R1);
- `TEMPORALITY_MODEL_LOG_PAYLOADS` — opt-in логирование JSON-запросов и сырого `message.content` ответов (`true` или `false`, по умолчанию `false`);
- `TEMPORALITY_MODEL_LOG_MAX_BYTES` — лимит каждого залогированного payload, по умолчанию `65536`, допустимо `1024..1048576`. Метаданные указывают исходный размер и факт усечения.

Compose передаёт эти значения в runtime.

> **Внимание:** payload logging может записывать prompts, RenderPacket, ответы модели и любые содержащиеся в них пользовательские или чувствительные данные. Включайте его только для контролируемой диагностики, защищайте доступ к логам и выключайте после завершения. HTTP headers и `Authorization` не логируются; API key может встретиться в payload только если он буквально был частью пользовательских данных.

Для временного включения добавьте в `.env` `TEMPORALITY_MODEL_LOG_PAYLOADS=true`, пересоздайте runtime и смотрите записи так:

```sh
docker compose logs -f runtime
```

Примеры провайдеров:

```dotenv
# OpenAI
TEMPORALITY_MODEL_BASE_URL=https://api.openai.com/v1
TEMPORALITY_MODEL_ID=gpt-4.1-mini
TEMPORALITY_MODEL_API_KEY=your-key

# OpenRouter
TEMPORALITY_MODEL_BASE_URL=https://openrouter.ai/api/v1
TEMPORALITY_MODEL_ID=openai/gpt-4.1-mini
TEMPORALITY_MODEL_API_KEY=your-openrouter-key
# отключить ризонинг у переключаемых моделей
TEMPORALITY_MODEL_REASONING=off

# Локальный Ollama, доступный из Compose-контейнера
TEMPORALITY_MODEL_BASE_URL=http://host.docker.internal:11434/v1
TEMPORALITY_MODEL_ID=llama3.2
TEMPORALITY_MODEL_API_KEY=
```

Для Ollama сначала загрузите модель: `ollama pull llama3.2`. При запуске Runtime вне Docker используйте `http://localhost:11434/v1`; `host.docker.internal` нужен именно контейнеру Compose.

После изменения `.env` пересоздайте Runtime:

```sh
docker compose up --build -d runtime executor debugger
```

Безопасную публичную конфигурацию (без API key) можно проверить так:

```sh
curl http://localhost:8080/v1/model/config
```

Провайдер должен поддерживать `response_format: {"type":"json_object"}` и `max_tokens`, а также возвращать один валидный объект `CognitiveEmission` со всеми обязательными полями схемы. Модели без надёжного JSON object mode могут приводить к ответу `502` на `/v1/model-step`.

Если провайдер отвечает медленно, debugger продолжает показывать elapsed time, настроенный timeout и лимит output tokens. По истечении `TEMPORALITY_MODEL_TIMEOUT` Runtime возвращает JSON `504` с сообщением, что провайдер не ответил за настроенное время (Nginx не подменяет его HTML-страницей). Сначала проверьте состояние и очередь провайдера; затем увеличьте timeout либо переключите `TEMPORALITY_MODEL_ID`/`TEMPORALITY_MODEL_BASE_URL` на более быструю модель и пересоздайте Runtime.

End-to-end проверка с локальным mock OpenAI-compatible сервером (нужен запущенный PostgreSQL, например `docker compose up -d postgres`):

```sh
make model-smoke
```

Smoke сначала собирает runtime, поднимает mock на `127.0.0.1:18081`, выполняет model-step и проверяет child Frame, replay, model provenance и события `attention.suggested`; реальные ключи и внешний model API не используются.

Для реального вызова сначала создайте Objective и initial Frame (это делает `scripts/smoke.py`, а IDs также видны в Debugger timeline), затем вызовите:

```sh
curl -X POST http://localhost:8080/v1/model-step \
  -H 'content-type: application/json' \
  -d '{
    "frame_id": "FRAME_UUID",
    "objective_id": "OBJECTIVE_UUID",
    "budget_tokens": 4000,
    "definitions": []
  }'
```

`definitions` содержит разрешённые Affordance Definitions, если модели разрешены actions. Пустой массив означает cognition-only шаг без физических действий. Ответ `503` означает отсутствующую/невалидную env-конфигурацию; `502` — ошибку provider, JSON mode или невалидный `CognitiveEmission`. API key не сохраняется в Event Log, RenderPacket, model provenance или HTTP-ответах.

```sh
curl -X POST http://localhost:8080/v1/events \
  -H 'content-type: application/json' \
  -d '{"event_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a01","episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a02","type":"episode.started","payload":{},"provenance":{"source":"user"}}'

curl -X POST http://localhost:8080/v1/replay \
  -H 'content-type: application/json' \
  -d '{"episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a02"}'

curl 'http://localhost:8080/v1/events?episode_id=018f47a7-34b2-7d10-a932-4f3ff37a4a02&limit=100'
curl http://localhost:8080/metrics

curl -X POST http://localhost:8080/v1/claims \
  -H 'content-type: application/json' \
  -d '{"event":{"payload":{},"provenance":{"source":"user"}},"claim":{"proposition":"The build is reproducible","confidence":0.9}}'

curl -X POST http://localhost:8080/v1/claims/CLAIM_ID/transitions \
  -H 'content-type: application/json' \
  -d '{"event":{"payload":{},"provenance":{"source":"verification"}},"to_status":"supported","confidence":0.98}'
```

## Runnable smoke scenario

With the Compose PostgreSQL healthy, run:

```sh
make smoke
```

This builds and starts `temporality-runtime` plus the separate `temporality-executor` process, creates an initial Frame, creates an Objective, verifies two identical deterministic RenderPackets, submits a validated `CognitiveEmission`, reduces its attention/frame operations into an immutable transition, restores the next Frame from PostgreSQL, atomically reduces CognitiveEmission into Frame/Claims/Attention/Executions and then runs the durable Execution lifecycle, verifies cursor replay/snapshot/blame, learns and matches evidence-backed Procedures, forks two A/B branches, prints the IDs/digest, and stops the runtime.

Render packets expose the attention engine version, scored Event/Region ambient map, selected periphery, and an outside-frame candidate count. `/metrics` includes render totals/errors, focus switches, ambient hit rate, attention entropy, missed candidates, and attention collapse score. Deliberate `attend()` operations bypass ambient hysteresis.

The M2 emission endpoint rejects emissions containing claims or actions until those intents can be committed atomically with Frame and Execution state; they are never silently dropped.

Procedure learning endpoints:

```text
POST /v1/projections/procedures/rebuild
GET  /v1/procedures
GET  /v1/procedures/{id}
POST /v1/procedures/match
```

Procedures are rebuildable projections compiled only from terminal Execution outcomes. Their confidence uses smoothed success/failure evidence, and matched procedures appear in RenderPacket with projector provenance.

Fork and A/B cognition endpoints:

```text
POST /v1/fork
GET  /v1/branches/{id}
GET  /v1/fork-groups/{id}
POST /v1/branches/{id}/head
POST /v1/branch-comparisons
GET  /v1/branch-comparisons/{id}
```

Fork creates two or more isolated root Frames atomically from one source Frame and exact event cursor. Branch heads use compare-and-swap, and comparisons pin both branch/frame endpoints plus comparator version.

Time-travel and provenance endpoints:

```text
POST /v1/replay                  # episode replay or frame_id cursor replay
POST /v1/snapshots
GET  /v1/snapshots/{id}
POST /v1/blame
```

Frame replay is read-only, excludes events after the target Frame cursor, verifies a canonical Frame hash, and treats snapshots only as optional accelerators. Corrupt snapshots fall back to an earlier snapshot or full Event replay.

Rebuildable projection endpoints:

```text
POST /v1/projections/regions/rebuild
GET  /v1/regions
```

Regions are derived from the canonical Event Log by the versioned `region-event-type.v1` projector and can be deleted/rebuilt without memory loss.

Objective and render endpoints:

```text
POST /v1/objectives
GET  /v1/objectives/{id}
POST /v1/render
```

Atomic cognition endpoint:

```text
POST /v1/step
```

A Step commits the validated CognitiveEmission record/hash, candidate Claims, deliberate attention Events, affordance requests, created Executions, `frame.transitioned`, and the next immutable Frame in one transaction. The emission itself is not an Event.

Execution endpoints:

```text
POST /v1/executions
GET  /v1/executions/{id}
POST /internal/v1/executions/{id}/transitions
```

Execution requests and `execution.created` are committed before the Executor can move an execution to `running`. The PoC executor supports the versioned `inspect_environment` deterministic workflow and bounded adaptive executions through durable recorded planner traces through a safe Go filesystem adapter; Every adaptive proposal is persisted before its effect and checked against capabilities/max steps. The recorded adapter performs no inference or network calls, and shell execution is not embedded in the Runtime.

Executions and steps accept an optional `world_id` binding. When present, the runtime validates requested capabilities against the world's grants at intent time and snapshots the world state version; the executor re-authorizes before every effect, so a stale or narrowed world fails the execution with `permission_denied` instead of acting.

World endpoints:

```text
POST /v1/worlds
GET  /v1/worlds
GET  /v1/worlds/{id}
```

A World describes the physical environment: resources (`filesystem`, `git_repository`, `http_endpoint`, `browser`, `mcp_server`), granted capabilities (`filesystem.read`, `git.read`, `http.read`, ... from the frozen registry), identities, credential references (secrets never inline), limits, and deny policies. The first save registers `world.registered` (version 1); every later save must strictly increase `state_version` and emits `world.state_updated`. Events carry the full world snapshot, so the worlds table is a rebuildable projection and replay knows in which environment an action happened.

Executors bind to a world with the `WORLD_ID` environment variable (Compose passes `TEMPORALITY_WORLD_ID` through). A bound executor serves the standard read-only affordances — `inspect_environment`, `inspect_workspace`, `list_files`, `read_file`, `git_status`, `git_log`, `inspect_http` — through the world adapter instead of the legacy safe filesystem adapter. Reads are bounded by world limits (default 64 KiB reads, 1000 directory entries, 30s), filesystem paths are contained inside declared resources (including symlink resolution), git inspection is pure Go without shell-out, and HTTP reads only reach declared `http_endpoint` resources. Every observation becomes a canonical `world.observation` Event committed atomically with the execution transition and carrying `world_id`/`world_version`/`execution_id`/`affordance_id`/`resource`/`observation_type`; there is no separate context-injection path. When the world changed since intent, the observation records the divergence via `intent_world_version`.

Write affordances (M12) extend the same adapter with physical effects. The executor routes each planned step by its capability category: `read`/`observe` steps become observations, `write`/`execute` steps become effects performed by the write adapter. Standard write affordances: `write_file`, `create_file`, `patch_file`, `delete_file`, `move_file`, `create_dir` (filesystem.write), `run_command` (process.execute), `git_create_branch`, `git_commit` (git.write), and `http_post` (http.write). Every physical change lands as a canonical `world.effect` Event committed atomically with the execution transition, in the same shape as observations (`resource`/`effect_type`/`payload` plus world and execution linkage), so replay can always answer what changed in the world and why. The effect boundary is enforced end to end: the capability is validated at intent time, re-authorized against the current world state at effect time, the intent is durable before any effect runs, and the result is persisted after. Writes additionally refuse `read_only` resources; content is bounded by `max_read_bytes`; process output is captured and truncated by the same limits; the process timeout comes from `timeout_sec`; and process execution honors command-scoped policies — an `allow` policy for `process.execute` with a `commands` list forms an allowlist, a `deny` policy with `commands` blocks only those names. Git writes are pure Go without shell-out: branches are ref writes and commits hash worktree contents as loose objects (git-tree ordering, executable modes, symlinks) and advance the current ref; packed-object repositories and checkout remain out of scope, and the commit snapshots the working tree rather than the staged index. Browser and MCP capabilities stay in the registry without an adapter for now.

On top of the primitives sits the semantic affordance layer (M12.4): `inspect_repository` (stat + listing + git status + git log in one execution), `run_tests` and `reproduce_issue` (bounded process execution, stack-agnostic via command/args overrides), and `update_configuration`, which reads the file and patches it so a single atomic execution commits both the `world.observation` of the current content and the `world.effect` of the change. The agent works with these meaningful actions while the primitive operations remain Executor building blocks — the deterministic workflows compile semantic intent into mixed observation/effect plans.

Ingestion endpoints:

```text
POST /v1/ingest
GET  /v1/claims/{id}/evidence
```

`POST /v1/ingest` runs one bounded knowledge import over a declared world resource (M13). The runtime observes the source through the same read-only world adapter (capability grants, path containment, and world limits all apply) and commits every observation as a canonical `world.observation` Event with provenance source `ingestion`. Filesystem and git resources are walked breadth-first within `depth` (default 2, max 5) and a per-run observation budget (`max_events`, default 100, max 500), reading recognized descriptors (`go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`, `README.md`); git resources add status/log observations; HTTP endpoints are fetched once. Deterministic extractors then turn observations into candidate Claims whose confidence stays below 1 and whose evidence cites the observation Events, keeping "what was found" separable from "what the substrate believes". Claim evidence is queryable via `GET /v1/claims/{id}/evidence`. Pointing ingestion at an empty substrate is the bootstrap import: there is no separate knowledge base, and observations enter memory only through the Event Log, where regions/attention pick them up after a rebuild.

Entity endpoints (M14):

```text
POST /v1/projections/entities/rebuild
GET  /v1/entities?type=&name=
GET  /v1/entities/{id}
```

Claims may carry an optional `subject`/`predicate`/`object` triple (`"module:example.com/app"`, `depends_on`, `"library:github.com/x/y"`) validated against frozen registries of entity types and predicates. Ingest extractors emit such triples: manifests bind packages/modules to their files and dependencies (`declared_in`, `depends_on`, `targets`, `contains`), git status binds repositories to branches (`on_branch`). `POST /v1/projections/entities/rebuild` scans every claim and atomically replaces the global entity graph — entities and typed relations are a pure projection over claims (deterministic content-derived ids, refuted/superseded claims stop projecting), never a separately maintained knowledge base. Entities are world knowledge rather than episode state: during render, entities with relevance to the frame (focus/objective overlap, graph proximity to a pinned entity) compete with events and regions for ambient attention, bounded to the most relevant candidates so a large graph cannot flood every frame. `frame.Ref` accepts `entity` refs in focus and working set, and the entity's confidence is the strongest confidence among the claims asserting it.

Bootstrap endpoint (M15):

```text
POST /v1/bootstrap
```

`POST /v1/bootstrap` is first contact: a fresh agent with an empty substrate meets a declared world. Given `world_id` and `objective_text`, the runtime creates a bootstrap episode (`episode.started` with world binding), an initial explore-mode frame focused on the objective, runs a bounded discovery over every declared filesystem/git resource through the normal ingestion pipeline (observations with `ingestion` provenance, evidence-backed candidate claims, no context-injection shortcut), rebuilds region/edge/entity projections, and returns the first render of the first frame. The agent therefore starts inside a map of the world that arose purely from real observation — and can immediately act from that frame: a step with a world-bound action (read, write, run) flows through the execution pipeline and commits its observation or effect into the same episode, closing the loop WORLD → OBSERVE → MEMORY → ATTENTION → FRAME → ACTION → WORLD. The response includes episode/branch/frame/objective ids, per-resource ingestion results, projection counts, the render packet, and a summary; `resource_id`, `depth`, and `max_events` bound the discovery, and `budget_tokens` sizes the first render.

Frame endpoints:

```text
POST /v1/frames
POST /v1/frames/{id}/transitions
POST /v1/frames/{id}/emissions
GET  /v1/frames/{id}
```

Model endpoints:

```text
GET  /v1/model/config             # безопасная конфигурация (без секретов)
POST /v1/model-step               # render -> LLM -> атомарный cognitive step
```

`POST /v1/model-step` принимает `frame_id`, `objective_id`, `budget_tokens`, `definitions` и опциональный `world_id`: рендер-пакет (включая раздел affordances с input_schema) уходит в OpenAI-совместимый провайдер, декодированный CognitiveEmission коммитится атомарно тем же шагом, а при world-привязке исполнения снапшотят версию мира. Ошибки провайдера возвращаются как JSON с actionable-подсказками (обрезка по `finish_reason=length`, пустой ответ при включённом reasoning и т.п.).

## Сценарий: first contact (сквозная проверка)

`scripts/first_contact.py` — автоматический рассказ о полном контуре M11–M15 с живой LLM: агент с пустой памятью приходит в свежий git-репозиторий (там спрятан баг: `Sum` вычитает вместо сложения, и тест `TestSum` падает) и должен сам разобраться в проекте.

```sh
docker compose up --build -d     # postgres + debugger + runtime
docker compose stop executor     # обязательно: compose-executor перехватывает исполнения
make first-contact               # или: python3 scripts/first_contact.py --max-steps 4
```

Скрипт поднимает собственный локальный runtime (`bin/temporality-runtime`, тот же PostgreSQL): bootstrap-ingestion выполняется внутри runtime, и пути мира должны быть видны процессу — локальный runtime видит хост-пути, а контейнер — нет. Флаг `--runtime URL` подключается к внешнему runtime, если его окружение видит пути репозитория.

Что происходит (и что это доказывает):

1. **Declare the world** — репозиторий регистрируется как World с ресурсом `git_repository` и правами `filesystem.read` + `git.read`: агент ничего не может трогать кроме объявленного (M11).
2. **Bootstrap** — `POST /v1/bootstrap` заселяет пустой substrate через обычный ingestion-pipeline: наблюдения → claims с provenance → regions → entity graph. Никакой отдельной «базы знаний» — карта мира возникает из реальных событий (M13+M14+M15).
3. **Cognition loop** — каждый `/v1/model-step` показывает: модель видит affordances в render packet, выбирает действия (`inspect_repository`, `read_file`, `git_log`…), Runtime валидирует намерение, executor выполняет его в мире, наблюдение возвращается событием `world.observation`, regions пересобираются — и следующий кадр уже знает больше. Это и есть замкнутый контур WORLD → OBSERVE → MEMORY → ATTENTION → FRAME → COGNITION → AFFORDANCE → EXECUTION → WORLD.
4. **What the agent knows** — счётчики событий по типам и рост entity graph показывают, что память — побочный продукт взаимодействия с миром, а не подкачка контекста.
5. **Result** — финальный ответ модели + ссылки: debugger (`http://localhost:3000`, вставьте episode id), `/v1/events?episode_id=…`, `/v1/replay` любого кадра. Каждое «что агент знал на этом шаге?» отвечает реальным сохранённым RenderPacket, а не реконструкцией задним числом (M10).

Критерий успеха: за несколько шагов модель находит `cmd/app/main.go`, указывает на `a - b` вместо `a + b` и формулирует исправление — не имея на старте ничего, кроме объективы и прав на чтение. На `deepseek/deepseek-v4.1-flash` сценарий обычно сходится за два шага: разведка структуры → чтение файлов → диагноз с `completion`.

Неудачный `/v1/model-step` ничего не фиксирует (render/emit идут до персистентности, а CommitStep транзакционен), поэтому скрипт безопасно повторяет шаг до 4 раз на 422/5xx — живые модели недетерминированы и иногда шлют невалидные emission (пустые refs, объектные completion); Runtime нормализует типовые огрехи на границе декодирования, а семантика остаётся строгой.

## Бенчмарк: FRP vs классический харнесс (State 2)

`scripts/benchmark.py` — честное head-to-head сравнение двух парадигм на одной задаче, одной модели и одном инструментальном охвате:

- **FRP** — render packet (с бюджетом) → CognitiveEmission → семантические affordances → executor → world events → следующий кадр;
- **classic** — канонический tool-calling loop с полной историей: system prompt + объективa + все результаты инструментов накапливаются в каждом запросе.

Обе стороны получают: одну модель (`TEMPORALITY_MODEL_*`, одинаковые temperature/max_tokens/reasoning), одинаковую объективу и равный охват инструментов (classic-инструменты зеркалят семантику executor: те же лимиты 64KB/1000 записей/60s).

Фикстура — детерминированный Go-репозиторий `example.com/ledger` с двумя независимыми багами и ловушками: int32-аккумулятор в `internal/series` ломает большие суммы в `internal/calc` (distant coupling — симптом в другом пакете, чем причина); `internal/store.Remove` пишет tombstone, который `Get`/`Len` игнорируют; `make test` прогоняет только `./internal/calc` (README врёт, что это «весь набор»); ARCHITECTURE.md врёт про «lossless 64-bit accumulation»; `internal/util/format.go` выглядит подозрительно, но корректен (red herring).

Независимый чекер выносит вердикт по факту: `go test ./...` зелёный, ни один `*_test.go` не изменён, `internal/store/store.go` изменён, изменено ≥ 2 non-test файлов. Расход токенов берётся из реального `usage` провайдера с обеих сторон (FRP — из `model_usage` в ответе `/v1/model-step`, который Runtime теперь возвращает и логирует).

```sh
docker compose up --build -d && docker compose stop executor
make benchmark                   # обе стороны, бюджет 30 шагов
make classic-benchmark           # только классический харнесс
python3 scripts/benchmark.py --side frp --max-steps 5   # короткий отладочный прогон
```

Метрики печатаются таблицей (steps, model_calls, retries, prompt/completion/total tokens, seconds, success) и сохраняются в `benchmarks/benchmark-<ts>.json`. Сценарий отвечает на главный вопрос State 2: решает ли FRP реальные задачи лучше или дешевле conventional harness — и показывает, где именно проходит цена (плоский, но тяжёлый render packet против растущей без потолка истории).

## Debugger development

```sh
npm --prefix debugger install
npm --prefix debugger test
npm --prefix debugger run dev
```

The Vite development server proxies `/api` to `localhost:8080`.

## Development

```sh
make fmt
make test
make race

# With a running PostgreSQL instance:
TEST_DATABASE_URL='postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable' make integration
```

Pagination responses contain `next_cursor` when another page exists. Pass it back as the `cursor` query parameter; clients must treat it as opaque.

The runtime/executor boundary is preserved: this service records runtime facts and does not execute external side effects.
