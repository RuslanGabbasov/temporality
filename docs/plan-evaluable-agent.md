# План работ: Evaluable Agent — определение агента и компиляция системного промта

Source spec: `docs/evaluable-agent.md`.
Статус: реализовано (этапы 1–7). Осталось: legacy-конверсия существующих промт-агентов — вручную через Rebuild/визард (решение 3 выполняется с человеком в цикле, автоматический проход при старте отвергнут: LLM-вызов на старте неработоспособен).

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

### Этап 6. Встроенные агенты — S — ✅

- [x] `workspace/builtin.go` — курированные определения Coder/Reviewer/Researcher/DevOps (§7, §12, §17); сидинг при `POST /v1/workspace/projects` (id `<project>-<slug>`, метка builtin, version 1 с промпт-снапшотом).
- [x] `GET /v1/workspace/agents/builtins` — источник шаблонов и кнопки «восстановить встроенное».
- [x] `TEMPLATES` удалены из `Agents.tsx`.

### Этап 7. Вкладка «Эволюция» — M — ✅

- [x] Диалог «Эволюция» в карточке агента: версии (дата, автор, источник, модель-генератор), diff семантики с предыдущей версией, статистика запусков по версиям (runs/completed/failed) из `GET /agents/{id}/runs` с группировкой по `agent_version`.
- [x] Legacy-агенты: промт «как есть», бейдж «ручной промт (legacy)» в превью.
- Аналитика качества/ошибок/траектории против версий — Phase 2 (после evaluation engine).

## 3. Тестирование и приёмка

- Юнит: компилятор (golden files), enforcement capabilities, билдер (парсинг/fallback), стор (версии immutable).
- API: create/update с definition → версия растёт только при реальном изменении; prompt endpoint.
- E2E/live: создать агента визардом из описания «нужен агент для code review…» → запустить run → в событиях `run.started` видна версия определения; сменить критерий завершения → версия 2; на «Эволюции» статистика разделена по версиям.
- Чек-лист спеки: §3 (нет YAML в UI), §5 (резюме, промт вторичен), §7–§10 (генератор не дублирует и не диктует шаги), §11 (evidence-контракт в базовой части), §13 (регенерация с diff), §14 (версии хранят всё перечисленное), §16 (builtin редактируются штатно).

## 4. Принятые решения

1. **Enforcement — жёсткий.** Capabilities реально режут инструменты и окружение, а не формулируют политику в промте: `modify_files=false` → read-only + без write-инструментов; `run_commands=false` → без `run_command`; `network=false` → без сети; `skills=false` → без инъекции скилов; `knowledge=false` → без prior knowledge hints. Иначе агент сам себе наделит полномочий через промт.
2. **Компиляция — при каждом запуске.** Промт всегда свежий; воспроизводимость обеспечивает текст, зафиксированный в `workspace_agent_version.compiled_prompt` (снимок той же детерминированной функции).
3. **Legacy-миграция — через builder.** Существующие агенты с `system_prompt` без definition прогоняются через agent builder (разовый проход, fallback — сохранить старый промт как `prompt_override`, если builder недоступен).
4. **`purpose` = существующая колонка `description`.** Отдельного поля в definition нет: «Назначение» в форме и резюме — это `workspace_agent.description`. Definition хранит только capabilities, constraints, completion, prompt_override.

## 5. Не-цели (этой волны)

- Evaluation engine и оценка качества версий — Phase 2 (после skills evaluations).
- Автономная эволюция агента (агент предлагает изменить своё определение) — Phase 2, по аналогии с skill evolution proposals.
- Marketplace/шаринг агентов между проектами.
