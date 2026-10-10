# План работ: Evaluable Agent — определение агента и компиляция системного промта

Source spec: `docs/evaluable-agent.md`.
Статус: Фаза 1 (этапы 1–7) реализована; ревизия 2026-10-10 сверила план со спекой и кодом — расхождения устранены в тексте, пост-плановые изменения зафиксированы в §2.1. Фаза 2: этап 8 (оценки агента) реализован; CLI — опциональный хвост. Legacy-конверсия — осознанно ручная: Rebuild/визард с человеком в цикле (автоматический проход при старте отвергнут: LLM-вызов на старте неработоспособен). Далее — этапы 9–10 (§2.2).

## 0. Текущее состояние (baseline)

- `workspace_agent` хранит `system_prompt` как свободный текст (миграция 000021). Семантических полей (назначение, ограничения, критерии завершения) нет.
- В рантайме `applyAgentConfig` (`cmd/agent-kernel/main.go`) копирует `SystemPrompt` в `RunInput`; `AgentRun` (`kernel/agent/workflow.go`) использует его как есть, иначе — `systemPromptForRole` (базовый контракт + роль). Секция skills и knowledge-hints добавляются отдельно.
- UI: на табе «Основное» диалога агента — свободный textarea «System prompt». Шаблоны Coder/Reviewer/Researcher/DevOps захардкожены в `Agents.tsx` (`TEMPLATES`).
- У агентов нет версионирования (у скилов есть: `workspace_skill_version`, immutable).
- Есть переиспользуемые паттерны: skill builder (`kernel/agent/skillbuilder.go` + `POST /skills/draft`), визард с вопросами и provenance-бейджами (`Skills.tsx`).

## 1. Целевая модель

### 1.1 Определение агента (AgentDefinition)

Хранится в `workspace_agent.definition` (JSONB), редактируется обычной формой, никогда — как YAML:

```json
{
  "capabilities": {
    "read_files": true,
    "modify_files": true,
    "run_commands": true,
    "network": false,
    "skills": true,
    "knowledge": true
  },
  "constraints": ["Не изменять инфраструктуру и не удалять файлы."],
  "completion": ["Запустить соответствующие тесты.", "Убедиться, что сборка проходит."],
  "prompt_override": ""
}
```

- Назначение агента — существующая колонка `description` (основное поле формы «Для каких задач нужен этот агент?»).
- `capabilities` — намерение пользователя; эффективные возможности вычисляются из окружения (sandbox profile, tools, MCP) и прав. Выключение capability жёстко ограничивает агента (см. этап 2).

### 1.2 Компиляция промта

Детерминированная функция ядра, не LLM:

```
CompileAgentPrompt(def, env) =
    базовый контракт (доказательность §11, память §10, песочница)
  + роль/идентичность (имя + purpose; ответственность, не стиль §12)
  + ограничения (Never: ...)
  + критерии завершения (Before declaring done, verify: ...)
  + политика окружения (read-only / no network / ...)
```

Правила генератора (§7–§10, §17): без пошаговых инструкций; без дублирования tool-схем и skills; без «always use memories»; минимальная длина; проверяемые критерии завершения.

## 2. Этапы работ

Порядок выбран так, чтобы ценность шла с первого этапа: компилятор и версионирование — ядро, UI и AI-формирование — поверх.

### Этап 1. Модель данных и миграция — S — ✅

- [x] Миграция `000030_agent_definition.up.sql` (`definition JSONB`, `definition_version`, таблица `workspace_agent_version`).
- [x] `workspace/models.go`: `AgentDefinition` + `Capabilities` (`nil` = разрешено); `Agent.Definition`, `DefinitionVersion`, `AgentVersion`.
- [x] `workspace/store.go`: create/update/list/get с новыми полями, `decodeAgentDefinition` (legacy ⇒ nil), `InsertAgentVersion`, `ListAgentVersions`.

### Этап 2. Компилятор промта и enforcement — M — ✅

- [x] `kernel/agent/prompt.go`: `CompileAgentPrompt` (§6–§12), `EffectiveSystemPrompt` (override > legacy manual > compiled).
- [x] Юнит-тесты: golden, restricted caps, отсутствие дублирования/пошаговости, fallback'и.
- [x] Enforcement в `applyAgentConfig` + `RunInput.DenyTools/ReadOnly/SkipKnowledge`: `modify_files=false` → read-only sandbox; `run_commands=false` → `run_command` исключён (реклама + defense-in-depth в RunTool); `network=false` → без сети; `skills=false` → без инъекции скилов; `knowledge=false` → без prior hints. `advertisedTools` фильтрует deny-лист.
- [x] `run.started` несёт `agent_id`/`agent_version`; `workspace_run.agent_version` (миграция 000031) — «Эволюция» сопоставляет точную версию.

### Этап 3. Workspace API и версионирование — S — ✅

- [x] `POST/PUT /v1/workspace/agents` принимают `definition` (+ `prompt_source`, `generator_model`); версия растёт только при изменении семантики (definition|purpose), снапшот в `workspace_agent_version` (author = субъект токена).
- [x] `GET /v1/workspace/agents/{id}/versions` — список версий.
- [x] `GET /v1/workspace/agents/{id}/prompt` — скомпилированный промт текущей версии (source: override|definition|legacy|role).
- [x] `GET /v1/workspace/agents/{id}/runs` — запуски агента (для «Эволюции»).

### Этап 4. UI: смысл вместо промта, визард, регенерация — L — ✅

- [x] Карточка агента — резюме по §5: Назначение / Может / Не может (выключенные capabilities + ограничения) / Перед завершением; скомпилированный промт с источником (override/definition/legacy/base).
- [x] Форма: 7 табов — Основное (имя, назначение, провайдер/модель + Rebuild + restore builtin), Возможности (6 переключателей с пояснением жёсткого enforcement), Правила (ограничения + проверка результата), Параметры, Навыки и MCP, Инструменты, Системный промт (превью + override). Свободный textarea удалён.
- [x] Визард: описание → `POST /agents/draft` → заполненная форма с provenance-бейджами (from agent / уточнить / verified) и вопросами билдера.
- [x] Переформировать (§13): diff-диалог по полям, поля с ручными правками помечены, принять/отменить.
- [x] Локализация en/ru.

### Этап 5. Agent Builder (AI-формирование) — M — ✅

- [x] `kernel/agent/agentbuilder.go` — `BuildAgentDraft`: NL → `{name, purpose, capabilities, constraints, completion, suggested, questions}`; промт билдера кодирует критерии §17; sandbox-подсказки валидируются.
- [x] Эндпоинт `POST /v1/workspace/agents/draft` (gate writer).
- [x] Тесты: парсинг fenced JSON, fallback имени из purpose, фильтрация пустых вопросов/значений, невалидный JSON.

### Этап 6. Встроенные агенты — S — ✅ (пересобран после уточнения спеки)

- [x] `workspace/builtin.go` — курированные шаблоны Coder/Reviewer/Researcher/QA/DevOps (§7, §12, §17).
- [x] Шаблоны никогда не создаются автоматически — ни при установке, ни при создании проекта (§16 в действующей редакции; исходный автосидинг при `POST /v1/workspace/projects` свёрнут в `d189e97`). Пользователь создаёт агента из галереи шаблонов одним действием и получает обычный редактируемый агент.
- [x] `GET /v1/workspace/agents/builtins` — источник галереи «Создать из шаблона» и кнопки «восстановить встроенное».
- [x] `TEMPLATES` удалены из `Agents.tsx`.

### Этап 7. Вкладка «Эволюция» — M — ✅

- [x] Диалог «Эволюция» в карточке агента: версии (дата, автор, источник, модель-генератор), diff семантики с предыдущей версией, статистика запусков по версиям (runs/completed/failed) из `GET /agents/{id}/runs` с группировкой по `agent_version`.
- [x] Legacy-агенты: промт «как есть», бейдж «ручной промт (legacy)» в превью.
- Аналитика качества/ошибок/траектории против версий — этап 9 (§2.2).

### 2.1. Пост-плановые изменения (ревизия 2026-10-10)

Спека и смежные волны ушли вперёд исходного плана; зафиксировано, чтобы документ соответствовал коду:

* **Шаблоны вместо автосоздания builtin-агентов** (`d189e97`) + курированный QA-шаблон (`385a99a`) — §16 в действующей редакции: «не создаются автоматически… создаются из галереи шаблонов одним действием».
* **Capability `delegation`** — «Делегировать другим агентам» в форме агента; инвертированный дефолт: nil/false ⇒ запрещено, делегирование выдаётся явно человеком (`51a2e50`, `docs/agent-delegation.md`).
* **`workspace_agent.org_unit_id`** — видимость агентов наследуется по оргдереву (`docs/org-structure.md` §3.2); empty = вся инсталляция. Агенты остаются кросс-функциональными; опциональный `project_id` в модели сохранён как legacy-скоуп — кандидат на вычистку в волне миграции org-structure.

### 2.2. Фаза 2 — Оценки и эволюция агента (план работ)

Спека §15: «Эволюция» должна сопоставлять изменения определения с результатами работы агента; в перспективе — связь с evaluation и накопленными свидетельствами. Evaluation-инфраструктура уже обкатана на скилах (living-skills Phase 2: suite/run таблицы, синхронный раннер, события, UI-таб, CLI) — фаза зеркалит её для агентов.

Ключевое отличие от скилов: оценка pinned не к «текущему состоянию», а к конкретной `definition_version` — сравниваются версии определения, а не дрейф текущего промта.

#### Этап 8. Оценки агента (suites + runner) — M — ✅

- [x] Миграция `000048_agent_evaluations`: `workspace_agent_evaluation_suite` (agent_id PK, cases JSONB, updated_at) и `workspace_agent_evaluation_run` (id BIGSERIAL, agent_id, agent_version INT, passed, failed, cases JSONB, created_at) — зеркало `000044_skill_evolution_phase2`.
- [x] Стор `workspace/agentevals.go`: `Get/SaveAgentEvaluationSuite`, `RecordAgentEvaluationRun`, `ListAgentEvaluationRuns` (лимиты как у скилов).
- [x] Эндпоинты: `GET/PUT /v1/workspace/agents/{id}/evaluation-suite` (reader/writer), `POST/GET /v1/workspace/agents/{id}/evaluations` (запуск — writer) + внутренний sink `POST .../evaluation-runs`; 404 на неизвестного агента.
- [x] Раннер `kernel/agent/agenteval.go` — `RunAgentEvaluation(agentID, version)`: version=0 → эффективный промт (эндпоинт `/prompt`), version>0 → `workspace_agent_version.compiled_prompt` снапшота; кейс = user-сообщение, один вызов модели на кейс, проверка `must_contain`/`must_not_contain`; запись run + событие `agent.evaluation.completed` (agent_id, agent_name, version, passed, failed).
- [x] UI: в «Эволюции» секция «Оценки» — редактор сьюта, выбор версии для запуска (текущая / конкретная), история с детализацией кейсов (passed/missed/unexpected). Локализация en/ru.
- [ ] CLI (опционально, зеркало skill CLI): `temporality agent evals <id>` / `eval-run <id> --version N`.
- [x] Тесты (`kernel/agent/agenteval_test.go`): текущая версия через эффективный промт, pinned-версия тестирует снапшот (не текущий промт), неизвестная версия/агент, пустой сьют, failed-case с missed/unexpected, запись run и событие.

#### Этап 9. Аналитика эволюции — S/M

- [ ] Агрегат на версию: runs (уже сгруппированы по `agent_version`) + eval pass rate + средние токены/ходы; без новых таблиц — соединение `workspace_agent_version` × eval-ранов × run-статистики.
- [ ] Строка версии в «Эволюции» получает pass rate и подсветку регрессии (падение pass rate против предыдущей версии при неизменном сьюте).
- [ ] Тесты: агрегация, детект регрессии.

#### Этап 10. Evolution proposals — M/L

- [ ] Анатомия как у скилов: observed problem / proposed change / expected effect + provenance (свидетельства: run ids, knowledge ids).
- [ ] Хранение: `workspace_agent_proposal` (agent_id, base_version, definition JSONB, problem/change/effect, evidence JSONB, status pending|applied|rejected, author, created_at); события `agent.definition.proposed/applied/rejected`.
- [ ] Применение — только человеком, через штатный update-путь: версия растёт, снапшот пишется как обычно (зеркало living-skills §26: агенты предлагают — люди применяют).
- [ ] Источники proposal: вручную из карточки агента; из прогона — инструмент `agent_propose` (зеркало `skill_propose`), пишет pending-proposal со ссылками на свидетельства.
- [ ] UI: список proposals во вкладке «Эволюция», diff definition к текущей версии, применить/отклонить.
- [ ] Тесты: CRUD, события, apply поднимает версию, права (writer на apply).

#### Этап 11 (P2, опционально). Trajectory-level оценки

Кейс = полная задача → реальный `AgentRun`; критерии: завершение в бюджете, отсутствие реворков/эскалаций. Дорого; включать только если prompt-level оценки перестанут различать версии.

## 3. Тестирование и приёмка

- Юнит: компилятор (golden files), enforcement capabilities, билдер (парсинг/fallback), стор (версии immutable).
- API: create/update с definition → версия растёт только при реальном изменении; prompt endpoint.
- E2E/live: создать агента визардом из описания «нужен агент для code review…» → запустить run → в событиях `run.started` видна версия определения; сменить критерий завершения → версия 2; на «Эволюции» статистика разделена по версиям.
- Чек-лист спеки: §3 (нет YAML в UI), §5 (резюме, промт вторичен), §7–§10 (генератор не дублирует и не диктует шаги), §11 (evidence-контракт в базовой части), §13 (регенерация с diff), §14 (версии хранят всё перечисленное), §16 (builtin редактируются штатно).

## 4. Принятые решения

1. **Enforcement — жёсткий.** Capabilities реально режут инструменты и окружение, а не формулируют политику в промте: `modify_files=false` → read-only + без write-инструментов; `run_commands=false` → без `run_command`; `network=false` → без сети; `skills=false` → без инъекции скилов; `knowledge=false` → без prior knowledge hints. Иначе агент сам себе наделит полномочий через промт.
2. **Компиляция — при каждом запуске.** Промт всегда свежий; воспроизводимость обеспечивает текст, зафиксированный в `workspace_agent_version.compiled_prompt` (снимок той же детерминированной функции).
3. **Legacy-миграция — ручная, через Rebuild/визард.** Существующие агенты с `system_prompt` без definition конвертируются человеком через «Переформировать» (fallback — оставить старый промт как `prompt_override`). Автоматический проход при старте ядра отвергнут: LLM-вызов на старте неработоспособен. Legacy-агенты до конверсии работают как есть, с бейджем «ручной промт (legacy)».
4. **`purpose` = существующая колонка `description`.** Отдельного поля в definition нет: «Назначение» в форме и резюме — это `workspace_agent.description`. Definition хранит только capabilities, constraints, completion, prompt_override.

## 5. Не-цели

- Автономное применение агентом изменений собственного определения: apply — только человек (этап 10 даёт агенту право предлагать, не применять).
- Marketplace/шаринг агентов между проектами.
- Trajectory-level оценки — до обоснования потребностью (этап 11, P2).
- Автовыбор агента под задачу / semantic routing.
