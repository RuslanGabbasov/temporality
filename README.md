# Temporality

Temporality — система наблюдаемости и накопления опыта для ИИ-агентов.

Агент выполняет задачу; Temporality фиксирует, что он знал и делал, откуда
взялись эти знания, как они применялись, что подтвердилось или оказалось
ложным, и как опыт менялся со временем. При новом запуске система
подсказывает релевантный прошлый опыт в компактном виде, не перегружая
контекст; при необходимости агент может обратиться к исходным данным.

```text
Agent Kernel ──events──▶ Journal ──projection──▶ Knowledge / Hints
     ▲                      │                         │
     └──────── hints ◀──────┴──── Experience Timeline ┘
```

## Состав

| Компонент | Путь | Назначение |
|---|---|---|
| Journal | `observation/`, `cmd/temporality-journal` | append-only события, knowledge-проекция, hint retrieval, bitemporal запросы |
| Agent Kernel | `kernel/`, `cmd/agent-kernel` | durable agent harness на Temporal: model gateway, MCP, sandbox, approvals, delegation |
| Workspace | `debugger/src/Workspace.tsx` | чат с агентом: SSE streaming, reasoning, conversations, branching |
| Debugger | `debugger/` | UI для наблюдения и анализа опыта агента |
| Control Plane | `controlplane/` | auth/RBAC, bearer-токены, project-scoping |

### Страницы UI

| Путь | Назначение |
|---|---|
| `/workspace` | Чат с агентом — SSE streaming, reasoning, Markdown, conversations |
| `/agent-config` | Управление агентами — CRUD, шаблоны, полная конфигурация |
| `/agents` | Traces запусков — timeline событий, trajectory extraction |
| `/operations` | Uncertain operations — batch reconcile, verdict buttons |
| `/observability` | Knowledge — lifecycle, activation chains, patterns, projection |
| `/experience` | Experience Timeline — memory lens, semantic zoom, forensics |
| `/providers` | Model providers — OpenAI-compatible endpoints |
| `/users` | Users — RBAC, tokens, project access |

## Запуск

```sh
cp .env.example .env   # настроить TEMPORALITY_MODEL_* и опционально sandbox
docker compose up --build -d
```

Поднимаются: PostgreSQL, Journal (`:8080`), debugger (`:3000`), Temporal
dev server (UI на `:8233`) и Agent Kernel (`:8090`).

Остановить, сохранив данные:

```sh
make stack-stop
```

## Настройка модели

Kernel вызывает провайдера через OpenAI-compatible Chat Completions API.

| Переменная | Описание | По умолчанию |
|---|---|---|
| `TEMPORALITY_MODEL_BASE_URL` | Корень API (с `/v1`) | — |
| `TEMPORALITY_MODEL_ID` | ID модели у провайдера | — |
| `TEMPORALITY_MODEL_API_KEY` | Ключ (пустой для Ollama) | — |
| `TEMPORALITY_MODEL_TEMPERATURE` | `0..2` | `0` |
| `TEMPORALITY_MODEL_TIMEOUT` | Go duration | `180s` |
| `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | `64..1000000` | `16384` |
| `TEMPORALITY_MODEL_REASONING` | `off` / `exclude` / `low` / `medium` / `high` | — |

Для reasoning-моделей CoT считается в том же бюджете — ставьте
с запасом (например `16384` или больше), иначе ответ может остаться
пустым с `finish_reason=length`. Default уже `16384`.

Примеры:

```dotenv
# OpenAI
TEMPORALITY_MODEL_BASE_URL=https://api.openai.com/v1
TEMPORALITY_MODEL_ID=gpt-4.1-mini
TEMPORALITY_MODEL_API_KEY=your-key

# OpenRouter
TEMPORALITY_MODEL_BASE_URL=https://openrouter.ai/api/v1
TEMPORALITY_MODEL_ID=openai/gpt-4.1-mini
TEMPORALITY_MODEL_API_KEY=your-openrouter-key

# Локальный Ollama
TEMPORALITY_MODEL_BASE_URL=http://host.docker.internal:11434/v1
TEMPORALITY_MODEL_ID=llama3.2
TEMPORALITY_MODEL_API_KEY=
```

## Auth

Bearer-токены с RBAC и project-scoping. Пустые переменные = auth выключен
(только для локальной разработки).

Формат: `token:subject:role:projects`, записи через `;` или переносы строк.
Роли: `reader < writer < operator < admin`. Projects — `*` или список через
запятую. Токены: `openssl rand -hex 24`.

```sh
# .env
JOURNAL_AUTH_TOKENS=token1:alice:reader:*;token2:bob:writer:lighthouse
KERNEL_AUTH_TOKENS=token1:alice:reader:*;token2:bob:writer:lighthouse
TEMPORALITY_API_TOKEN=token2   # ядро → journal
```

| Слой | reader | writer | operator | admin |
|---|---|---|---|---|
| journal | чтение, hints | ingest | + invalidation | всё |
| kernel | GET runs | POST runs | + approvals, reconcile | всё |

В UI токен вводится через Login в хедере (хранится в localStorage).

## Каналы уведомлений

Когда агент вызывает `ask_human`, ядро доставляет вопрос получателю по его
предпочтительному каналу (см. `docs/triggers-and-escalations.md` §6-7). Каналы —
часть профиля пользователя и настраиваются самостоятельно в меню пользователя →
«Каналы уведомлений». Встроенный канал — почтовый ящик в приложении (Runs →
вопросы); внешние транспорты включаются переменными ядра:

```sh
# .env
KERNEL_MATRIX_HOMESERVER=https://matrix.org       # homeserver бота ядра
KERNEL_MATRIX_ACCESS_TOKEN=syt_...                  # токен бота (состоит в комнатах пользователей)
KERNEL_TELEGRAM_BOT_TOKEN=123456:ABC...             # токен бота для Bot API
KERNEL_UI_URL=http://localhost:3000                 # для ссылок в уведомлениях
```

Адрес канала: для Matrix — ID комнаты (`!room:matrix.org`), для Telegram — chat
ID. Доставка не блокирует выполнение: сбой транспорта фиксируется событием
`notification.sent` со статусом `failed`, а вопрос остаётся доступным в UI.
Ответ всегда происходит в приложении; внешние каналы — только доставка вопроса.

## Agent Kernel

Durable Temporal workflow: model calls, approvals, `remember` proposals,
auto-capture verification observations, at-least-once outbox, MCP stdio
adapter, Docker command sandbox.

### Sandbox

`KERNEL_SANDBOX_ROOT` + `KERNEL_SANDBOX_IMAGE` — изолированный контейнер
для `run_command`: no network (или `bridge` если `network_access: true`),
read-only root, dropped capabilities, resource limits, bounded output.

### MCP

`KERNEL_MCP_COMMAND`, `KERNEL_MCP_ARGS` (JSON array), `KERNEL_MCP_ALLOW`
(tools list), `KERNEL_MCP_APPROVAL` (subset requiring human approval).

### Failure reconciliation

`tool.failed` несёт семантику эффекта: `effect=none` (отказ до исполнения)
vs `effect=uncertain` (сбой на границе исполнения). Read-only tools
(read_file, search, grep) автоматически получают `effect=none`.

Неурегулированные операции видны на `/operations` с batch reconcile.

```sh
curl 'http://localhost:8090/v1/agent/operations?project=repo-a' -H "Authorization: Bearer $TOKEN"
curl -X POST http://localhost:8090/v1/agent/operations/reconcile \
  -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"project":"repo-a","run_id":"...","operation_id":"...","effect":"occurred","note":"verified","actor_id":"ops","started_event_id":"..."}'
```

## Trajectory & Experience

Trajectory extraction строит из event stream структурированные данные:
turns, tool calls, knowledge events, repeating patterns. Experience
projection агрегирует trajectories across runs: tool patterns, knowledge
lifecycle, scopes, success rates.

| Endpoint | Описание |
|---|---|
| `GET /v1/workspace/runs/{id}/trajectory` | Пер-run extraction |
| `GET /v1/workspace/projects/{id}/projection` | Cross-run aggregation |
| `GET /v1/workspace/runs/compare?a=X&b=Y` | Side-by-side diff |
| `GET /v1/workspace/runs/{id}/artifacts` | Reusable tool sequences |
| `GET /v1/workspace/knowledge/{id}/chain` | Activation chain |
| `GET /v1/workspace/projects/{id}/chains` | All chains |
| `GET /v1/workspace/runs/{id}/stream` | SSE event stream |
| `GET /v1/workspace/runs/{id}/stream` | + model.text_delta, model.reasoning |

## Workspace

Чат с агентом через SSE streaming. Каждое сообщение → task + run.
Follow-up messages reuse the same task (PATCH prompt). Conversation
history передаётся как context. Agent selector при создании чата.
Branching — форк из любой точки.

Features:
- SSE streaming: model.text_delta, model.reasoning, tool events
- Markdown rendering для ответов
- Reasoning/thinking block (collapsible)
- Conversation persistence (localStorage)
- Agent templates (Coder, Reviewer, Researcher, DevOps)
- Project settings (default agent, default model)

## Control Plane

- **Auth**: bearer-токены `token:subject:role:projects`
- **Quotas**: `KERNEL_RUN_QUOTAS="*:50,project:200"` — лимит запусков/день
- **Secrets**: конвенция `<VAR>_FILE` — значение берётся из файла
- **Providers**: OpenAI-compatible model endpoints, API key masking
- **Users**: CRUD with token generation, RBAC roles

## Experience Timeline

`/experience` — визуализация жизни памяти агента:

- **Runs** по горизонтали, **knowledge-полосы** по вертикали
- **Lifecycle**: appeared → recalled → injected → reused → validated → contradicted → invalidated
- **Memory Lens filters**: strength, recency, activated, cross-scope, terminal state
- **Semantic zoom**: scope lanes → knowledge rows → episode detail
- **Forensic panel**: formation evidence, activation chain, lineage
- **Conflicts lens**: supersession arrows (K82 → K73)
- **Population lane**: active memory count over time
- **Shareable URLs**: full investigation state in URL

## Разработка

```sh
make fmt          # gofmt
make test         # go test ./...
make race         # go test -race ./...
make build        # bin/temporality-journal + bin/temporality-agent-kernel
make debugger-test
make debugger-build
```

Debugger dev-сервер (`npm --prefix debugger run dev`) проксирует `/api`
на `localhost:8080` (journal) и `/kernel-api` на `localhost:8090`.

## Схемы и контракты

- Схема события: [`schemas/temporality.event.v1.schema.json`](schemas/temporality.event.v1.schema.json)
- Контракты: [`docs/temporality-contract.md`](docs/temporality-contract.md)
- Knowledge model: [`docs/knowledge-model.md`](docs/knowledge-model.md)
- Event model: [`docs/event-model.md`](docs/event-model.md)
- Architecture: [`docs/architecture.md`](docs/architecture.md)
- Roadmap: [`docs/ROADMAP.md`](docs/ROADMAP.md)
