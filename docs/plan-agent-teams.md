# План работ: Команды агентов

Source spec: `docs/agent-teams.md` (21 раздел / 8 фаз).
Статус: **в работе**. Волны A–H соответствуют фазам 1–8 спеки; порядок выбран так, чтобы после волны D система была юзабельна (MVP = A–D), а formation и эволюция надстраивались поверх работающей механики.

Размеры задач: S / M / L — относительно одной волны работы.

## 0. Текущее состояние (baseline после 9c00cb4)

- Механика совместной работы есть в ядре: `delegate` (1:1) и `plan` (DAG: `goal`, `tasks[{id, agent_id, prompt, depends_on, review_of, max_turns}]`, `max_rework`), acceptance gates с rework-циклами (`plan.task.rejected/reopened/invalidated/skipped`), валидация циклов, резолв всех агентов до старта (`effect: none`), бюджеты запусков, `MaxDelegationDepth`.
- События и UI: `delegation.*`, `plan.*`; DelegationTree и PlanGraph рендерят их без привязки к «команде».
- Пример команды захардкожен: `examples/lead_coder_reviewer_qa` (отдельный Temporal-workflow, последовательный pipeline lead→coder→reviewer→qa) — подлежит миграции в волне H.
- Паттерн версионированной сущности готов: `workspace_skill` + `workspace_skill_version` (immutable, provenance, draft→apply), орг-привязка `org_unit_id` + `ListSkillsVisible`.
- Организация: `Principal.VisibleUnits`, эффективная видимость, роли на узлах, аудит `access_audit_log`, execution identity.
- Cost: per-run учёт токенов/стоимости (`/v1/agent/cost/run`, `total_tokens`/`cost_usd` в `run.completed`); rollup по дереву — нет.

Известные пробелы против спеки — вся сущность «команда»: нет `workspace_team`, слотов, протоколов, team-ранов, rollup, formation, эволюции.

## 1. Карта фаз спеки → волны плана

| Фаза спеки | Содержание | Волна |
|---|---|---|
| 1 Модель и API | `workspace_team` + версии, слоты/протоколы как данные, CRUD, org-видимость | A |
| 2 Рантайм | компиляция протоколов в plan/delegate, `team.*` события, снапшот, запуск/отмена | B |
| 3 Привязка слотов | fixed/role-подбор, подтверждение, unresolved-ошибки | C |
| 4 UI команд | раздел, карточка с вкладками, запуск, rollup-сводка | D |
| 5 Formation | Team Builder Agent, wizard, происхождение значений | E |
| 6 Командный контекст | team scratch, артефакты контрактов, scoped hints | F |
| 7 Телеметрия и эволюция | метрики версий, evolution proposals, вкладка «Эволюция» | G |
| 8 Миграция примера | LeadCoderReviewerQA → шаблон pipeline; шаблоны команд | H |

## 2. Волны работ

### Волна A. Модель и API — M — фаза 1 — **готово**

- [x] Миграция: `workspace_team` (id, name, description, version, manifest JSONB — слоты/протокол/дефолты, org_unit_id) + `workspace_team_version` (immutable, PK(team_id,version)); аналог 000028. (000047)
- [x] Стор `workspace/teams.go`: Team/TeamVersion-структуры, Create/Get/ListAll/ListVisible/Update/Delete, ListVersions, ProposeVersion/ApplyVersion (переиспользовать паттерн draft→apply из skills). (+ Reject)
- [x] Валидация манифеста в сторe: известный `protocol.kind`, непустые слоты с уникальными id, `binding.mode` ∈ {fixed, role}, `fixed` требует agent_id; синтаксис-ошибки — 422. (+ cycle/dangling-проверки для dag)
- [x] API `/v1/workspace/teams` CRUD (+ `GET /{id}/versions`, `POST /{id}/versions/{v}/apply`, `POST /{id}/versions/{v}/reject`); списки фильтруются по OrgVisible, мутации — по роли; PUT не сбрасывает `org_unit_id` (binding — админ-эндпоинт, как у остальных).
- [x] Аудит `team.created/updated/deleted`, `team_version.proposed/applied/rejected`.
- [x] Go-тесты: валидация манифеста (22 кейса), версии (immutable, apply переключает указатель, повторный apply 409), видимость по дереву, CRUD.

Заметки: аудит проверен на стенде (`team.created/proposed/applied` в `access_audit_log`, actor ops); binding-smoke: `PUT /v1/org/resources/team/code-delivery/binding` → появляется в `GET /v1/org/units/rd/resources` → unbind очищает.

### Волна B. Рантайм командных запусков — L — фаза 2

- [ ] `POST /v1/agent/teams/{id}/runs` {project, goal, bindings?, version?}: снапшот определения в `exec_context` (по образцу org-structure §35), резолв слотов (волна A даёт fixed; role — заглушка «preferred или 422 до волны C»).
- [ ] Компиляция протоколов в существующие механизмы: `pipeline` (последовательные дочерние раны, контракт → промпт следующего), `review_gate` (plan c `review_of` + `max_rework`), `dag` (шаги с `depends_on`); `lead_workers`/`fan_out` — поверх `plan`. Дочерние раны — настоящие AgentRun (принцип agent-delegation.md).
- [ ] События `team.started` {team_id, version, goal, bindings}, `slot.bound` {slot_id, agent_id, mode}, `team.completed` {status, total_tokens, cost_usd, rework_rounds}, `team.failed` — эмит из корневого workflow рядом с существующими `plan.*`/`delegation.*`.
- [ ] Отмена командного рана — вниз по дереву (переиспользовать существующее); budgets: сумма шагов, честный `budget_limit`.
- [ ] Сквозные Go-тесты: порядок событий, pipeline из 3 слотов, review_gate с отклонением→доработкой→принятием, снапшот (изменение определения после старта не влияет), отмена, лимит бюджета.

### Волна C. Привязка слотов — M — фаза 3

- [ ] Роль-подбор при запуске: кандидаты из ListAgentsVisible по requirements (capabilities ⊇ required; preferred_agent_id — тай-брейк); однозначный кандидат → авто-привязка, несколько → 409 со списком кандидатов для явного выбора, ноль → 422 с перечнем требований.
- [ ] `bindings` в теле запуска: пользователь фиксирует выбор; проверка видимости агента в скоупе команды.
- [ ] `GET /v1/agent/teams/{id}/resolve?project=` — предпросмотр привязок для UI (кто будет выбран и почему).
- [ ] Тесты: fixed-агент невидим → отказ; role без кандидатов → отказ до старта (effect=none); preferred при равных; capabilities-матчинг.

### Волна D. UI команд — M — фаза 4

- [ ] Раздел «Команды» в Layout: список с фильтром, бейдж узла/«глобально», создание/редактирование/удаление.
- [ ] Карточка команды: вкладки «Основное» (протокол, дефолты), «Состав» (слоты: ответственность, требования, контракт, привязка), «Запуски» (история с итогами), «Эволюция» (заглушка до волны G).
- [ ] Форма редактирования — структурированная (не YAML): протокол селектом, слоты карточками, requirements мультивыбором из известных capabilities, привязка fixed (селект агентов) / role (requirements + preferred).
- [ ] Диалог запуска: проект, цель, предпросмотр привязок (волна C), подтверждение/замена role-слотов.
- [ ] Runs: командные раны отображаются существующими DelegationTree/PlanGraph; сводка team.completed (токены/стоимость/rework) в шапке рана.
- [ ] i18n ru/en, обе темы, ListFilter; поле «Доступность» как у остальных ресурсов.

### Волна E. Formation — M — фаза 5

- [ ] `POST /v1/workspace/teams/builder` {description} — Team Builder Agent (по образцу skill builder): интерпретация → черновик манифеста (протокол, слоты, требования, дефолты) + происхождение значений.
- [ ] Wizard в UI: textarea → «Сформировать команду» → форма редактирования с маркерами происхождения; уточняющие вопросы в том же wizard; «Переформировать» с diff и сохранением ручных правок.
- [ ] Шаблоны команд (Code Delivery, Research Fan-out, Delivery + QA gate) — seed-строки для builder, не предсозданные сущности.

### Волна F. Командный контекст — M — фаза 6

- [ ] Team scratch: префикс в workspace storage (`team/{run_id}/`), путь передаётся каждому дочернему рану, TTL-очистка после завершения.
- [ ] Артефакты контрактов: контракт выхода слота пишется в scratch файлом; зависимые слоты получают текст + ссылку.
- [ ] Scoped hints: знания, подтверждённые внутри командного рана, видят все слоты (расширение существующего hints-механизма фильтром по run tree).

### Волна G. Телеметрия и эволюция — M/L — фаза 7

- [ ] Rollup-эндпоинт `/v1/agent/cost/team-run?run_id=` (агрегация по дереву дочерних ранов) + `rework_rounds` из `plan.task.*`.
- [ ] Метрики версии определения: accept-first-try, средние rework-циклы, стоимость/длительность, доля споров → таблица/запрос, вкладка «Эволюция» с версиями и причинами изменений.
- [ ] Evolution proposals: детектор устойчивых паттернов (частые отклонения конкретной пары слотов) → `ProposeTeamVersion` с provenance (observed_problem/proposed_change/expected_effect); UI accept/reject с проваливанием в evidence.

### Волна H. Миграция примера — S — фаза 8

- [ ] LeadCoderReviewerQA → шаблон команды `pipeline`; перевод UI-ссылок на пример; удаление `examples/lead_coder_reviewer_qa` и спец-эндпоинтов.

## 3. Тестирование и приёмка (по §20 спеки)

- CRUD команды + версии (immutable, apply переключает указатель, история читается).
- `pipeline`-запуск: дерево дочерних ранов, события `team.*` и `plan.*`/`delegation.*` в правильном порядке.
- `review_gate`: работа → отклонение → доработка → принятие (полный цикл).
- Слот `role` находит агента по capabilities / несколько кандидатов → 409 / ноль → 422 до старта.
- Rollup токенов/стоимости командного рана в Runs.
- Изменение определения после старта не влияет на идущий запуск.
- Команда не расширяет права участников (у каждого слота свои политики).
- Org-видимость фильтрует списки команд и кандидатов привязки.
- Пользователь ни в одном сценарии не видит `team.yaml` (манифест = внутренний формат).

## 4. Принятые решения

1. **Без нового движка оркестрации**: протоколы компилируются в существующие `plan`/`delegate`; командный рантайм — надстройка над AgentRun.
2. **Модель версий — как у skills**: immutable `workspace_team_version`, draft→apply, provenance у версии — переиспользуем проверенный паттерн.
3. **Слоты — данные манифеста, не отдельная таблица**: MVP хранит слоты в JSONB манифеста; выделенная таблица — если понадобится запросами по слотам.
4. **Role-подбор детерминирован и скромен**: capabilities-матчинг + preferred-тай-брейк; без LLM-подбора и истории успеха в первой версии.
5. **`team.*` события рядом с `plan.*`/`delegation.*`**: существующие UI работают без переделки, командная разметка аддитивна.
6. **Шаблоны вместо предсозданных команд** (урок встроенных агентов): builder предлагает, пользователь создаёт свою сущность.
7. **Rollup строится поверх per-run cost-учёта** (9c00cb4): без отдельного биллинга.

## 5. Не-цели (первой версии)

- N-агентный свободный чат и произвольные топологии вне словаря протоколов.
- Вложение команд (команда слотом другой команды).
- LLM-матчинг слотов, «переговоры» слотов о контрактах.
- Расписания/триггеры для команд (связка с triggers — будущая работа).
- Отдельная таблица слотов и аудит привязок на каждый запуск (минимальный аудит — в волнах A/G).
