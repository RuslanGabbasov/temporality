# Техническое обследование: Organizational Operations

Дата: 16.11.2026. Выполнено до реализации `docs/temporality-organizational-operations-spec.md`
(§23 — обязательное обследование). Факты сверены с кодом, не с документами.

## 1. Контракт существующего Supervisor

- Supervisor — не workflow, а фоновые goroutine-циклы в `cmd/agent-kernel`:
  `startSupervisorTriageLoop` (`cmd/agent-kernel/supervisortriage.go:32`), волны A1/A3/B/C/reconcile
  на тике 2 мин (`KERNEL_SUPERVISOR_TRIAGE_INTERVAL`), kill-switch `KERNEL_SUPERVISOR_CASES=off`.
- Вход — журнал событий по HTTP (`GET /v1/observations/events`, bearer `TEMPORALITY_API_TOKEN`,
  читается на каждый проход). Хелперы: `supervisorEventPage`/`supervisorTypedEventPage`
  (`cmd/agent-kernel/supervisorqueue.go:340/353`), лимит страницы 500.
- Выход — события журнала `supervisor.verdict` / `supervisor.case` через durable outbox
  (`kernel/agent/activities.go`: `PublishSupervisorVerdict`, `PublishSupervisorCase` →
  `kernel_event_outbox` → `observation_events`). Ничего не исполняется автоматически.
- LLM: `judgeModelClient` (`cmd/agent-kernel/knowledgeareas.go:106`, настройка `eval.judge.model`),
  структурированный вызов `completeParsed[T]` (`kernel/agent/modeljson.go:64`).
- Пустой вход ⇒ нет LLM-вызова (прецедент: triage выходит при `len(failed)==0`).
- Дедуп: `SupervisorClusterKey` (нормализация digits→#, paths→~) и per-subject окна
  (`supervisorCaseDedupWindow = 7d`). Прямой образец для `deduplication_key` из ТЗ §6.4.

## 2. Steer: точки интеграции и гарантии

- Steer — Temporal-сигналы на workflow `AgentRun`: `kernel.user_note`
  (`agent.UserNoteSignal` → `agent.UserNote{Text, ActorID}`, `kernel/agent/workflow.go:42/621`),
  доставка `temporalClient.SignalWorkflow(ctx, agent.WorkflowID(sourceID, project, runID), "", …)`
  (`WorkflowID` — `workflow.go:3955`; sourceID проставляется `ws.RunSourceID`, `workspace/store.go:911`).
- Точка применения — граница turn'а: `appendUserNotes` (`workflow.go:1982`, 5560+) до построения
  следующего модельного запроса; история append-only, кэш-префикс стабилен. Уже завершённый
  модельный вызов не меняется; mid-call возможен только `kernel.nudge`.
- Очередь — сам канал сигналов Temporal (durable, replay-безопасный). Внутри workflow дедуп
  по `RequestID` для agent-сообщений; user notes не дедуплицируются — дедуп обязан делать
  ядро (наша таблица доставок).
- Run завершён до доставки ⇒ `SignalWorkflow` возвращает ошибку («workflow gone»): прецеденты
  `deliverUserNote` (`cmd/agent-kernel/channelinbound.go:1395`) и `healGoneRun`
  (`cmd/agent-kernel/nudge.go:137`). Доставку помечаем `expired`, не ретраим.
- Фрейминг для отдельного блока контекста — по образцу `userNotePrefix`/`agentMessageFraming`
  (`workflow.go:5206/5273`): явное указание источника и статуса.

## 3. Модели Plan Graph, Task/Saga, delegated Run

- Task: `workspace.Task` (`workspace/models.go:530`), таблица `workspace_task`; Run:
  `workspace.Run` (models.go:541), таблица `workspace_run`; связка `workspace_run.task_id`.
- Delegated child runs НЕ имеют строки `workspace_run` (комментарий `workspace/store.go:891`) —
  живут только в журнале; id `<parent>/delegate/<NN>`; причинность через
  `parent_run_id` в контексте событий; родитель публикует `delegation.started/completed/failed`.
- Saga — event-sourced, без таблицы: `saga.registered/attached/renamed/merged/deleted`
  (`cmd/agent-kernel/sagalifecycle.go`); UI-роуты `RegisterSagaRoutes` (main.go:3222).
- Plan — не персистентен: события `plan.started/task.started/task.completed/…` внутри workflow
  (`kernel/agent/workflow.go:1061–1729`); типы `PlanSpec`/`PlanTask` (`kernel/agent/plan.go:30/69`).
  Межплановых зависимостей в модели нет — только производные связи (§11.1 ТЗ):
  общая Task, общий Artifact/Saga, общий проект, явная ссылка.
- Существующая визуализация графа: `debugger/src/PlanGraph.tsx` — клиентский fold событий
  `plan.*` (`foldPlans`/`PlanDAG`), переиспользуем для раскрытия карточек.

## 4. Lifecycle events и active Run

- Есть: `run.started/completed/failed/cancelled/restarted`, `delegation.*`, `plan.*`,
  `saga.*`, `artifact.proposed/updated/review.*`, `human.requested/answered/timed_out`,
  `approval.requested/granted/rejected/auto_granted`, `run.user_note`, `supervisor.verdict`.
- Нет `run.waiting`/`run.resumed` — ожидание наблюдается парами
  `human.requested→answered` и `approval.requested→granted/rejected`; в проекции P0
  ожидание выводится из этих событий (и из открытых `human_request`).
- Нет `task.*` событий — состояние Task берётся из `workspace_task` + `workspace_run`.
- События `artifact.*` не несут `context.project` — на fold-этапе присоединяем
  `workspace_artifact`.
- Active Run: `RunsMissingTerminal` / `RunTerminalStatuses` (`workspace/store.go:1104/1068`) —
  journal-side; `ListStartedRuns` — только root-раны. Для проекции — свой снапшот
  `org_active_run`, обновляемый fold-циклом.

## 5. Org-scoped RBAC

- Роли: `RoleReader<Writer<Operator<Admin` (`controlplane/controlplane.go:28`); проверка
  `gate.Allow/AllowQuery` в каждом обработчике; org-скоуп: `visibleUnits(r)` (main.go:1867),
  `workspace.OrgVisible`, `principal.MaxRoleAt`; run-скоуп: `RunVisibilityActors`
  (`workspace/runvisibility.go:26`) + `guardRunVisibility` (main.go:9350).
- Trace-чтение: журнал проверяет `AllowQuery(RoleReader)` + проекты + `ActorScope` на каждый
  запрос (`observation/httpapi/server.go:360/518`); kernel — `guardRunVisibility`.
- Artifact-чтение: `GET /v1/workspace/artifacts/{id}` — RoleReader + `OrgVisible` (404, не 403).
- Орг-привязка Run: НЕТ колонки `org_unit_id` на `workspace_run`; атрибуция — snapshot в
  `exec_context.authorized_by.org_unit_id` (`runExecContextFor`, main.go:9194) на момент запуска.
  Историческая привязка сохраняется (журнал append-only; перевод пишет аудит
  `user.org_unit_changed`, ничего не переписывает).

## 6. Watermark, replay, идемпотентность event stream

- Журнал: `observation_events`, PK `(source_id, event_id)` — базовый дедуп; append-only триггер;
  порядок всегда по кортежу `(occurred_at, received_at, source_id, event_id)`; ретеншн удаляет
  старые события ⇒ проекция обязана быть долговременной записью своей области.
- Паттерн консьюмера — `syncSupervisorCases` (`cmd/agent-kernel/supervisorqueue.go:174`):
  watermark `workspace_supervisor_sync` + якорная таблица `workspace_supervisor_case_event`
  (`ON CONFLICT DO NOTHING RETURNING`), `since = watermark − 1s`, advance после страниц.
- Поздние события: старше watermark — пропускаются; принятая компенсация — ограниченное
  переполнение окна (SRE rollup перекатывает последние часы). Для орг-проекции: якорь +
  повторный fold последнего окна на каждом проходе (безопасно по якорю).
- Replay/rebuild: truncate проекции + сброс watermark; fold — чистая функция событий в строки.

## 7. Переиспользование визуализации графов

`debugger/src/PlanGraph.tsx` (`foldPlans`, `fetchPlans`, `PlanDAG`) — переиспользуется как есть
для per-run графов; сводный граф подразделения требует нового агрегирующего эндпоинта
(`GET /v1/org/plan-graph`) — клиентского fold для подразделения недостаточно.

## 8. Фоновые задачи

Канонический шаблон — goroutine + `time.NewTicker`, запуск в `run()` рядом с
`go runArtifactReviewSweep(...)` (main.go:8221); best-effort, идемпотентные проходы,
dedupe через DB-штампы и journal EventID. Лидер-элекшена нет — безопасно при одном инстансе
kernel (текущий деплой). Пример для копирования: `cmd/agent-kernel/artifactreviewsweep.go`.

## 9. Аудит чтения Artifact

- Агентские чтения уже журналируются: событие `artifact.read` из `handleArtifactRead`
  (`kernel/agent/artifacttools.go:183`) с checksum-версией.
- Человеческие/API чтения НЕ аудируются: `access_audit_log` фиксирует только мутации
  (`workspace/audit.go:143` `RecordAccessAudit`, ~70 констант действий, listing
  `GET /v1/org/audit`). Для §16 ТЗ добавляем аудируемые действия чтения (`artifact.read` /
  `trace.read` / `org_awareness.details`) в местах защищённых чтений — best-effort по
  конвенции.

## 10. Связи Plan, Task, Artifact, Run

`workspace_run.task_id → workspace_task.id → workspace_project`; plan — события журнала,
группируются по `operation_id`; child runs — `parent_run_id` в контексте событий;
`workspace_artifact.origin_run → run`, `saga_id → saga`; орг-скоуп — `org_unit_id` на
ресурсах + `workspace_project_org_unit` + `visibleUnits`/`RunVisibilityActors`.

## Итоговые решения по контрактам (уточнение ТЗ)

1. `org_node_id` события = снапшот атрибуции: для root-ранов `exec_context.authorized_by.org_unit_id`,
   для дочерних — наследование от родителя; резолв один раз при первом fold-событии рана,
   хранится в `org_active_run`; перевод сотрудника историю не двигает (§3 ТЗ).
2. Названия операций API — `/v1/org/*` поверх существующих middleware; формат ошибок
   `{"error": …}` (`writeError`), конверты `{<entity>: [...], count}`.
3. Настройки узла — таблица `org_activity_setting(org_unit_id, key, value)` + резолв по цепочке
   предков (шаблон `org_policy`/`EffectivePolicy` с `sources` для UI).
4. Steer-доставка — `agent.UserNoteSignal` с явным фреймингом «organizational awareness» и
   маркером `[org-awareness:<delivery_id>]` в тексте: маркер позволяет correlate
   `run.user_note` (событие workflow) с доставкой и честно отмечать `consumed`.
5. Дедуп решений — уникальный индекс `(deduplication_key, target_run_id)` на доставках;
   `ignore`-решения не создают доставок.
6. run.waiting/run.resumed не добавляются в kernel/agent в рамках P0 — проекция выводит
   ожидание из `human.*`/`approval.*` (изменение workflow-эмитов — отдельный PR при необходимости).

Схема логического контракта события (§4.3 ТЗ) согласована с фактическим конвертом
`observation.Event`: `event_id`+`source_id` (дедуп), `type`, `occurred_at`,
`context.{project,run,task,actor}`, `data` — источник для summary/refs;
`org_node_id`, `summary`, `visibility_policy`, `schema_version` — поля проекции, не журнала.
