# Temporality

Temporality — это опыт и память для программных агентов. Любой harness
отправляет события о том, что агент делал и наблюдал; Temporality хранит
их как append-only журнал, проектирует из них знания с полным жизненным
циклом (proposed → confirmed → reused → contradicted → invalidated) и
возвращает подсказки, которые агент использует в следующих запусках.
Experience Timeline показывает, как память агента рождалась,
активировалась, конфликтовала и умирала во времени.

```text
Agent Kernel ──events──▶ Journal ──projection──▶ Knowledge / Hints
     ▲                      │                        │
     └──────── hints ◀──────┴──── Experience Timeline ┘
```

Состав репозитория:

| Компонент | Путь | Что делает |
|---|---|---|
| Observation Journal | `observation/`, `cmd/temporality-journal` | append-only события, knowledge-проекция, hint retrieval, bitemporal запросы (`as_of`/`known_at`) |
| Agent Kernel | `kernel/`, `cmd/agent-kernel` | durable agent harness на Temporal: model gateway, MCP, sandbox, approvals, delegation, публикация событий в journal |
| Experience Timeline | `debugger/` | UI: `/experience` (таймлайн жизни памяти), `/agents` (запуски kernel и approvals), `/observability` (knowledge и инвалидации) |

Схема события: [`schemas/temporality.event.v1.schema.json`](schemas/temporality.event.v1.schema.json).
Контракты и модели: [`docs/temporality-contract.md`](docs/temporality-contract.md),
[`docs/knowledge-model.md`](docs/knowledge-model.md), [`docs/event-model.md`](docs/event-model.md),
[`docs/architecture.md`](docs/architecture.md). Инвентаризация продукта и
направление развития: [`docs/product-architecture.md`](docs/product-architecture.md).

## Запуск полного стека

```sh
cp .env.example .env   # настроить TEMPORALITY_MODEL_* и опционально sandbox
docker compose up --build -d
```

Поднимаются: PostgreSQL, Journal (`:8080`), debugger (`:3000`), Temporal
dev server (UI на `:8233`) и Agent Kernel (`:8090`).

- Experience Timeline — [http://localhost:3000/experience](http://localhost:3000/experience)
- Agent runs и approvals — [http://localhost:3000/agents](http://localhost:3000/agents)
- Knowledge observability — [http://localhost:3000/observability](http://localhost:3000/observability)

Остановить приложение, сохранив данные PostgreSQL:

```sh
make stack-stop
```

## Настройка модели (OpenAI-compatible)

Kernel вызывает провайдера через OpenAI-compatible Chat Completions API.
Переменные в `.env`:

- `TEMPORALITY_MODEL_BASE_URL` — корень API, обычно с суффиксом `/v1`;
- `TEMPORALITY_MODEL_ID` — идентификатор модели у провайдера;
- `TEMPORALITY_MODEL_API_KEY` — ключ (пустой для Ollama; ключ никогда не
  возвращается в provenance);
- `TEMPORALITY_MODEL_TEMPERATURE` — `0..2`, по умолчанию `0`;
- `TEMPORALITY_MODEL_TIMEOUT` — Go duration, по умолчанию `180s`;
- `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` — `64..65536`, по умолчанию
  `1024`. Для reasoning-моделей CoT считается в том же бюджете — ставьте
  с запасом (например `16384`), иначе ответ может остаться пустым с
  `finish_reason=length`;
- `TEMPORALITY_MODEL_REASONING` — `off` / `exclude` / `low` / `medium` /
  `high`; пусто — поле не отправляется. У моделей, которые рассуждают
  всегда (DeepSeek-R1 и т.п.), ризонинг выключить нельзя — выберите
  неризонинг-вариант;
- `TEMPORALITY_MODEL_LOG_PAYLOADS` — opt-in логирование JSON-запросов и
  ответов (по умолчанию `false`); `TEMPORALITY_MODEL_LOG_MAX_BYTES` —
  лимит payload (по умолчанию `65536`).

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

# Локальный Ollama (из compose-контейнера)
TEMPORALITY_MODEL_BASE_URL=http://host.docker.internal:11434/v1
TEMPORALITY_MODEL_ID=llama3.2
TEMPORALITY_MODEL_API_KEY=
```

После изменения `.env` пересоздайте стек: `docker compose up --build -d`.

## Auth (bearer-токены)

Journal и Agent Kernel поддерживают bearer-токены с RBAC и project-scoping
(минимум enterprise control plane). Пустые переменные = auth выключен
(только для локальной разработки; при старте печатается предупреждение).

Формат записи: `token:subject:role:projects`, записи разделяются `;` или
переносами строк. Роли: `reader < writer < operator < admin`. Projects —
`*` или список через запятую. Токены: `openssl rand -hex 24`.

```sh
# .env
JOURNAL_AUTH_TOKENS=reader-token:alice:reader:*;writer-token:bob:writer:lighthouse;operator-token:carol:operator:*
KERNEL_AUTH_TOKENS=reader-token:alice:reader:*;writer-token:bob:writer:lighthouse;operator-token:carol:operator:*
TEMPORALITY_API_TOKEN=writer-token   # ядро → journal (ingest + hints)
```

Права:

| Слой | reader | writer | operator | admin |
|---|---|---|---|---|
| journal | чтение, hints | ingest событий | + invalidation | всё |
| kernel | GET runs | POST runs | + approvals, outbox | всё |

Листинг без фильтра проекта требует `*`-scope. `/healthz` открыт.
В UI debugger токен вводится в поле в хедере любой страницы (хранится в
localStorage, отправляется на journal и kernel).

```sh
curl -H 'Authorization: Bearer reader-token' 'http://localhost:8080/v1/observations/knowledge?project=repo-a'
```

## Journal API

Приём событий (1–100 на batch; повторная доставка того же
`(source.id, event_id)` с тем же содержимым — `repeated`, с другим —
конфликт):

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
```

Knowledge lifecycle: `knowledge.proposed` (с `data.knowledge_id` и
`data.proposition`), затем `knowledge.used` / `confirmed` / `challenged` /
`corrected` / `superseded` / `invalidated` / `disproved` с тем же ID и
evidence-ссылками. Текущее состояние и история — проекция из событий:

```sh
curl 'http://localhost:8080/v1/observations/knowledge?project=repo-a'
# bitemporal восстановление состояния
curl 'http://localhost:8080/v1/observations/knowledge?project=repo-a&as_of=2026-09-24T10:00:00Z&known_at=2026-09-24T10:01:00Z&knowledge_id=claim-42'

curl -X POST http://localhost:8080/v1/observations/knowledge/invalidate \
  -H 'content-type: application/json' \
  -d '{"knowledge_id":"claim-42","project":"repo-a","actor":{"id":"reviewer-1","type":"human"},"reason":"A later test disproved this claim","evidence":[{"ref":"test-run-918","type":"execution"}]}'
```

Подсказки после tool-результата (детерминированный lexical/entity/topic
матчинг; corrected/superseded/invalidated знания исключаются,
возвращается `context_block` для модели):

```sh
curl -X POST http://localhost:8080/v1/observations/hints \
  -H 'content-type: application/json' \
  -d '{"project":"repo-a","run":"run-42","query":"login returns 401 with PAT","tool":"test-runner","tool_result":"authentication test failed with status 401","limit":8}'
```

Harness может затем сообщать `hint.used` / `hint.ignored` /
`hint.outcome` с `data.hint_id` — projection считает offer/use/outcome.

Клиенты без зависимостей: [`examples/python/temporality_client.py`](examples/python/temporality_client.py),
[`examples/javascript/temporality.mjs`](examples/javascript/temporality.mjs).

## Agent Kernel

Durable Temporal workflow: OpenAI-compatible model calls, approvals,
`remember` knowledge proposals, автоматический capture наблюдений
успешных verification/build команд (project-stable identity — первой
успешный запуск предлагает знание, повторные подтверждают/reuse),
at-least-once outbox в journal, опциональный MCP stdio adapter и Docker
command sandbox.

Запуск вне compose (нужны запущенные postgres, journal и Temporal dev
server):

```sh
DATABASE_URL='postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable' \
TEMPORALITY_URL=http://localhost:8080 \
TEMPORAL_ADDRESS=localhost:7233 \
TEMPORALITY_MODEL_BASE_URL=... TEMPORALITY_MODEL_ID=... TEMPORALITY_MODEL_API_KEY=... \
make kernel-run
```

Запуск и статус:

```sh
curl -X POST http://localhost:8090/v1/agent/runs \
  -H 'content-type: application/json' \
  -d '{"run_id":"demo-1","project":"repo-a","task_id":"hello","prompt":"Say hello and explain what you can do."}'
curl 'http://localhost:8090/v1/agent/runs/demo-1?project=repo-a'
```

MCP: `KERNEL_MCP_COMMAND`, `KERNEL_MCP_ARGS` (JSON array),
`KERNEL_MCP_ALLOW` (список tools), `KERNEL_MCP_APPROVAL` (subset,
требующий human approval). Sandbox: `KERNEL_SANDBOX_ROOT` +
`KERNEL_SANDBOX_IMAGE` (`name@sha256:...`, `--pull=never`), после чего
запросы с абсолютным `workspace_path` получают approval-gated
`run_command` (no network, read-only root, dropped capabilities,
resource limits, bounded output). Approval waits — 1 час по умолчанию
(`approval_timeout_seconds`, максимум 24h).

Lead → Coder → Reviewer → QA пример — [`examples/lead_coder_reviewer_qa`](examples/lead_coder_reviewer_qa),
в compose включён build-тегом `agent_examples`.

### Failure reconciliation

`tool.failed` несёт семантику эффекта: `effect=none` (отказ до исполнения —
безопасно повторять) vs `effect=uncertain` (сбой на/после границы
исполнения). Неурегулированные операции проецируются из event stream
по запросу:

```sh
curl 'http://localhost:8090/v1/agent/operations?project=repo-a' \
  -H "Authorization: Bearer $READER_TOKEN"
curl -X POST http://localhost:8090/v1/agent/operations/reconcile \
  -H "Authorization: Bearer $OPERATOR_TOKEN" -H 'content-type: application/json' \
  -d '{"project":"repo-a","run_id":"demo-1","operation_id":"...","effect":"occurred","note":"verified downstream","actor_id":"ops","started_event_id":"..."}'
```

`effect` — закрытый словарь `none|occurred|unknown`; записанный вердикт
сеттлит операцию (производный `operation.reconciled` с `parent_event_id`
на `tool.started`). Операции с неурегулированным эффектом видны и
реконсайлятся из UI: экран **Operations** (`/operations`) в debugger,
форма записи вердикта доступна токенам с ролью operator. Live-drill:
`KERNEL_FAULT_AFTER_EFFECT=<operation-id
или tool-name>` — эффект реально выполняется, после чего воркер
«падает», и операция честно попадает в listing как uncertain
(см. [`docs/failure-reconciliation.md`](docs/failure-reconciliation.md)).

## Control plane

Auth/RBAC: bearer-токены `token:subject:role:projects` (reader < writer <
operator < admin), project-scoping на journal и kernel API
(`JOURNAL_AUTH_TOKENS` / `KERNEL_AUTH_TOKENS`).

Квоты (kernel): `KERNEL_RUN_QUOTAS="*:50,forge:200"` — лимит запусков
агентов в UTC-день на проект; `*` задаёт дефолт, отсутствие записи —
безлимит. Слот расходуется только на валидно принятый запуск (422/422-класс
ошибок квоту не жгут), превышение — 429 с `Retry-After` и телом
`{quota: {allowed, limit, used, reset_at}}`. Текущее использование:

```sh
curl 'http://localhost:8090/v1/agent/quotas?project=repo-a' \
  -H "Authorization: Bearer $READER_TOKEN"
```

Секреты: конвенция `<VAR>_FILE` — если переменная не задана напрямую, а
`<VAR>_FILE` указывает на файл, значение берётся из файла (без хвостового
переноса строки). Поддержаны `DATABASE_URL`,
`TEMPORALITY_MODEL_API_KEY`, `TEMPORALITY_API_TOKEN`,
`KERNEL_AUTH_TOKENS` (kernel) и `JOURNAL_AUTH_TOKENS` (journal) — так
docker compose secrets подключаются без попадания ключей в окружение.
Прямое env-значение всегда приоритетнее файла.

## Experience Timeline

Главный экран — `/experience`: runs по горизонтали, knowledge-полосы по
временени с lifecycle-событиями (appeared/recalled/injected/reused/
validated/contradicted/invalidated), lens-режимы (trajectory /
experience / lifecycle / conflicts / activation), semantic zoom
cluster → knowledge → episode, forensic-панель с evidence и lineage.
Первые acceptance-корпуса — эксперименты 4–8
([`docs/experiment4-rejected-path.md`](docs/experiment4-rejected-path.md) …
[`docs/experiment8-long-horizon-evolution.md`](docs/experiment8-long-horizon-evolution.md)),
фикстуры в `debugger/src/fixtures`.

## Разработка

```sh
make fmt          # gofmt cmd kernel observation examples
make test         # go test ./...
make race         # go test -race ./...
make build        # bin/temporality-journal + bin/temporality-agent-kernel
make debugger-test
make debugger-build
```

Debugger dev-сервер (`npm --prefix debugger run dev`) проксирует `/api`
на `localhost:8080` (journal) и `/kernel-api` на `localhost:8090`.

## История

Репозиторий прошёл три эпохи гипотез: FRP cognitive runtime (эпоха 1),
AML benchmark harness (эпоха 2), Agent Kernel + Temporality journal
(продукт). Документы эпох и старые бенчмарки — в [`docs/history/`](docs/history/).
