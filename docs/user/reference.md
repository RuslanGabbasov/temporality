---
order: 12
section: Администрирование
title: Справочник
description: Переменные окружения, порты, страницы интерфейса и ссылки на подробные документы.
---

# Справочник

## Переменные окружения (основные)

| Переменная | По умолчанию | Описание |
|---|---|---|
| `TEMPORALITY_MODEL_BASE_URL` / `_ID` / `_API_KEY` | — | Провайдер по умолчанию (обязательны) |
| `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | `16384` | Лимит генерации; reasoning-моделям — с запасом |
| `TEMPORALITY_MODEL_ACTIVITY_TIMEOUT` | `20m` | Потолок одной модельной активности |
| `TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT` | `2m` | Ожидание первого токена (адаптивно +60с/МБ промпта) |
| `TEMPORALITY_TOOL_ACTIVITY_TIMEOUT` | `16m` | Потолок активности `run_tool` |
| `KERNEL_TOOL_MAX_SECONDS` | `900` | Потолок одной команды песочницы |
| `KERNEL_SANDBOX_ROOT` | `/tmp` | Корень персистентных воркспейсов |
| `KERNEL_DOCKER_BINARY` | `docker` | Путь к docker CLI |
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
`sandbox-security-matrix.md`, `failure-reconciliation.md`, `runbook.md`
(эксплуатация) и другие.
