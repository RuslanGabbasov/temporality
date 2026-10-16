# План работ: Команды агентов

Source spec: `docs/agent-teams.md` (21 раздел / 8 фаз).
Статус: **в работе** (волны A–C готовы, MVP остаётся за UI волны D). Волны A–H соответствуют фазам 1–8 спеки; порядок выбран так, чтобы после волны D система была юзабельна (MVP = A–D), а formation и эволюция надстраивались поверх работающей механики.

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

### Волна B. Рантайм командных запусков — L — фаза 2 — **готово**

- [x] `POST /v1/agent/teams/{id}/runs` {project, goal, bindings?, version?}: снапшот манифеста в input рана (семантика §35), резолв слотов (`TeamSlotBindings`: fixed→агент; role→preferred/override или 422).
- [x] Компиляция протоколов в существующие механизмы (`CompileTeamProgram`): `pipeline` (последовательные дочерние раны, контракт → промпт следующего), `review_gate` (plan c `review_of` + `max_rework`), `dag` (шаги с `depends_on`), `fan_out` (последний слот зависит от всех), `lead_workers` (один lead-ран с ростером, без механических гейтов). Дочерние раны — настоящие AgentRun.
- [x] События `team.started` {team_id, version, goal, bindings}, `slot.bound` {slot_id, agent_id, mode}, `team.completed` {status, total_tokens, cost_usd, rework_rounds}, `team.failed` — эмит из корневого workflow; тоталы — честный subtree-rollup (own + children) в `run.completed`/`team.completed`, включая путь завершения по review-verdict.
- [x] Отмена командного рана — вниз по дереву (существующий механизм cancellation); budgets: тоталы агрегируются по поддереву.
- [x] Сквозные Go-тесты: компилятор всех 5 протоколов, привязка слотов (6 кейсов), pipeline порядок событий + тоталы, review_gate rework-цикл (отклонение→доработка→принятие), failure → team.failed, lead_workers.

### Волна C. Привязка слотов — M — фаза 3 — **готово**

- [x] Роль-подбор при запуске: кандидаты из ListAgentsVisible (+ фильтр проектного скоупа агентов) по requirements (`Agent.SatisfiesRequirements`: capabilities в семантике карточек ростера — nil/true = может, false = нет, delegation только явная выдача; skills ⊇ required); однозначный кандидат → авто-привязка, несколько без preferred → 409 со списком кандидатов, ноль → 422 с перечнем требований (`agent.ResolveTeamBindings` + `TeamBindingRejection`).
- [x] `bindings` в теле запуска: пользователь фиксирует выбор; оверрайд обязан попасть в ростер запуска (невидимый агент — 422 до старта, effect=none).
- [x] `GET /v1/agent/teams/{id}/resolve?project=&version=` — предпросмотр привязок для UI: по слотам (agent_id, mode, reason, candidates/requirements), `resolvable` + причина; превью не падает целиком.
- [x] Валидация манифеста дополняет словарь: неизвестные capability в requirements — 422 на сохранении; `write_files` принят как исторический алиас `modify_files`.
- [x] Тесты: fixed-агент вне ростера → 422; role без кандидатов → 422 до старта; preferred при равных; capabilities-матчинг (явный false отсекает, nil пропускает); legacy-агенты — только слоты без требований; delegation — только явная выдача; skills-требования; оверрайды (валидный/невидимый/чужой слот); семантика SatisfiesCapability зафиксирована в workspace.

### Волна D. UI команд — M — фаза 4 — **готово**

- [x] Раздел «Команды» в Layout (основное меню, рядом с Запусками): список с фильтром, бейдж узла/«глобально», создание/редактирование/удаление; API-клиент в `workspaceApi.ts` (CRUD, версии, apply/reject, resolve, запуск).
- [x] Карточка команды: вкладки «Основное» (протокол, шаги DAG, дефолты, статистика), «Состав» (слоты: ответственность, требования, контракт, привязка), «Запуски» (история из journal-событий `team.*`/`run.cancelled` с итогами — `teamRuns.ts` + тесты), «Эволюция» (версии с provenance, apply/reject черновиков; метрики версий — волна G).
- [x] Форма редактирования — структурированная (не YAML): протокол селектом, слоты карточками, requirements чекбоксами из 7 канонических capabilities + навыки списком id, привязка fixed (селект агентов) / role (requirements + preferred); редактор шагов для DAG.
- [x] Диалог запуска: проект, цель, предпросмотр привязок через `GET /v1/agent/teams/{id}/resolve`, подтверждение/замена слотов (явные bindings), обработка 409 (кандидаты) и 422 (требования); после старта — переход в Запуски на новый ран. Черновик версии не запускается (предупреждение + disabled).
- [x] Runs: командные раны отображаются существующими DelegationTree/PlanGraph; сводка team.completed (токены/стоимость/rework) в шапке рана (AgentRuns.tsx).
- [x] i18n ru/en, обе темы, ListFilter; поле «Доступность» (org_unit_id) как у остальных ресурсов.

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
