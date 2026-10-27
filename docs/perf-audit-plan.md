# План оптимизаций по итогам аудита производительности (ветка `perf/hot-path-audit`)

База аудита: полное чтение ядра (`kernel/agent/workflow.go`, `kernel/llm/client.go`,
`kernel/agent/activities.go`) + четыре параллельных аудита периферии (storage/outbox,
знания/активация, инструменты/MCP/sandbox, controlplane/каналы). Ключевые находки
перепроверены по коду. Оценки задержек — порядковые, не замеры.

Исходная точка: `main` = `924d873`, полный `go test ./...` зелёный
(эталон прогона зафиксирован перед началом работ).

## Правила безопасности

1. Базовая линия зелёная — после каждой фазы прогоняются целевые пакеты, в конце —
   полный `go test ./...` + `go vet` + `go test -race` по `kernel/llm`, `kernel/agent`,
   `kernel/mcpclient`.
2. Любое изменение порядка команд Temporal-воркфлоу (что ждём, что шедулим) — только
   под `workflow.GetVersion` (прецедент: `delegation-note-steering`, `model-schedule-to-start`).
3. Продакшн-данные: миграции только аддитивные (новые индексы), без изменения
   существующих колонок; append-only триггер `observation_events` не трогаем.
4. Что НЕ входит в эту ветку (отдельные ветки после стабилизации этой):
   контейнер на ран для sandbox (`docker exec` вместо `docker run`),
   партиционирование/retention `observation_events`, асинхронный ack вебхуков каналов,
   push вместо поллинга в UI-стримах, шардирование TokenBus, LocalActivity для emit.

## Фаза 1 — kernel/llm (низкий риск, каждый ход)

| # | Правка | Файлы |
|---|--------|-------|
| 1.1 | Гонка данных: `Client.jitter (*rand.Rand)` не потокобезопасен при конкурентных `Complete` из общих клиентов — заменить на потокобезопасный источник | `kernel/llm/client.go` |
| 1.2 | Порядок tool calls из стрима: копились в `map[int]*ToolCall`, обход без сортировки — сортировать по индексу | `kernel/llm/client.go` |
| 1.3 | `bufio.Scanner` max token 64 КиБ: большой SSE-чанк роняет стрим и переигрывает весь ход — поднять потолок до 1 МиБ | `kernel/llm/client.go` |
| 1.4 | 429: honor `Retry-After` (секунды или HTTP-date), cap существующим backoff-потолком | `kernel/llm/client.go` |
| 1.5 | Деградационные ретраи (strip `response_format`, strip-ladder из WIP) без sleep: срезание поля — не провайдерский ретрай, backoff не нужен | `kernel/llm/client.go` |
| 1.6 | Довести WIP «400-degradation ladder» до рабочего состояния: тест исправлен под реальность (`Complete` с `tools` даёт 3 срезаемых поля; `stream_options` в blocking-запросе отсутствует по определению; сервер должен возвращать валидный chat completion) + дублированный комментарий `compatSanitize` убран | `kernel/llm/client.go`, `client_test.go` |
| 1.7 | Тесты: конкурентный `Complete` под `-race`, порядок tool calls, длинная SSE-строка >64 КиБ, `Retry-After` | `kernel/llm/*_test.go` |

## Фаза 2 — горячий путь инструментов (latency каждого tool call)

| # | Правка | Файлы |
|---|--------|-------|
| 2.1 | `web_fetch`/`fetch_image`: один общий `http.Client` с SSRF-транспортом вместо нового транспорта на вызов (keep-alive, HTTP/2) | `kernel/agent/webtools.go`, `images.go` |
| 2.2 | Кэши URL/поиска: вытеснение протухших записей вместо полного сброса map после 64 записей | `kernel/agent/webtools.go` |
| 2.3 | Browser: убрать безусловный `sleep(300ms)` после клика (готовность ждёт `browserWaitReady`), один `currentTabURL` вместо двух, action-ctx вместо `context.Background()` | `kernel/agent/browser.go` |
| 2.4 | `read_file`: читать через `os.Open` + `io.LimitReader` (cap+1), бинарность по первым 8 КиБ — не грузить гигабайтные файлы целиком | `kernel/agent/filetools.go` |
| 2.5 | Sandbox: мемоизация `ensureNetwork` (кэш с сбросом при ошибке запуска) | `kernel/sandbox/docker.go` |

## Фаза 3 — журнал событий и знания (throughput + старт рана)

| # | Правка | Файлы |
|---|--------|-------|
| 3.1 | Outbox: батч-клейм (`LIMIT 50`, один CTE) → один POST пачки (ingest принимает до 100) → `markDelivered` одним `UPDATE ... = ANY`. Публикация чанками ≤100 | `kernel/outbox/store.go` |
| 3.2 | Outbox: `CleanupDelivered(ctx, olderThan)` для фоновой чистки delivered-строк (проводка в main — фаза 5) | `kernel/outbox/store.go` |
| 3.3 | Hints/explore: использовать существующий кэш проекции (`cachedKnowledge`, TTL 15c) вместо полной перезагрузки истории проекта; инвалидация только на `knowledge.*`-событиях | `observation/httpapi/server.go` |
| 3.4 | Эмбеддинги: фильтр по проекту в `ListKnowledgeEmbeddings` (сейчас грузятся все векторы всех проектов на каждый вызов hints) | `observation/postgres/store.go`, `observation/httpapi/server.go` |
| 3.5 | Гонка: `sortKnowledgeList` мутирует общий кэшированный слайс — сортировать один раз при заполнении кэша | `observation/httpapi/server.go` |
| 3.6 | Телеметрия активации: батч-вставка `hint.query`/`hint.offered` (pgx Batch), 500 на частичной записи не ронять весь вызов | `observation/httpapi/server.go`, `observation/postgres/store.go` |
| 3.7 | Индексы: `observation_events(event_id)`, `(run_id, occurred_at)`, `(occurred_at, received_at, source_id, event_id)` — аддитивная миграция | `observation/postgres/migrations/` |

## Фаза 4 — агентский цикл (replay-безопасно, gated)

| # | Правка | Файлы |
|---|--------|-------|
| 4.1 | `advertisedTools(&input)` вычислить один раз перед циклом ходов (input в цикле неизменен) | `kernel/agent/workflow.go` |
| 4.2 | Генерация title параллельно с knowledge hints (обе — activities) под `GetVersion("run-title-parallel")`; на реплеях старых историй — прежняя последовательность | `kernel/agent/workflow.go` |

## Фаза 5 — проводка и настройки воркера

| # | Правка | Файлы |
|---|--------|-------|
| 5.1 | Worker options из env: `MaxConcurrentActivityTaskPollers`/`MaxConcurrentWorkflowTaskPollers` (дефолт SDK — 2) | `cmd/agent-kernel/main.go` |
| 5.2 | Проводка `CleanupDelivered` (раз в час, батчами) | `cmd/agent-kernel/main.go` |
| 5.3 | SSE-курсор стрима рана: полный кортеж `(occurred_at, event_id)` вместо подзапроса по `event_id` (seq-scan на каждый полл 1.5 c) | `workspace/store.go`, `cmd/agent-kernel/main.go` |

## Отложено (вне этой ветки)

- Контейнер на ран + `docker exec` в sandbox (меняет lifecycle, требует дизайна по чистке).
- Персистентная MCP-сессия с lazy-redial (требует интеграционной проверки с реальными
  серверами; в этой ветке оставляем сессию на вызов).
- Асинхронный ack вебхуков каналов (поведенческое изменение контракта со Slack).
- Батч-emit / LocalActivity для событий (изменяет детерминизм воркфлоу, отдельная ветка
  с нагрузочным прогоном на реплеях).
- Retention/партиционирование `observation_events`, push-стриминг UI, шардирование TokenBus.

## Критерии приёмки

1. `go build ./...`, `go vet ./...` — чисто.
2. Полный `go test ./... -count=1` — зелёный (не хуже базовой линии).
3. `go test -race ./kernel/llm ./kernel/agent ./kernel/mcpclient` — чисто.
4. Новые тесты: strip-ladder (исправленный), race на `Complete`, порядок tool calls,
   длинная SSE-строка, `Retry-After`, батч-фрейминг outbox, eviction кэшей web,
   `read_file` на большом файле, мемоизация `ensureNetwork`, курсор SSE.
5. Ни одного изменения семантики ожиданий воркфлоу без `GetVersion`.
