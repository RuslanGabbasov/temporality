# Temporality

**Русский** · [English](README.en.md)

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
| Journal | `observation/`, `cmd/temporality-journal` | append-only события, проекция знаний, подбор подсказок, байтемпоральные запросы |
| Agent Kernel | `kernel/`, `cmd/agent-kernel` | долговременный harness агента на Temporal: модельный шлюз, MCP, песочница, подтверждения, делегирование |
| Workspace | `debugger/src/Workspace.tsx` | чат с агентом: SSE-стриминг, reasoning, диалоги, ветвление |
| Debugger | `debugger/` | UI наблюдения и анализа опыта агента |
| Control Plane | `controlplane/` | авторизация и RBAC, оргструктура и доступы, bearer-токены, область проектов |

### Страницы UI

| Путь | Назначение |
|---|---|
| `/workspace` | Чат с агентом — SSE-стриминг, reasoning, Markdown, диалоги |
| `/agent-config` | Управление агентами — CRUD, шаблоны, полная конфигурация |
| `/agents` | Трассы запусков — лента событий, извлечение траекторий |
| `/operations` | Неопределённые операции — пакетный reconcile, кнопки вердиктов |
| `/observability` | Знания — жизненный цикл, цепочки активации, паттерны, проекция |
| `/experience` | Таймлайн опыта — memory lens, семантический зум, ретроспективный аудит |
| `/skills` | Живые навыки — контракт, версии, выполнения, память, эволюция, сборщик навыков |
| `/mcp` | MCP-серверы — stdio / SSE / Streamable HTTP, привязка к агентам |
| `/providers` | Провайдеры моделей — OpenAI-совместимые эндпоинты |
| `/users` | Пользователи — RBAC, токены, каналы уведомлений |
| `/triggers` | Триггеры — cron, webhook, слушатели событий, execution identity |
| `/org` | Организация — дерево оргструктуры, роли на узлах, политики, эффективный доступ |

## Запуск

```sh
cp .env.example .env   # настроить TEMPORALITY_MODEL_* и опционально песочницу
docker compose up --build -d
```

Поднимаются: PostgreSQL, Journal (`:8080`), debugger (`:3000`), dev-сервер
Temporal (UI на `:8233`), Agent Kernel (`:8090`) и SearXNG (`127.0.0.1:8889`, метапоиск
для тула агентов `web_search`; отключается пустым `KERNEL_SEARXNG_URL`).

Остановить стек, сохранив данные:

```sh
make stack-stop
```

## Настройка модели

Ядро вызывает провайдера через OpenAI-совместимый Chat Completions API.

| Переменная | Описание | По умолчанию |
|---|---|---|
| `TEMPORALITY_MODEL_BASE_URL` | Корень API (с `/v1`) | — |
| `TEMPORALITY_MODEL_ID` | ID модели у провайдера | — |
| `TEMPORALITY_MODEL_API_KEY` | Ключ (пустой для Ollama) | — |
| `TEMPORALITY_MODEL_TEMPERATURE` | `0..2` | `0` |
| `TEMPORALITY_MODEL_TIMEOUT` | Go duration — общий лимит одной блокирующей попытки (стриминг его не использует) | `180s` |
| `TEMPORALITY_MODEL_STREAM_IDLE_TIMEOUT` | Максимум тишины между чанками стрима; медленная, но живая генерация не прерывается | `90s` |
| `TEMPORALITY_MODEL_STREAM_NO_PROGRESS_TIMEOUT` | Максимум тишины между чанками, несущими сгенерированные токены; защищает от шлюзов, которые держат соединение пустыми пингами (0 — отключить) | `4m` |
| `TEMPORALITY_MODEL_ACTIVITY_TIMEOUT` | Потолок одной активности `call_model` в Temporal (общий для стрим-попытки и блокирующего fallback с ретраями) | `20m` |
| `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | `64..1000000` | `16384` |
| `TEMPORALITY_MODEL_REASONING` | `off` / `exclude` / `low` / `medium` / `high` | — |

Для reasoning-моделей CoT считается в том же бюджете — ставьте с запасом
(например, `16384` или больше), иначе ответ может остаться пустым с
`finish_reason=length`. По умолчанию уже `16384`.

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

## Авторизация

Bearer-токены с RBAC и областью проектов. Пустые переменные = авторизация
выключена (только для локальной разработки).

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
| journal | чтение, подсказки | ingest | + invalidation | всё |
| kernel | GET runs | POST runs | + подтверждения, reconcile | всё |

В UI токен вводится через Login в шапке (хранится в localStorage).

## Секреты

Секрет — полноценный ресурс воркспейса (`docs/secrets-management.md`): метаданные
в `workspace_secret`, значение — в конвертном AES-256-GCM в
`workspace_secret_value`, расшифровывается только внутри процесса ядра.
Агент никогда не видит значения: ссылки `secret://<имя>` в env/заголовках
MCP-серверов и гранты на карточке агента разрешаются на границе исполнения.

- `KERNEL_SECRET_MASTER_KEY` — 32 байта hex/base64 (`openssl rand -hex 32`),
  поддерживает суффикс `_FILE`. Без ключа ядро стартует, но builtin-секреты не
  разрешаются.
- Провайдеры: `builtin` (шифруем в БД) и `env` (переменная `SECRET_<ИМЯ>`
  процесса ядра — для локальной разработки).
- Выданные агенту секреты приходят в `run_command` как `SECRET_<ИМЯ>`;
  тул `secret_list` показывает только имена и описания.
- В выдаче агенту секрета пишутся события `secret.issued` / `secret.denied`;
  журнал на ingest дополнительно вымарывает известные паттерны токенов
  (ghp_*, AKIA*, xoxb-*, sk-*, Bearer, PRIVATE KEY) — вторая линия обороны.

## Каналы уведомлений

Когда агент вызывает `ask_human`, ядро доставляет вопрос получателю по его
предпочтительному каналу (см. `docs/triggers-and-escalations.md` §6-7). Каналы —
часть профиля пользователя и настраиваются самостоятельно в меню
пользователя → «Каналы уведомлений». Встроенный канал — почтовый ящик в
приложении (Runs → вопросы); внешние транспорты включаются переменными ядра:

```sh
# .env
KERNEL_MATRIX_HOMESERVER=https://matrix.org       # homeserver бота ядра
KERNEL_MATRIX_ACCESS_TOKEN=syt_...                # токен бота (состоит в комнатах пользователей)
KERNEL_TELEGRAM_BOT_TOKEN=123456:ABC...           # токен бота для Bot API
KERNEL_UI_URL=http://localhost:3000               # для ссылок в уведомлениях
```

Адрес канала: для Matrix — ID комнаты (`!room:matrix.org`), для Telegram —
chat ID. Доставка не блокирует выполнение: сбой транспорта фиксируется
событием `notification.sent` со статусом `failed`, а вопрос остаётся
доступным в UI. Ответ всегда происходит в приложении; внешние каналы —
только доставка вопроса.

## Agent Kernel

Долговременный workflow на Temporal: вызовы модели, подтверждения (approvals),
предложения `remember`, автоматические verification-наблюдения, outbox с
доставкой at-least-once, MCP (stdio / SSE / Streamable HTTP), песочница
команд в Docker.

Делегирование: `delegate` — параллельные субагенты, `plan` — DAG-план между
агентами с зависимостями и предикатами (`docs/agent-delegation.md`).

Живые навыки: инструменты `skill_search` / `skill_inspect` / `skill_validate` /
`skill_history` / `skill_executions` / `skill_memory` / `skill_propose`
(`docs/living-skills.md`).

Также: подсказки знаний и извлечение знаний из траекторий, триггеры
(cron/webhook/listeners), вопросы человеку (`ask_human`) и политики
организации (ограничения моделей/MCP/токенов/сети/песочницы).

### Песочница

`KERNEL_SANDBOX_ROOT` + `KERNEL_SANDBOX_IMAGE` — изолированный контейнер для
`run_command`: без сети (или `bridge`, если `network_access: true`),
read-only root, сброшенные capabilities, лимиты ресурсов, ограниченный
вывод. Персистентный кэш тулчейнов на проект монтируется в `/cache`
(GOCACHE/GOMODCACHE) — переживает `git clean` и пересоздание воркспейса.

Нативные файловые инструменты `write_file` / `read_file` / `edit_file`
выполняются на стороне ядра прямо по воркспейсу: без шелла, без лимитов
аргументов команды, с атомарной записью и проверками symlink. Правки файлов
предпочтительны через них, а не heredoc-ами через `run_command`.

Образ запинен по sha256-digest и запускается с `--pull=never`
(`docs/sandbox-security-matrix.md`), поэтому локальный Docker должен содержать
именно тот образ, на который указывает digest. После правки
`sandbox/Dockerfile` или чистки образов (`docker system prune`) запускайте
`./sandbox/build.sh` — он пересоберёт образ, обновит `KERNEL_SANDBOX_IMAGE`
в `.env` и пересоздаст agent-kernel. Без этого `run_command` падает с
exit 125 «No such image».

### MCP

Основной способ — реестр MCP-серверов в workspace (`/mcp`): транспорт stdio,
SSE или Streamable HTTP, привязка серверов к агентам и панель инструментов
в карточке агента (включая тумблеры доступности полученных из MCP
инструментов).

Переменные `KERNEL_MCP_COMMAND`, `KERNEL_MCP_ARGS` (JSON-массив),
`KERNEL_MCP_ALLOW` (список tools), `KERNEL_MCP_APPROVAL` (подмножество,
требующее подтверждения человеком) — legacy-вариант для запуска ядра без
workspace.

### Согласование сбоев

`tool.failed` несёт семантику эффекта: `effect=none` (отказ до исполнения) и
`effect=uncertain` (сбой на границе исполнения). Инструменты только для
чтения (read_file, search, grep) автоматически получают `effect=none`.

Неурегулированные операции видны на `/operations` с пакетным reconcile.

```sh
curl 'http://localhost:8090/v1/agent/operations?project=repo-a' -H "Authorization: Bearer $TOKEN"
curl -X POST http://localhost:8090/v1/agent/operations/reconcile \
  -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"project":"repo-a","run_id":"...","operation_id":"...","effect":"occurred","note":"verified","actor_id":"ops","started_event_id":"..."}'
```

## Траектории и опыт

Извлечение траекторий строит из потока событий структурированные данные:
ходы, вызовы инструментов, события знаний, повторяющиеся паттерны. Проекция
опыта агрегирует траектории между запусками: паттерны инструментов,
жизненный цикл знаний, области, статистику успеха.

| Endpoint | Описание |
|---|---|
| `GET /v1/workspace/runs/{id}/trajectory` | Извлечение по одному запуску |
| `GET /v1/workspace/projects/{id}/projection` | Агрегация по запускам |
| `GET /v1/workspace/runs/compare?a=X&b=Y` | Сравнение бок о бок |
| `GET /v1/workspace/runs/{id}/artifacts` | Переиспользуемые последовательности инструментов |
| `GET /v1/workspace/knowledge/{id}/chain` | Цепочка активации |
| `GET /v1/workspace/projects/{id}/chains` | Все цепочки |
| `GET /v1/workspace/runs/{id}/stream` | SSE-поток событий |
| `GET /v1/workspace/runs/{id}/stream` | + model.text_delta, model.reasoning |
| `GET /v1/agent/cost/task?project=P&task=T` | Бюджет задачи: все раны (продолжения чата + дерево делегирования) с суммой времени/токенов/стоимости |

## Workspace

Чат с агентом через SSE-стриминг. Каждое сообщение → task + run. Повторные
сообщения переиспользуют ту же task (PATCH prompt). История диалога
передаётся как контекст. Выбор агента при создании чата. Ветвление — форк из
любой точки.

Возможности:
- SSE-стриминг: model.text_delta, model.reasoning, события инструментов
- Markdown-рендеринг ответов (включая ответы субагентов)
- Блок reasoning/thinking (сворачиваемый)
- Сохранение диалогов (localStorage)
- Шаблоны агентов (Coder, Reviewer, Researcher, DevOps, QA)
- Настройки проекта (агент по умолчанию, модель по умолчанию)
- Дерево делегирования и DAG-план: прогресс субагентов прямо в чате
- Автозаголовки запусков — краткое имя генерируется отдельным лёгким вызовом модели

## Control Plane

- **Авторизация**: bearer-токены `token:subject:role:projects`
- **Оргструктура**: дерево оргструктуры, наследуемая видимость ресурсов сверху вниз, роли на узлах, явное членство в проектах, execution identities, политики — `docs/org-structure.md`
- **Квоты**: `KERNEL_RUN_QUOTAS="*:50,project:200"` — лимит запусков в день
- **Секреты**: конвенция `<VAR>_FILE` — значение берётся из файла
- **Провайдеры**: OpenAI-совместимые эндпоинты моделей, маскирование API-ключей
- **Пользователи**: CRUD с генерацией токенов, роли RBAC

## Таймлайн опыта

`/experience` — визуализация жизни памяти агента:

- **Запуски** по горизонтали, **полосы знаний** по вертикали
- **Жизненный цикл**: appeared → recalled → injected → reused → validated → contradicted → invalidated
- **Фильтры memory lens**: strength, recency, activated, cross-scope, terminal state
- **Семантический зум**: полосы областей → строки знаний → детали эпизода
- **Панель ретроспективного аудита**: свидетельства формирования, цепочка активации, происхождение
- **Линза конфликтов**: стрелки замещения (K82 → K73)
- **Полоса популяции**: количество активной памяти во времени
- **Ссылки с состоянием**: полный статус исследования в URL

## Разработка

```sh
make fmt          # gofmt
make test         # go test ./...
make race         # go test -race ./...
make build        # bin/temporality-journal + bin/agent-kernel
make debugger-test
make debugger-build
```

Dev-сервер debugger (`npm --prefix debugger run dev`) проксирует `/api` на
`localhost:8080` (journal) и `/kernel-api` на `localhost:8090`.

## Схемы и контракты

- Схема события: [`schemas/temporality.event.v1.schema.json`](schemas/temporality.event.v1.schema.json)
- Контракты: [`docs/temporality-contract.md`](docs/temporality-contract.md)
- Модель знаний: [`docs/knowledge-model.md`](docs/knowledge-model.md)
- Модель событий: [`docs/event-model.md`](docs/event-model.md)
- Архитектура: [`docs/architecture.md`](docs/architecture.md)
- Управление секретами: [`docs/secrets-management.md`](docs/secrets-management.md)
- Роадмап: [`docs/ROADMAP.md`](docs/ROADMAP.md)
