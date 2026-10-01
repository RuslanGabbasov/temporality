# Living Skills — эволюционирующие capabilities в Temporality

## 1. Цель

Развить текущую модель skills из статического набора Markdown-инструкций в **версионируемые capabilities**, которыми одинаково могут пользоваться:

* агент;
* человек-разработчик;
* runtime;
* инструменты анализа;
* система evaluations.

При этом **Skill не должен становиться хранилищем памяти**.

Skill отвечает за:

> **что агент умеет делать и каким каноническим способом это делается.**

Temporality отвечает за:

> **что произошло при реальном применении Skill, что из этого было узнано, что было переиспользовано, что оказалось неверным и как на основании накопленного опыта может измениться сам Skill.**

Таким образом, необходимо разделить четыре сущности:

```text
Skill
  capability + canonical procedure

Execution
  конкретное применение capability

Memory / Knowledge
  опыт, полученный из executions

Evolution
  изменение Skill на основании опыта и evidence
```

Основной цикл:

```text
             ┌──────────────────┐
             │      Skill       │
             │ capability       │
             │ procedure        │
             │ tools            │
             └────────┬─────────┘
                      │
                   execute
                      │
                      ▼
             ┌──────────────────┐
             │    Execution     │
             │ trajectory       │
             │ tool calls       │
             │ evidence         │
             └────────┬─────────┘
                      │
                 observations
                      │
                      ▼
             ┌──────────────────┐
             │ Memory /         │
             │ Knowledge        │
             │                  │
             │ discovered       │
             │ confirmed        │
             │ reused           │
             │ invalidated      │
             └────────┬─────────┘
                      │
                 enough evidence
                      │
                      ▼
             ┌──────────────────┐
             │ Skill Evolution  │
             │ proposal         │
             │ evaluation       │
             │ approval         │
             └────────┬─────────┘
                      │
                      ▼
                new Skill version
```

---

# 2. Основной архитектурный принцип

## Skill ≠ Memory

Skill не должен постепенно превращаться в свалку накопленного operational knowledge.

Необходимо различать:

### Skill

Каноническая capability:

> "Как агент должен выполнять эту работу."

### Memory / Knowledge

Контекстный опыт:

> "Что мы узнали при выполнении этой работы."

### Execution

Фактическая история:

> "Что реально сделал агент."

### Evolution

Изменение capability:

> "Что в Skill необходимо изменить, если накопленного evidence достаточно."

---

# 3. Пример разделения

Skill:

```text
Deploy service

1. Check repository state
2. Build image
3. Run tests
4. Deploy to staging
5. Wait until service is healthy
6. Promote to production
```

Memory:

```text
Для service X health endpoint обычно становится
стабильным через 30–50 секунд после rollout.

Источник:
  execution #1827
  execution #1831
  execution #1842

Состояние:
  confirmed

Использовано:
  7 раз
```

Эта память **не должна автоматически превращаться в строку Skill**.

Если накоплено достаточно evidence, может появиться evolution proposal:

```text
Skill deploy v1.2 → v1.3

Change:
  replace generic health check with
  wait_until_healthy(service=X)

Reason:
  repeated failures during health check

Evidence:
  14 executions

Evaluations:
  12/12 passed
```

После принятия появляется новая версия Skill.

---

# 4. Skill package

Минимальная структура:

```text
skills/
  <skill-id>/
    SKILL.md
    skill.yaml

    scripts/
    examples/
    evals/

    CHANGELOG.md
```

**Не создавать `knowledge/` внутри Skill.**

Knowledge является частью temporal state Temporality и хранится отдельно от skill package.

Skill package должен оставаться переносимым и пригодным для:

* Git;
* code review;
* ручного редактирования;
* распространения;
* versioning;
* загрузки в другой runtime.

---

# 5. `SKILL.md`

`SKILL.md` остаётся основным человеко- и LLM-читаемым описанием Skill.

Он должен содержать:

* назначение;
* область применения;
* когда использовать;
* когда не использовать;
* canonical procedure;
* ограничения;
* рекомендации;
* типичные ошибки;
* примеры.

Пример:

```markdown
# Deploy service

## Purpose

Deploy a service through staging into production.

## When to use

Use this skill when a service must be deployed through
the standard deployment pipeline.

## Procedure

1. Check repository state.
2. Build the image.
3. Run tests.
4. Deploy to staging.
5. Wait until the service is healthy.
6. Promote to production.

## Constraints

- Do not deploy with uncommitted changes.
- Production promotion requires successful staging verification.
```

Markdown не должен быть единственным источником машинно необходимой информации.

---

# 6. `skill.yaml`

Машинный контракт Skill.

Пример:

```yaml
id: deploy-service
version: 1.2.0

name: Deploy service

description: >
  Deploys a service through staging into production.

inputs:
  repository:
    type: string
    required: true

  revision:
    type: string
    required: true

outputs:
  deployment:
    type: artifact

capabilities:
  - inspect_repository
  - build
  - test
  - deploy
  - verify

tools:
  - repository.read
  - repository.status
  - build.run
  - test.run
  - deployment.create
  - deployment.status

runtime:
  sandbox: required
  network: restricted

preconditions:
  - repository_available
  - revision_exists

postconditions:
  - staging_deployment_created
  - production_deployment_verified

evidence:
  required:
    - revision
    - test_result
    - deployment_result

evaluation:
  suite: deploy-service/default
```

---

# 7. Contract

Contract описывает то, что runtime способен проверить независимо от LLM.

Минимально поддержать:

* `inputs`;
* `outputs`;
* `capabilities`;
* `tools`;
* `runtime`;
* `preconditions`;
* `postconditions`;
* `evidence`;
* `evaluation`.

Contract не должен превращаться в язык программирования.

Он описывает **границы capability**, а не каждый шаг реализации.

---

# 8. Capabilities

Skill может предоставлять одну или несколько capabilities.

Например:

```yaml
capabilities:
  - inspect_repository
  - build
  - test
  - deploy
  - verify
```

Capability должна быть стабильным идентификатором, пригодным для:

* поиска;
* выбора Skill;
* связывания Memory;
* аналитики;
* evolution proposals.

Memory может ссылаться не только на `skill_id`, но и на конкретную capability.

Например:

```text
skill: deploy-service
capability: verify
```

Это позволяет Temporality накапливать опыт на более точном уровне.

---

# 9. Tools

Skill должен явно объявлять используемые инструменты.

Например:

```yaml
tools:
  - repository.read
  - repository.diff
  - test.run
  - deployment.create
```

Runtime должен уметь определить:

1. какие tools нужны;
2. какие tools доступны;
3. каких tools не хватает;
4. какие permissions требуются;
5. какие ограничения накладываются на tools.

Tool не должен существовать только как строка в Markdown.

---

# 10. Runtime requirements

Skill может требовать:

* sandbox;
* workstation;
* filesystem;
* network;
* credentials;
* MCP;
* конкретные версии инструментов;
* определённые environment capabilities.

Пример:

```yaml
runtime:
  sandbox: required

  network:
    mode: restricted

  filesystem:
    read:
      - repository

  credentials:
    - github.read

  mcp:
    - repository
    - ci
```

Runtime должен уметь проверить совместимость Skill с окружением **до запуска**.

---

# 11. Preconditions / Postconditions

Skill должен описывать ожидаемые условия начала и завершения операции.

```yaml
preconditions:
  - repository_available
  - revision_exists

postconditions:
  - deployment_created
  - service_verified
```

Postcondition не означает, что агент всегда достигнет его.

Execution должен фиксировать:

```text
expected postcondition
actual result
evidence
```

Это позволяет Temporality анализировать расхождения между intended procedure и actual trajectory.

---

# 12. Evidence

Skill может объявлять минимально необходимые evidence:

```yaml
evidence:
  required:
    - inspected_revision
    - test_result
    - deployment_result
```

Execution должен связывать:

```text
Skill
  ↓
Execution
  ↓
Operation
  ↓
Tool calls
  ↓
Artifacts / Evidence
```

Evidence используется для:

* проверки postconditions;
* формирования Memory;
* evaluations;
* evolution proposals;
* воспроизводимости.

---

# 13. Execution

Каждый запуск Skill должен быть отдельным Execution.

Execution должен фиксировать:

```text
skill_id
skill_version
capability
inputs
runtime
tools
trajectory
outputs
evidence
result
cost
duration
errors
memory_used
memory_created
```

Критически важно:

**Execution всегда привязан к конкретной версии Skill.**

Если Skill позднее изменился, старый execution не должен "перепривязываться" к новой версии.

---

# 14. Memory / Knowledge

Memory является отдельным temporal layer Temporality.

Она не входит физически в Skill package.

Memory представляет собой:

* наблюдение;
* опыт;
* закономерность;
* контекстное правило;
* обнаруженное ограничение;
* подтверждённое или опровергнутое знание.

Каждая Memory должна иметь:

```text
id
content
skill_id
capability
source executions
state
confidence
created_at
updated_at
last_used_at
usage_count
```

---

# 15. Memory должна быть контекстной

Memory не должна автоматически попадать в каждый execution Skill.

Для нового execution Temporality должен определить релевантные Memory на основании:

* Skill;
* capability;
* inputs;
* текущего состояния среды;
* контекста операции;
* исторической применимости.

Pipeline:

```text
current operation
       ↓
candidate memories
       ↓
relevance filtering
       ↓
compact memory context
       ↓
agent
```

Агент должен получать **не всю историю Skill**, а компактный контекст релевантного опыта.

---

# 16. Memory provenance

Каждое существенное знание должно иметь provenance.

Например:

```yaml
id: memory-183

skill:
  id: deploy-service
  capability: verify

content: >
  Service X requires approximately 30–50 seconds
  before its health endpoint becomes stable.

source:
  executions:
    - run-1827
    - run-1831
    - run-1842

evidence:
  - test-result-1827
  - deployment-result-1831

state: confirmed
```

Нельзя считать Memory подтверждённой только потому, что LLM её сформулировала.

---

# 17. Memory lifecycle

Минимальная state machine:

```text
proposed
    │
    ▼
confirmed
    │
    ├──────► invalidated
    │
    ▼
used
```

Дополнительно:

```text
deprecated
superseded
```

Изменение состояния Memory должно быть событием.

Не удалять историю состояния.

---

# 18. Memory reuse

При использовании Memory execution должен фиксировать:

```text
memory_id
why_selected
relevance_context
whether_used
effect
```

Например:

```text
memory.used

memory: memory-183
execution: run-1942
reason:
  service=X
  operation=health-check

result:
  avoided premature health check
```

Это позволяет анализировать не только наличие памяти, но и **реальное переиспользование опыта**.

---

# 19. Memory invalidation

Если execution показывает, что Memory больше не соответствует реальности, она не должна быть просто изменена.

Необходимо:

```text
old memory
    ↓
invalidated / superseded
    ↓
new memory
```

Например:

```text
M1:
"Service X requires 30–50 sec"

        ↓ evidence

M1:
superseded

M2:
"After v4 deployment service X usually becomes healthy
within 5–10 sec."
```

История должна сохраняться.

---

# 20. Skill Evolution

Skill может эволюционировать на основании:

* повторяющихся failures;
* trajectory patterns;
* evaluation results;
* evidence;
* накопленного Memory;
* изменений tools/runtime;
* явного решения человека.

Но:

**Memory не изменяет Skill автоматически.**

Сначала возникает evidence и evolution proposal.

---

# 21. Evolution Proposal

Агент может создать proposal:

```text
Skill Evolution Proposal

Skill: deploy-service
Current version: 1.2.0

Observed problem:
Health checks frequently execute too early.

Evidence:
14 executions

Related memories:
memory-183
memory-211

Proposed change:
Use wait_until_healthy(service)

Affected capabilities:
verify

Expected effect:
Reduce transient deployment failures.

Required evaluations:
deploy-service/health-check
```

Proposal является отдельным объектом Temporality.

---

# 22. Agent-driven evolution

Стандартный workflow:

```text
observe
   ↓
detect repeated pattern
   ↓
collect evidence
   ↓
find related memories
   ↓
create proposal
   ↓
validate
   ↓
run evaluations
   ↓
approval
   ↓
apply
   ↓
new Skill version
```

Агент не должен менять опубликованный Skill непосредственно на основании одного execution.

---

# 23. Human-driven evolution

Человек должен иметь возможность:

* посмотреть Skill;
* посмотреть contract;
* посмотреть capabilities;
* посмотреть tools;
* посмотреть executions;
* посмотреть Memory;
* посмотреть evidence;
* посмотреть evaluations;
* посмотреть evolution proposals;
* изменить Skill;
* подтвердить proposal;
* отклонить proposal;
* создать новую версию;
* откатить Skill.

Человек и агент должны работать с **одной моделью данных**.

---

# 24. Skill analysis tools

Минимальный набор инструментов.

## `skill.inspect`

Показывает:

```text
identity
version
description
capabilities
contract
tools
runtime
evaluation status
recent executions
related memory summary
evolution status
```

---

## `skill.validate`

Проверяет:

* manifest;
* schema;
* tools;
* capabilities;
* dependencies;
* runtime;
* preconditions;
* postconditions;
* evidence definitions.

---

## `skill.diff`

Показывает различия между версиями:

```text
instructions
contract
capabilities
tools
runtime
evaluations
```

Memory в Skill diff не включается.

Она отображается отдельно как temporal context.

---

## `skill.history`

Показывает:

```text
skill created
version created
execution
failure
evaluation
proposal
approval
new version
```

---

## `skill.executions`

Позволяет анализировать реальные применения Skill:

```text
list
inspect
filter
compare
```

Фильтры:

* version;
* capability;
* result;
* time;
* failure;
* cost;
* memory used.

---

## `skill.memory`

Не изменяет Skill.

Показывает Memory, связанные с:

* Skill;
* capability;
* конкретным execution.

Поддержать:

```text
list
search
inspect
```

Изменение состояния Memory должно выполняться отдельными memory operations.

---

## `skill.propose`

Создаёт Evolution Proposal.

---

## `skill.evaluate`

Запускает evaluations для Skill или proposal.

---

## `skill.apply`

Применяет утверждённый proposal.

Поддержать:

```text
dry-run
```

---

## `skill.rollback`

Создаёт новую версию, эквивалентную указанной предыдущей версии.

История не удаляется.

---

# 25. Memory tools

Memory должна иметь собственные инструменты.

Минимально:

```text
memory.search
memory.inspect
memory.confirm
memory.invalidate
memory.supersede
memory.history
```

Важно:

`skill.memory` — это навигация от Skill к Memory.

`memory.*` — управление самой Memory.

---

# 26. Agent tools

Для агента предоставить компактный MCP/tool interface:

```text
skill.search
skill.inspect
skill.validate
skill.history
skill.executions
skill.memory
skill.propose
skill.evaluate

memory.search
memory.inspect
memory.confirm
memory.invalidate
```

Изменяющие Skill операции разделить:

```text
read
propose
approve
apply
```

По умолчанию агент должен иметь возможность:

* наблюдать;
* анализировать;
* создавать proposals.

Прямое изменение опубликованного Skill должно контролироваться policy/approval.

---

# 27. Skill discovery

Runtime должен уметь находить подходящие Skill по:

* capability;
* input requirements;
* tool requirements;
* runtime requirements;
* описанию задачи.

Например:

```text
task:
  "review pull request"

       ↓

candidate capabilities:
  code.review

       ↓

candidate skills:
  github-code-review
  security-review
```

Discovery не должен загружать полный текст всех Skill в context LLM.

Сначала используется machine-readable metadata, затем загружается необходимая инструкция.

---

# 28. Skill composition

В будущем Skill может использовать другие Skill.

Например:

```text
release
 ├── code-review
 ├── build
 ├── deploy
 └── verify
```

Но composition не должна приводить к копированию их Memory внутрь родительского Skill.

Memory остаётся связанной с фактическими executions и capabilities.

---

# 29. Evaluations

Skill должен иметь evaluations.

Структура:

```text
evals/
  basic/
  regression/
  edge-cases/
```

Evaluation должна проверять не только output, но по возможности:

* правильность tools;
* preconditions;
* postconditions;
* evidence;
* trajectory;
* retries;
* cost;
* failure modes.

---

# 30. Skill quality

Не вводить единственный "Skill Score".

Хранить наблюдаемые показатели:

```text
success_rate
failure_rate
tool_error_rate
average_cost
average_duration
average_trajectory_length
retry_rate
postcondition_failure_rate
evidence_completeness
evaluation_pass_rate
memory_reuse_rate
```

Показатели должны быть доступны в динамике.

---

# 31. Memory analytics

Для каждой Memory должно быть видно:

```text
discovered
confirmed
used
revalidated
invalidated
superseded
```

Также:

```text
usage_count
last_used_at
source_execution_count
```

Особенно важно видеть:

* сколько раз Memory реально использовалась;
* в каких Skill/capabilities;
* какие execution её подтвердили;
* какие execution ей противоречат;
* когда она стала неактуальной.

---

# 32. Skill evolution analytics

Для Skill должна существовать temporal timeline:

```text
Skill v1.0
   │
   ├── execution
   ├── memory discovered
   ├── memory confirmed
   ├── memory reused
   ├── failures
   ├── evaluation
   ├── evolution proposal
   ├── human approval
   │
   ▼
Skill v1.1
```

Главная цель:

> видеть не только текущий Skill, но и историю того, **почему он стал таким**.

---

# 33. Skill snapshots

Для любой точки времени должна быть возможность получить состояние:

```text
skill state at T
```

Snapshot должен включать:

```text
skill version
skill contract
instructions
tools
runtime
evaluation definition
```

Memory не копируется внутрь Skill snapshot.

Вместо этого execution ссылается на использованный набор Memory.

---

# 34. Reproducibility

Для каждого execution должно быть возможно восстановить:

```text
skill_id
skill_version
skill_snapshot
capability
inputs
runtime
tools
memory_used
trajectory
evidence
outputs
```

Цель:

> Через любое время должно быть возможно понять, почему агент действовал именно так.

---

# 35. CLI

Добавить CLI:

```bash
temporality skill list

temporality skill inspect code-review

temporality skill validate code-review

temporality skill history code-review

temporality skill diff code-review 1.1.0 1.2.0

temporality skill executions code-review

temporality skill memory code-review

temporality skill eval code-review

temporality skill propose code-review

temporality skill apply <proposal-id>

temporality skill rollback code-review 1.1.0
```

Memory:

```bash
temporality memory search "health check"

temporality memory inspect <memory-id>

temporality memory history <memory-id>

temporality memory confirm <memory-id>

temporality memory invalidate <memory-id>

temporality memory supersede <memory-id> <new-memory-id>
```

Для каждого read-only command поддержать:

```bash
--json
```

Например:

```bash
temporality skill inspect code-review --json
```

---

# 36. API

Предоставить API для runtime и внешних клиентов.

Skill:

```text
GET  /skills
GET  /skills/{id}
GET  /skills/{id}/versions
GET  /skills/{id}/history
GET  /skills/{id}/executions
GET  /skills/{id}/memory
GET  /skills/{id}/evaluations

POST /skills/{id}/validate
POST /skills/{id}/evaluate
POST /skills/{id}/proposals
POST /skills/{id}/apply
```

Memory:

```text
GET  /memory
GET  /memory/{id}
GET  /memory/{id}/history

POST /memory/{id}/confirm
POST /memory/{id}/invalidate
POST /memory/{id}/supersede
```

API не должен обходить event model.

---

# 37. Event model

**Event stream остаётся единственным source of truth.**

Не создавать независимый mutable state, который становится вторым источником истины.

Допустимы materialized views и индексы.

Проекции:

```text
Skill state
Memory state
Execution state
Evolution timeline
Analytics
UI
```

должны восстанавливаться из событий.

---

# 38. Events

Минимальный vocabulary:

```text
skill.created
skill.updated
skill.version.created
skill.validated
skill.evaluated

skill.proposal.created
skill.proposal.approved
skill.proposal.rejected
skill.proposal.applied

skill.execution.started
skill.execution.completed
skill.execution.failed

memory.proposed
memory.confirmed
memory.used
memory.invalidated
memory.superseded

evaluation.started
evaluation.completed
evaluation.failed
```

Каждое событие должно иметь provenance.

---

# 39. Skill evolution не должна менять прошлое

Если:

```text
Skill v1.0
```

использовался в execution #100, а затем появился:

```text
Skill v1.1
```

execution #100 должен навсегда оставаться связанным с v1.0.

То же самое относится к Memory.

Если Memory была подтверждена на момент execution #100, а затем инвалидирована, это не должно изменять историческое состояние execution #100.

---

# 40. Legacy skills

Существующие skills вида:

```text
SKILL.md
scripts/
```

должны продолжать работать.

Если `skill.yaml` отсутствует, runtime должен загрузить legacy Skill.

Добавить:

```bash
temporality skill migrate <path>
```

который создаёт initial manifest.

Автоматически определённые поля должны быть помечены как `inferred`.

---

# 41. Migration

Pipeline:

```text
legacy SKILL.md
       ↓
parse
       ↓
infer metadata
       ↓
generate skill.yaml
       ↓
validate
       ↓
human review
       ↓
Skill v1.0
```

Migration не должна менять фактическое поведение Skill.

---

# 42. Security

Skill является потенциально исполняемой capability.

Необходимо явно контролировать:

* tool permissions;
* credentials;
* sandbox;
* network;
* filesystem;
* MCP access;
* approval requirements.

Skill не получает permission только потому, что permission упомянут в `SKILL.md`.

**Manifest описывает требуемые capabilities, runtime policy определяет реально предоставленные permissions.**

---

# 43. MVP

Первый этап должен быть минимальным.

### Обязательно:

1. `skill.yaml`;
2. `SKILL.md`;
3. legacy Skill loading;
4. Skill registry;
5. Contract validation;
6. Capability declarations;
7. Tool declarations;
8. Skill versions;
9. Execution → Skill version linkage;
10. Memory objects;
11. Memory lifecycle;
12. Memory → execution provenance;
13. Memory reuse tracking;
14. event-based evolution;
15. `skill.inspect`;
16. `skill.validate`;
17. `skill.history`;
18. `skill.executions`;
19. `skill.memory`;
20. CLI;
21. JSON API.

### Не требуется в MVP:

* автоматическое изменение Skill агентом;
* полноценный UI editor;
* сложный evaluation engine;
* marketplace;
* автоматический Skill generation;
* сложная policy engine.

---

# 44. Phase 2

После MVP:

1. evolution proposals;
2. agent-driven proposals;
3. evaluations;
4. evidence-aware evaluations;
5. human approval;
6. Skill diff;
7. rollback;
8. contextual Memory retrieval;
9. evolution analytics;
10. UI.

---

# 45. Phase 3

Исследовать:

* автоматическое обнаружение деградации Skill;
* обнаружение систематических trajectory failures;
* автоматическое обнаружение устаревших Memory;
* Skill composition;
* capability discovery;
* Skill dependencies;
* automatic proposal generation;
* cross-project Skill reuse.

---

# 46. UI

На существующем UI Temporality добавить представление Skill.

Разделы:

```text
Overview
Contract
Capabilities
Tools
Executions
Memory
Evaluations
Evolution
Versions
```

Главный экран:

```text
Code Review
v1.2.0

Capabilities
  inspect_code
  inspect_diff
  run_tests
  report_findings

Tools
  repository
  CI

Executions
  183

Memory
  17 confirmed
  3 invalidated
  8 recently reused

Evaluations
  42 / 44 passed

Evolution
  6 changes
  2 pending proposals
```

Важно:

**Memory должна визуально оставаться отдельным слоем.**

Не создавать впечатление, что `17 confirmed memories` являются частью файла Skill.

---

# 47. Human editing workflow

Человек изменяет:

```text
SKILL.md
skill.yaml
tools
evals
```

Pipeline:

```text
edit
  ↓
validate
  ↓
run affected evaluations
  ↓
show diff
  ↓
create new version
```

Опубликованную версию нельзя silently изменять.

---

# 48. Agent evolution workflow

Агент:

```text
execute Skill
      ↓
observe trajectory
      ↓
find repeated problem
      ↓
search Memory
      ↓
collect evidence
      ↓
form evolution proposal
      ↓
validate
      ↓
evaluate
      ↓
request approval
      ↓
apply
      ↓
new Skill version
```

Ключевой момент:

**агент не обязан менять Skill при каждом обнаружении нового знания.**

Большинство discoveries должны оставаться Memory.

Skill меняется только тогда, когда накопленный evidence показывает, что **сама canonical capability/procedure должна измениться**.

---

# 49. Критерий разделения Memory и Skill

При каждом потенциальном изменении необходимо задавать вопрос:

> Это знание о конкретной ситуации или изменение канонического способа выполнения capability?

Если:

```text
"В service X после deploy надо ждать 40 секунд"
```

это Memory.

Если:

```text
"Проверка health должна ждать готовности сервиса,
а не выполняться один раз сразу после deploy"
```

это потенциальное изменение Skill.

Таким образом:

```text
specific/contextual
        → Memory

general/canonical
        → Skill Evolution
```

Это правило не обязано приниматься автоматически: агент может предложить классификацию, а evidence и человек определяют результат.

---

# 50. Критерии готовности

Реализованный MVP должен позволять выполнить следующий end-to-end сценарий.

## Scenario

Создать Skill:

```text
code-review
v1.0.0
```

с:

```text
SKILL.md
skill.yaml
tools
evals
```

Запустить несколько executions.

Во время executions агент обнаруживает:

```text
repository X
requires special test setup
```

Создаётся Memory:

```text
proposed
   ↓
confirmed
```

Memory используется в следующих executions:

```text
memory.used
```

Через несколько запусков появляется повторяющийся failure.

Temporality показывает:

```text
14 executions
7 related memories
9 failures
same trajectory pattern
```

Агент формирует Evolution Proposal:

```text
current Skill: v1.0.0

observed problem:
...

evidence:
...

related memory:
...

proposed change:
...

affected capability:
...

evaluation:
...
```

Человек рассматривает proposal.

После approval:

```text
Skill v1.1.0
```

создаётся как новая версия.

При этом старые executions остаются привязанными к:

```text
Skill v1.0.0
```

и сохраняют:

```text
skill snapshot
memory used
trajectory
tools
evidence
```

Можно открыть timeline:

```text
Skill v1.0
    │
    ├── execution
    ├── execution
    ├── memory discovered
    ├── memory confirmed
    ├── memory reused
    ├── repeated failure
    ├── evolution proposal
    ├── evaluation
    ├── approval
    │
    ▼
Skill v1.1
    │
    ├── execution
    ├── memory reused
    └── improved result
```

---

# 51. Главный принцип системы

Skill не должен превращаться в ещё один формат prompt engineering.

Skill — это:

> **версионируемая capability с каноническим способом выполнения, контрактом, инструментами и runtime requirements.**

Memory — это:

> **временное и контекстное знание, полученное из реальных executions и подтверждённое evidence.**

Execution — это:

> **наблюдаемая история фактического применения capability.**

Evolution — это:

> **процесс изменения Skill на основании накопленного evidence, evaluations и опыта.**

В результате Temporality замыкает цикл:

```text
              ┌────────────────────┐
              │       SKILL        │
              │ capability         │
              │ canonical method   │
              └─────────┬──────────┘
                        │
                     execute
                        │
                        ▼
              ┌────────────────────┐
              │     EXECUTION      │
              │ trajectory         │
              │ tools              │
              │ evidence           │
              └─────────┬──────────┘
                        │
                     observe
                        │
                        ▼
              ┌────────────────────┐
              │      MEMORY        │
              │ contextual         │
              │ experience         │
              └─────────┬──────────┘
                        │
                     reuse
                        │
                        ├───────────────► next execution
                        │
                   enough evidence
                        │
                        ▼
              ┌────────────────────┐
              │     EVOLUTION      │
              │ proposal           │
              │ evaluation         │
              │ approval           │
              └─────────┬──────────┘
                        │
                        ▼
                  SKILL vNext
```

**Ключевое свойство архитектуры: Skill остаётся относительно стабильным и переносимым артефактом, а Temporality становится памятью его эксплуатации. Skill учится не за счёт разрастания `SKILL.md`, а за счёт наблюдаемого цикла `execution → memory → evidence → evolution → new skill version`.**
