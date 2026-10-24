---
order: 12
section: Справка
title: Справочник
description: Переменные окружения, порты, страницы интерфейса и ссылки на подробные документы.
---

# Справочник

## Переменные окружения (основные)

| Переменная | По умолчанию | Описание |
|---|---|---|
| `TEMPORALITY_MODEL_BASE_URL` / `_ID` / `_API_KEY` | — | Провайдер по умолчанию (обязательны) |
| `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | `16384` (код) / `1024` (compose) | Лимит генерации; reasoning-моделям — с запасом. Compose-стек по умолчанию ставит 1024 |
| `TEMPORALITY_MODEL_TIMEOUT` | `180s` | Базовый таймаут HTTP-запроса к провайдеру |
| `TEMPORALITY_MODEL_STREAM_IDLE_TIMEOUT` | `90s` | Тишина в стриме между токенами |
| `TEMPORALITY_MODEL_STREAM_NO_PROGRESS_TIMEOUT` | `4m` | Стрим жив, но прогресса нет |
| `TEMPORALITY_MODEL_TEMPERATURE` / `_REASONING` | — | Переопределение параметров запроса |
| `TEMPORALITY_MODEL_ACTIVITY_TIMEOUT` | `20m` | Потолок одной модельной активности |
| `TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT` | `2m` | Ожидание первого токена (адаптивно +60с/МБ промпта) |
| `TEMPORALITY_TOOL_ACTIVITY_TIMEOUT` | `16m` | Потолок активности `run_tool` |
| `KERNEL_TOOL_MAX_SECONDS` | `900` | Потолок одной команды песочницы |
| `KERNEL_EMBEDDINGS_MODEL` | `intfloat/multilingual-e5-small` | Модель семантического слоя знаний (ин-процесс, rembed) |
| `KERNEL_EMBEDDING_WORKERS` | — | Параллелизм фоновых embed-вычислений |
| `KERNEL_PROVIDER_MODELS_REFRESH` | `24h` | Цикл фоновой синхронизации каталога моделей провайдера (`0` — выключить) |
| `KERNEL_SUPERVISOR_TRIAGE_INTERVAL` | `2m` | Периодичность триажа упавших ранов (значения вне допустимого диапазона откатываются к дефолту — выключателя нет) |
| `KERNEL_SANDBOX_ROOT` | `./.sandbox` (код) / `/srv/temporality/sandbox` (compose) | Корень персистентных воркспейсов. Не ставь `/tmp` — очистка при ребуте узла уничтожит воркспейсы |
| `KERNEL_SANDBOX_PIDS` / `KERNEL_SANDBOX_SCRATCH_SIZE` | — | Лимиты контейнера песочницы: процессы и размер scratch |
| `KERNEL_DOCKER_BINARY` | `docker` | Путь к docker CLI |
| `KERNEL_SESSION_TTL` | `12h` | Скользящий TTL браузерной сессии |
| `KERNEL_SESSION_ABSOLUTE_TTL` | `168h` | Абсолютный потолок сессии |
| `KERNEL_COOKIE_SECURE` | `false` | Флаг Secure у сессионной cookie (включай за HTTPS) |
| `KERNEL_WEB_SEARCH_PER_MIN` / `KERNEL_BROWSER_PER_MIN` | `30` / `60` | Rate-лимиты поиска и браузера |
| `KERNEL_WEB_FETCH_ALLOW_PRIVATE` | `false` | Разрешить `web_fetch` в приватные сети (SSRF-защита; включай осознанно) |
| `KERNEL_SECRET_CACHE_TTL_SECONDS` | — | Сколько разрешённое значение секрета живёт в памяти ядра |
| `KERNEL_MCP_COMMAND` / `_ARGS` / `_ALLOW` / `_APPROVAL` / `_SERVER_NAME` | — | Внешний MCP-сервер: команда, аргументы, разрешённые инструменты, подтверждение |
| `KERNEL_STORAGE_DEFAULT_QUOTA_BYTES` / `KERNEL_STORAGE_SWEEPER` | — | Квоты и очистка хранилища песочниц |
| `TEMPORALITY_API_TOKEN` | автопровижин | Bearer-токен kernel → journal |
| `TEMPORALITY_OPERATIONS_WINDOW` | `7d` | Окно для эксплуатационных свёрток |
| `KERNEL_UI_URL` | — | Базовый URL UI (письма/ссылки) |
| `ARTIFACT_SUBMIT_PER_DAY` | `10` | Квота отправок артефактов на ревью в сутки на ран |
| `ARTIFACT_REVIEW_TIMEOUT` | `72h` | Дедлайн ревью артефакта: напоминание согласующим, эскалация главе отдела — через два таймаута; `0` выключает свип |
| `KERNEL_SECRET_MASTER_KEY` | — | Ключ шифрования секретов (`openssl rand -hex 32`) |
| `KERNEL_AUTH_TOKENS` / `JOURNAL_AUTH_TOKENS` | пусто | Bearer-токены (`token:user:role:projects`; пусто = auth off) |
| `KERNEL_RUN_QUOTAS` | пусто | Суточные квоты запусков: `project:limit`, `*` — дефолт |
| `KERNEL_MODEL_PRICES` | пусто | Цены токенов: `model:prompt:completion` |
| `KERNEL_INTAKE_MAX_PER_HOUR` | `10` | Лимит канальных сообщений на отправителя |
| `KERNEL_INTAKE_RETENTION_DAYS` | `30` | Срок хранения тел входящих |
| `KERNEL_SEARXNG_URL` | `http://searxng:8080` | Метапоиск для `web_search` (пусто = off) |
| `KERNEL_BROWSER_CDP_URL` | lightpanda | Headless-браузер для `browser` (пусто = off) |
| `KERNEL_CHANNEL__MATRIX__*`, `__TELEGRAM__*`, `__MATTERMOST__*`, `__SLACKBOT__*` | — | Транспорты каналов |

Полный список с семантикой — в корневом `README.md` и `docs/runbook.md`.

## Порты

| Сервис | Порт |
|---|---|
| UI (debugger) | `3000` |
| Journal API | `8080` |
| Agent Kernel API | `8090` |
| Temporal UI | `8233` |
| SearXNG | `127.0.0.1:8889` |

## Страницы интерфейса

| Путь | Раздел |
|---|---|
| `/dashboard` | Дашборд: health, модели, проекты, качество, саги, песочницы |
| `/supervisor` | Очередь супервайзера: триаж упавших ранов, кейсы, кандидаты в навыки |
| `/workspace` | Чат с агентом |
| `/agents` | Запуски и трассы |
| `/knowledge` | Знания: карта, поиск, досье, противоречия |
| `/experience` | Таймлайн опыта |
| `/storage` | Хранилище песочниц (файлы) |
| `/agent-config` · `/teams` | Агенты и команды |
| `/skills` · `/artifacts` | Навыки и артефакты |
| `/inbound` · `/triggers` · `/channels` | Входящие, триггеры, каналы |
| `/providers` · `/secrets` · `/sandbox` | Провайдеры, секреты, песочницы |
| `/users` · `/org` | Пользователи и оргструктура |
| `/mcp` | Состояние внешнего MCP-сервера |

## Глоссарий

- **Ран** — один запуск агента над задачей (durable workflow).
- **Дополнение (addendum)** — сообщение пользователя, долитое в выполняющийся ран.
- **Поправка (amendment)** — канальное сообщение, продолжающее существующую задачу.
- **Сага** — отслеживаемая длительная работа через несколько ранов и агентов.
- **Знание** — утверждение с происхождением, статусом и историей.
- **Подсказка (hint)** — компактная выдержка опыта, подставляемая агенту.
- **Навык** — версионируемая процедура с контрактом и оценками.
- **Артефакт** — справочный документ в реестре, подгружаемый по требованию.
- **Reconcile** — ручное разрешение неопределённой операции (произошло/не произошло).
- **Судья** — LLM-модель, оценивающая ответы в контуре качества.

## Инженерные документы

Архитектура и внутренние механизмы описаны в `docs/` репозитория: `architecture.md`
(общая), `event-model.md`, `knowledge-model.md`, `agent-quality-loop.md`,
`living-skills.md`, `agent-delegation.md`, `inbound-messages.md`,
`sandbox-security-matrix.md`, `failure-model.md`, `failure-reconciliation.md`,
`resilience-and-scale.md` (отказоустойчивость и границы масштаба: replay,
история воркфлоу, Continue-As-New), `runbook.md`
(эксплуатация) и другие.
