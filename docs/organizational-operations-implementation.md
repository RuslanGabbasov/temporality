# Реализация: Organizational Operations

Дата: 16.11.2026. Реализация `docs/temporality-organizational-operations-spec.md`
(этапы P0–P5 частично, P6-lite). Обследование —
`docs/organizational-operations-survey.md`.

## Карта «спецификация → код»

| Спецификация | Реализация |
|---|---|
| §4 Organizational Activity (проекция, watermark, идемпотентность, оргатрибуция, active Run) | `cmd/agent-kernel/orgactivity.go` — fold-цикл (1 мин): `types=` allow-list жизненного цикла (tool/model/turn-события не публикуются), watermark `org_activity_sync` + slack 1s + grace-пере-fold каждые 10 проходов, якорь `(source_id, event_id)` PK. Атрибуция — снапшот на момент запуска: exec_context → родитель → actor → проект → root, кэш на проход; хранится в `org_active_run` и не пере-resolve'ится (перевод историю не двигает). |
| §4.2 события | Категории run/progress/plan/task/artifact/quality/human в `orgActivityCategories`; `run.waiting`/`run.resumed` не существуют в stream — ожидание выводится из `human.*`/`approval.*` (решение зафиксировано в survey). |
| §4.3 контракт | `workspace.OrgActivityEvent` (`workspace/orgactivity.go`): summary — только whitelisted поля, cap 200, control-символы схлопнуты; refs — whitelisted ids; source_ref — только provenance (без содержимого). |
| §5 настройки узла | `workspace/orgactivitysettings.go`: таблица `org_activity_setting(org_unit_id, key, value)`, резолв по цепочке предков (local → inherited → system → default) с `sources` для UI; 19 ключей §5; валидация значений. API: `GET/PUT/DELETE /v1/org/units/{unitID}/activity-settings` (`cmd/agent-kernel/orgboards.go`), PUT аудируется (`org.settings.changed`). Независимость фич: activity_enabled гейтит только проекцию; active-run snapshot ведётся всегда (Work Board/Management View без Awareness). |
| §6 Supervisor | `cmd/agent-kernel/orgsupervisor.go` + `kernel/agent/orgawareness.go`: per-node расписание `batch_interval_minutes` (по умолч. 10 мин), batch = события окна + active runs + recent deliveries + лимиты; пустой batch/нет живых адресатов → LLM не вызывается; решения через `RunOrgAwarenessBatch` (structured JSON, `completeParsed`); валидация решений: закрытый enum, цель обязана быть running-раном скоупа, источник обязан быть в батче, caps, дедуп-ключи. |
| §6.4/§8 идемпотентность | Unique-индексы: решения `(deduplication_key) WHERE decision<>'ignore'`, доставки `(deduplication_key, target_run_id)`; retry/reprocess не создаёт повторов; сбой батча не двигает окно узла (`org_activity_batch_state`), `Complete`/`Fail` транзакционны. |
| §7 steer-доставка | `startOrgAwarenessDeliveryLoop` (30 с): selected→queued→delivered через `agent.UserNoteSignal` (`SignalWorkflow(agent.WorkflowID(...))`) — безопасная точка steer (граница turn'а), исходный tool result не меняется. Фрейминг: явное «Organizational awareness … not a user request and not a new task», источник, причина, уровень деталей, маркер `[org-awareness:<delivery_id>]`. Завершённый Run → доставка `expired` (liveness-проверка + isWorkflowGone). Retry: attempts≤5, потом `failed`. |
| §8 consumed | `sweepOrgAwarenessConsumption`: корреляция `run.user_note` журнала по маркеру — `consumed` = steer попал в модельный контекст (не семантическое использование). `acted_upon` — только явный `action_ref` через API. |
| §7.1 запрос подробностей | `POST /v1/org/awareness/details`: уровни summary/findings/decisions/artifacts/trace; run-visibility проверка (`runVisibilityActors` + `childRunVisible`), trace — дополнительно `allow_trace_discovery` узла + аудит `org_awareness.details`. Trace отдаётся указателем на существующий journal API (без копирования). |
| §9 Activity Board | `GET /v1/org/activity`, `GET /v1/org/activity/event`, `GET /v1/org/active-runs`; фильтры периода/сотрудника/проекта/типа/статуса; UI `debugger/src/OrgActivityBoard.tsx` (группировка по run, настройки узла на странице). |
| §10/§13 Work Board | `GET /v1/org/work-view`: колонки выводятся из lifecycle ранов (urgent-latch = waiting только по событию human/approval; failed → review/quality gate + флаг); metrics §10.5; problems §13 (waiting_on_human / failed_run / no_movement — no_movement информационный, не блокировка). UI `OrgWorkBoard.tsx` — канбан + problems + карточка с раскрытием. |
| §11 Plan Graph | `GET /v1/org/plan-graph`: только подтверждённые связи (task binding, delegation.started, общий artifact); UI-секция в Work Board; per-run графы — существующий `debugger/src/PlanGraph.tsx` (не тронут). |
| §12 Awareness Flow | `GET /v1/org/awareness-flow` (delivery + batch + source_event в одной записи, фильтры §12); UI `OrgAwarenessFlow.tsx` — цепочка, 10 статусов, dismiss/follow-up/details, Batches-секция (manual batch, решения). |
| §15 API | Все операции §15 покрыты (`cmd/agent-kernel/orgboards.go`): list/get activity, active runs, create/get/process batches (process = manual batch, 202), decisions, list_run_awareness, delivery, details, follow-up (+dismiss). Конвенции: `gate.Allow`, `writeJSON/writeError`, org-скоуп через `visibleUnits`/`OrgVisible` (404, не 403). |
| §16 безопасность | Серверные проверки на каждом эндпоинте; summary без содержимого Trace; artifact ref ≠ право чтения (артефакты открываются существующими эндпоинтами с их RBAC); аудит настроек, dismiss/follow-up/чтения подробностей (`access_audit_log`, виден в `GET /v1/org/audit`). В steer указан источник и статус. |
| §17 НФТ | Пакет, а не LLM на событие; пустые batch без LLM; snapshot без чтения Trace; пагинация/лимиты во всех запросах; сбой прохода → retry, Journal/Run не блокируются; `ORG_OPERATIONS=off` выключает supervisor+delivery (проекция продолжается для досок). |
| §18 наблюдаемость | `org.supervisor.batch` journal events (batch_id, node, counts, model); таблицы `org_activity_batch(+-state)`, статусы доставок — источник метрик; объяснения — reason без chain-of-thought. |

## Что НЕ сделано (осознанно)

- P4 UI-детали: делегирования/точки возврата в раскрытии карточки — базовая
  карточка с ранами есть; интеграция `PlanGraph.tsx` в раскрытие — следующий
  шаг.
- P5 режимы Flow «история уведомлений выбранного Run» и «граф
  распространения» — реализованы фильтрами (target_run/source_run), полный
  граф — следующий шаг.
- P6 межплановая аналитика — сделан lite (общий artifact/task/delegation);
  пересечения по компонентам/репозиториям и маркировка гипотез — следующий
  шаг (компонентных refs в событиях сейчас нет).
- `run.waiting`/`run.resumed` эмиты в workflow — не добавлялись; ожидание
  выводится из существующих событий.
- Нагрузочные прогонки §22 — не выполнялись (нет стенда в сессии).

## Запуск/настройка

- Миграция: `migrations/workspace/0031_org_operations.up.sql` (применяется
  `MigrateAll` на старте).
- Loops: `RegisterOrgOperationsRoutes` в `main.go` запускает fold (1 мин),
  supervisor-пакеты (per-node 10 мин) и delivery worker (30 с).
- Kill-switch: `ORG_OPERATIONS=off|0|false` — supervisor и доставка off.
- Модель supervisor'а: стандартная цепочка `eval.judge.model` /
  `judgeModelClient`. Если модель не сконфигурирована — пакеты остаются
  `failed` без потерь событий (окно не двигается), обычные Run не
  затрагиваются.
- Включение: настройки узла через UI (Org Activity Board → Node activity
  settings) или `PUT /v1/org/units/{unitID}/activity-settings`; для работы
  Awareness — `agent_awareness_enabled=true`.

## Тесты

- `workspace/orgactivitysettings_test.go` — матрица наследования
  (default/local/inherited/system/nearest), валидация значений.
- `cmd/agent-kernel/orgoperations_test.go` — safety summary, маркеры, refs
  whitelist, exec_context-атрибуция, валидация решений (чужая цель,
  выдуманный источник, подавление vs update_awareness, нормализация enum),
  фрейминг steer (round-trip маркера), scope-резолв.
- `kernel/agent/orgawareness_test.go` — парсинг ответа модели
  (нормализация, обёртки, caps).
- Полный прогон: `go test ./workspace/ ./kernel/agent/ ./cmd/agent-kernel/`
  — зелёный; `go build ./...`, `go vet` — чисто; UI: `tsc -b`, `npm run
  build`, vitest 784 теста — зелёные.
