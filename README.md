# Temporality

Reference implementation of **Frame Runtime Protocol (FRP) v0.3**.

## Status

Implemented foundations for **M0 — Event Log + replay** and **M1 — Claims**:

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

If `DATABASE_URL` is omitted, the runtime uses an ephemeral in-memory store.

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
- `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` — максимум токенов ответа, отправляемый провайдеру как OpenAI-compatible `max_tokens`; по умолчанию `1024`, допустимо `64..16384`;
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

Executors bind to a world with the `WORLD_ID` environment variable (Compose passes `TEMPORALITY_WORLD_ID` through). A bound executor serves the standard read-only affordances — `inspect_environment`, `inspect_workspace`, `list_files`, `read_file`, `git_status`, `git_log`, `inspect_http` — through the world adapter instead of the legacy safe filesystem adapter. Reads are bounded by world limits (default 64 KiB reads, 1000 directory entries, 30s), filesystem paths are contained inside declared resources (including symlink resolution), git inspection is pure Go without shell-out, and HTTP reads only reach declared `http_endpoint` resources. Every observation becomes a canonical `world.observation` Event committed atomically with the execution transition and carrying `world_id`/`world_version`/`execution_id`/`affordance_id`/`resource`/`observation_type`; there is no separate context-injection path. When the world changed since intent, the observation records the divergence via `intent_world_version`. Write capabilities (`filesystem.write`, `process.execute`, `git.write`, `http.write`, `browser.*`) are part of the frozen registry but intentionally have no adapter yet; they arrive with M12 after the observation pipeline matures.

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

Frame endpoints:

```text
POST /v1/frames
POST /v1/frames/{id}/transitions
POST /v1/frames/{id}/emissions
GET  /v1/frames/{id}
```

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
