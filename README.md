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
| `/dashboard` | Дашборд здоровья — health-полоса, провайдеры/модели, сводка по проектам, петля качества, саги и задачи с бюджетом, наблюдаемость знаний |
| `/workspace` | Чат с агентом — SSE-стриминг, reasoning, Markdown, диалоги |
| `/agent-config` | Управление агентами — CRUD, шаблоны, полная конфигурация |
| `/agents` | Трассы запусков — лента событий, извлечение траекторий, инлайн-reconcile неопределённых операций |
| `/knowledge` | Исследование знаний — карта областей, фасетный поиск, досье с первоисточниками, граф связей, битемпоральная история, противоречия, сессии исследования, воспроизведение исследования агента |
| `/experience` | Таймлайн опыта — memory lens, семантический зум, ретроспективный аудит |
| `/skills` | Живые навыки — контракт, версии, выполнения, память, эволюция, сборщик навыков |
| `/artifacts` | Артефакты — реестр справочных документов (регламенты, инструкции), привязка к навыкам |
| `/mcp` | MCP-серверы — stdio / SSE / Streamable HTTP, привязка к агентам |
| `/secrets` | Секреты — хранилище (builtin/env), гранты агентам и провайдерам |
| `/providers` | Провайдеры моделей — OpenAI-совместимые эндпоинты |
| `/users` | Пользователи — RBAC, токены, каналы уведомлений |
| `/channels` | Каналы доставки — настройки транспортов (Matrix, Telegram, Mattermost, Slack) |
| `/inbound` | Входящие — сообщения из каналов: конвейер intake, статусы, аудит |
| `/triggers` | Триггеры — cron, webhook, слушатели событий, execution identity |
| `/org` | Организация — дерево оргструктуры, роли на узлах, политики, эффективный доступ |

Ленты запусков, операций и вопросов — глобальные: проект выступает фильтром,
а не разделом (`docs/activity-scoping.md`).

## Запуск

```sh
cp .env.example .env   # настроить TEMPORALITY_MODEL_* и опционально песочницу
docker compose up --build -d
```

Поднимаются: PostgreSQL, Journal (`:8080`), debugger (`:3000`), dev-сервер
Temporal (UI на `:8233`), Agent Kernel (`:8090`), SearXNG (`127.0.0.1:8889`,
метапоиск для тула агентов `web_search`; отключается пустым
`KERNEL_SEARXNG_URL`) и headless-браузер Lightpanda для тула `browser`
(отключается пустым `KERNEL_BROWSER_CDP_URL`).

Чистая установка настраивается мастером онбординга: проект → администратор →
провайдер модели (ключ сохраняется в секретах) → агент → сборка песочницы.
Основная навигация скрыта, пока не создан первый проект.

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
| `TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT` | Максимум ожидания первого токена: провайдер принял запрос, но молчит (стоит в очереди). Watchdog гасит попытку, и ретрай Temporal открывает свежее соединение — автоматический аналог ручного «Подтолкнуть». Бюджет адаптивный: +60с за каждый мегабайт промпта, чтобы легитимный prefill большого контекста не срезался (0 — отключить) | `2m` |
| `TEMPORALITY_MODEL_ACTIVITY_TIMEOUT` | Потолок одной активности `call_model` в Temporal (общий для стрим-попытки и блокирующего fallback с ретраями); всего до 3 попыток в пределах общего бюджета (3× таймаута + 5 минут) | `20m` |
| `TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS` | `64..1000000` | `16384` |
| `TEMPORALITY_MODEL_REASONING` | `off` / `exclude` / `low` / `medium` / `high` | — |

Для reasoning-моделей CoT считается в том же бюджете — ставьте с запасом
(например, `16384` или больше), иначе ответ может остаться пустым с
`finish_reason=length`. По умолчанию уже `16384`.

### Маршрутизация моделей по провайдерам

Переменные `TEMPORALITY_MODEL_*` задают провайдера по умолчанию. Дополнительно
ядро читает реестр провайдеров из workspace (раздел «Провайдеры» в UI): base URL,
API-ключ и список моделей. Если модель запуска не совпадает с моделью
по умолчанию, ядро ищет провайдер, у которого эта модель есть в списке
(с учётом видимости по оргединицам проекта), и направляет вызов туда;
резолюция кешируется на 60 секунд. Если модель не находит ни один провайдер —
вызов уходит провайдеру по умолчанию с подменой имени модели (прежнее
поведение).

Поле ключа провайдера понимает три формы: `secret://имя` — ссылка на секрет
из хранилища Temporality (рекомендуется, см. раздел «Секреты»; подтаблица
`#key` выбирает поле JSON-значения), имя переменной окружения в UPPER_SNAKE
(напр. `Z_AI_API_KEY`) или литеральный ключ (legacy — ключ лежит в БД открытым
текстом). Человекочитаемый ответ маршрутизации содержит `api_key_ref`, сам
ключ отдаётся только сервисному принципалу ядра.

Ручная проверка маршрута:

```sh
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8090/v1/workspace/model-config?model=mimo-v2.6-pro&project=myproject"
# {"provider_id":"...","base_url":"...","api_key_ref":"...","model":"mimo-v2.6-pro"}
```

Цены для отчётности по токенам задаются отдельно через `KERNEL_MODEL_PRICES`
(формат `model:prompt_per_1k:completion_per_1k`); непрофилированные модели
считаются бесплатными.

### Долгие прогоны на ноутбуке

Если хост уходит в сон посреди прогона, замораживаются и воркер, и Temporal.
На пробуждении модельный вызов переживает сон автоматически: стрим-ватчдог
рвёт мёртвое соединение, а Temporal ретраит активность свежей попыткой
(до 3 попыток, модельный вызов побочных эффектов не имеет, префикс промпта
кешируется через `prompt_cache_key`). Сон дольше общего бюджета всё же убьёт
ран. Чтобы ноутбук не засыпал во время долгих прогонов, держите в отдельном
терминале (macOS; пока он запущен, сон отключён):

```sh
caffeinate -dims
```

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
TEMPORALITY_API_TOKEN=token2   # ядро → journal (необязателен, см. ниже)
```

Если `TEMPORALITY_API_TOKEN` не задан (или отклоняется журналом), ядро на
старте пробует журнал и автоматически провиженит сервисный принципал
`temporality-kernel` (writer, все проекты) в общей БД — журнал подхватывает
его при очередной перезагрузке токенов. Это закрывает чистую установку без
ручных токенов: после онбординга журнал включает авторизацию, а ядро уже
имеет допустимый токен. Открытый журнал (без пользователей и статических
токенов) остаётся открытым — провижининг срабатывает только после явного
401/403.

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
# .env — канонические имена (KERNEL_CHANNEL__<КАНАЛ>__<ПОЛЕ>)
KERNEL_CHANNEL__MATRIX__HOMESERVER=https://matrix.org   # homeserver бота ядра
KERNEL_CHANNEL__MATRIX__ACCESS_TOKEN=syt_...            # токен бота (состоит в комнатах пользователей)
KERNEL_CHANNEL__TELEGRAM__BOT_TOKEN=123456:ABC...       # токен бота для Bot API
KERNEL_UI_URL=http://localhost:3000                     # для ссылок в уведомлениях
KERNEL_CHANNEL__MATTERMOST__BASE_URL=https://mm.corp    # self-hosted Mattermost
KERNEL_CHANNEL__MATTERMOST__TOKEN=...                   # токен бота/персональный (REST v4 + WebSocket)

# Двусторонние push-каналы (вебхуки приходят в ядро, путь — публичный через
# reverse proxy):
KERNEL_CHANNEL__SLACKBOT__BOT_TOKEN=xoxb-...            # Slack-приложение: токен бота
KERNEL_CHANNEL__SLACKBOT__SIGNING_SECRET=...            # подпись Events API (v0:ts:body HMAC-SHA256)
#   события: POST /v1/channels/slackbot/events
# Mattermost входящие принимает через WebSocket-стрим тем же токеном
# (включая личные сообщения) — отдельный вебхук и OUTGOING_TOKEN больше не нужны

# Легаси-имена работают как алиасы (одно и то же поле):
# KERNEL_MATRIX_HOMESERVER, KERNEL_MATRIX_ACCESS_TOKEN,
# KERNEL_TELEGRAM_BOT_TOKEN, KERNEL_WEBHOOK_SECRET
```

Тот же набор настроек редактируется в UI (Настройки → Каналы доставки) без
перезапуска: значения из БД перекрывают env по-польному. Секреты обратно не
возвращаются — только признак «установлен» и хвост из 4 символов.

Адрес канала: для Matrix — ID комнаты (`!room:matrix.org`), для Telegram —
chat ID. Доставка не блокирует выполнение: сбой транспорта фиксируется
событием `notification.sent` со статусом `failed`, а вопрос остаётся
dоступным в UI. Ответ всегда происходит в приложении; внешние каналы —
только доставка вопроса.

## Входящие сообщения (intake)

Сообщение пользователя по собственной инициативе в канале запускает конвейер
intake (классификация → уточнение → задача), см. `docs/inbound-messages.md`.
Включается по проекту и по умолчанию выключено: проект начинает принимать
канальные задачи только после тумблера «Принимать задачи из каналов» в
настройках проекта. При апгрейде включёнными остаются только проекты, в
которые уже маршрутизировались сообщения, — рабочий канал не умирает молча.

Дополнительные ограничения:

- **Политика орг-области**: `intake_mode = deny` в политике узла запрещает
  канальные задачи для всего поддерева; потомок не может ослабить запрет
  предка. Включение — только на проекте, «allow» в политике не существует.
- **Лимит отправителя**: `KERNEL_INTAKE_MAX_PER_HOUR` (по умолчанию 10, 0 —
  выключить) — превышение отклоняется с вежливым ответом в канал и событием
  `intake.rejected` (reason `rate_limited`) вместо запуска intake-рана.
- **Хранение тел (PII)**: `KERNEL_INTAKE_RETENTION_DAYS` (по умолчанию 30,
  0 — хранить вечно) — по истечении срока тело и вложения вымарываются,
  строка остаётся как звено аудиторской цепочки (пометка в «Входящих»).
- **Аудит**: `GET /v1/intake/audit?sender=<id>` (operator) — цепочка
  сообщение → intake-ран → ран задачи с исходами, без тел сообщений.

## Agent Kernel

Долговременный workflow на Temporal: вызовы модели, подтверждения (approvals),
предложения `remember`, автоматические verification-наблюдения, outbox с
доставкой at-least-once, MCP (stdio / SSE / Streamable HTTP), песочница
команд в Docker.

Делегирование: `delegate` — параллельные субагенты, `plan` — DAG-план между
агентами с зависимостями и предикатами (`docs/agent-delegation.md`).

Саги: явно зарегистрированная длительная работа, которая живёт дольше одного
рана и переходит между агентами. Инструменты `saga_register` / `saga_attach` /
`saga_list`; привязка живёт на уровне задачи — продолжения чата и делегированные
дети присоединяются к саге автоматически. Вкладка «Саги» дашборда (`/dashboard`)
показывает саги и задачи со сводкой бюджета (`docs/sagas.md`).

Веб-исследования: `web_fetch` (страница → readability → markdown), `web_search`
(self-hosted SearXNG), `view_image` / `fetch_image` (изображения как
vision-блоки) и `browser` — headless-браузер за CDP-контрактом для JS-страниц
(`docs/web-research.md`).

Живые навыки: инструменты `skill_search` / `skill_inspect` / `skill_validate` /
`skill_history` / `skill_executions` / `skill_memory` / `skill_propose`
(`docs/living-skills.md`).

Реестр артефактов: справочные документы (регламенты, инструкции, чек-листы),
на которые манифест навыка ссылается, но не включает в промпт — агент
подгружает тело по требованию через `artifact_search` / `artifact_read`
чанками с аудитом `artifact.read`; url-артефакты ядро проксирует и кэширует
само (`docs/artifact-registry.md`).

Контур качества агентов: применение предложений определений — только поверх
свежего зелёного оценочного прогона (golden-кейсы из продакшна, HITL-вердикты,
LLM-судья с калибровкой, дашборд качества по версиям) —
`docs/agent-quality-loop.md`.

Также: подсказки знаний и извлечение знаний из траекторий, триггеры
(cron/webhook/listeners), вопросы человеку (`ask_human`) и политики
организации (ограничения моделей/MCP/токенов/сети/песочницы).

Если провайдер «завис» и вызов модели молча ждёт первый токен, доступен
ручной nudge: `POST /v1/agent/runs/{runID}/nudge?project=…` (writer) —
сигнал отменяет висящий запрос и сразу повторяет ход (вызовы модели чистые,
промпт-префикс стабилен через `prompt_cache_key`, повтор безопасён).
Вмешательство фиксируется событием `model.nudged` в журнале.

Тот же сценарий автоматизирован watchdog'ом первого токена
(`TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT`, по умолчанию 2м): если провайдер
молчит до первого токена дольше бюджета, попытка гасится без блокирующего
fallback, а ретрай Temporal открывает свежее соединение. Каждое такое
восстановление отражается в трейсе и в чате маркером `model.attempt`
(reason `stream_timeout`) — долгая тишина больше не выглядит как зависание.

### Песочница

`KERNEL_SANDBOX_ROOT` + `KERNEL_SANDBOX_IMAGE` — изолированный контейнер для
`run_command`: без сети (или `bridge`, если `network_access: true`),
read-only root, сброшенные capabilities, лимиты ресурсов, ограниченный
вывод. Персистентный кэш тулчейнов на проект монтируется в `/cache`
(GOCACHE/GOMODCACHE) — переживает `git clean` и пересоздание воркспейса.
Без настройки корень по умолчанию — `/tmp` (работает без подготовки, но
переживает перезагрузки только при явном `TEMPORALITY_SANDBOX_ROOT` в
`.env`, см. `.env.example`).

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

Доступ к Docker-сокету ядро получает автоматически: контейнер стартует от
root, entrypoint определяет группу сокета (`root:docker` на Linux,
`root:root` на Docker Desktop / OrbStack) и дропается до непривилегированного
пользователя с этой группой — ничего настраивать не нужно.
`TEMPORALITY_DOCKER_GID` в `.env` остаётся для экзотических сетапов
(переопределяет автоопределение). Если сборка песочницы из онбординга
падает с permission denied — пересоздайте контейнер с текущим образом:
`docker compose up -d --build agent-kernel`.

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

Неурегулированные операции всплывают прямо в трейсе запуска на `/agents` — карточки вердиктов у упавших шагов, пометка рангов с проблемами и фильтр по ним.

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
| `GET /v1/workspace/runs/{id}/stream` | SSE-поток событий; `?tail=1` — без реплея всей истории: последние ~50 событий + только новые (для переподключения к долгому рану) |
| `GET /v1/workspace/runs/{id}/stream` | + model.text_delta, model.reasoning |
| `GET /v1/agent/cost/task?project=P&task=T` | Бюджет задачи: все раны (продолжения чата + дерево делегирования) с суммой времени/токенов/стоимости |
| `GET /v1/agent/cost/project-budget?project=P` | Бюджет проекта: тоталы и построчная разбивка по сагам и задачам (время/токены/стоимость/approvals) |

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
- Заголовки запусков: тайтл задачи (первая строка сообщения в чате) → краткое имя лёгким вызовом модели → эвристическая первая строка промпта; медленный провайдер не оставляет ран без имени

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
- Контур качества агентов: [`docs/agent-quality-loop.md`](docs/agent-quality-loop.md)
- Реестр артефактов: [`docs/artifact-registry.md`](docs/artifact-registry.md)
- Саги: [`docs/sagas.md`](docs/sagas.md)
- Веб-исследования: [`docs/web-research.md`](docs/web-research.md)
- Область активности: [`docs/activity-scoping.md`](docs/activity-scoping.md)
- Роадмап: [`docs/ROADMAP.md`](docs/ROADMAP.md)
