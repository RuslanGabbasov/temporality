# Task: спроектировать и собрать базовый Agent Harness с нуля

## 1. Контекст

Нужно спроектировать новую инфраструктуру для запуска и эксплуатации AI-агентов **с чистого листа**, не наследуя архитектуру GoClaw, LangGraph, TrueForge или другого существующего harness.

Главная архитектурная особенность проекта — с самого начала встроить в runtime контракт с **Temporality**.

Temporality — это система temporal observability для эволюции коллективного знания и работы агентной команды.

Она должна позволять отвечать на вопросы:

* что команда знала в момент `T`;
* откуда появилось конкретное знание;
* кто его создал;
* кто его использовал;
* кто его подтвердил или оспорил;
* какие знания были исправлены или признаны недействительными;
* как знания распространялись между агентами;
* какие знания переиспользуются;
* где команда повторно решает уже решённые проблемы;
* где возникает knowledge silo;
* как менялось состояние проекта во времени;
* какие действия/события привели к изменению знания;
* как восстановить trajectory выполнения агента.

Ключевой принцип:

> **Temporality — не observability plugin, а контракт Agent Kernel.**

Каждая существенная операция runtime должна иметь возможность порождать канонические Temporality events.

При этом Temporal и Temporality решают разные задачи:

* **Temporal** отвечает за durable execution;
* **Temporality** отвечает за семантическую историю runtime и эволюцию знания.

Не следует использовать Temporal History как хранилище Temporality.

---

# 2. Цель

Разработать архитектуру и минимальный working PoC нового Agent Harness, в котором:

```text
Client
   |
   v
Agent Kernel
   |
   +---- Model Gateway
   |
   +---- MCP
   |
   +---- Sandbox
   |
   +---- Delegation
   |
   +---- Human Approval
   |
   +---- Memory / Knowledge
   |
   +---- Temporality Contract
   |
   v
Temporal
```

а Temporality получает независимый canonical event/frame stream:

```text
Agent Kernel
      |
      v
Temporality Event Stream
      |
      +---- Runtime projection
      +---- Knowledge projection
      +---- Timeline
      +---- Provenance
      +---- Replay
      +---- Analytics
```

Результатом должен быть не просто обзор технологий, а **конкретное архитектурное решение**, подтверждённое PoC.

---

# 3. Основные требования

## 3.1. Agent Kernel

Нужен небольшой собственный runtime/kernel.

Не следует строить kernel вокруг LangGraph или другого workflow framework.

Kernel должен иметь минимальный набор примитивов:

```text
turn()
model()
tool()
observe()
delegate()
approve()
remember()
emit()
```

Конкретный API может отличаться, если исследование покажет более удачную модель.

Важно, чтобы операции были:

* composable;
* observable;
* resumable;
* cancellable;
* durable там, где это необходимо;
* связаны с actor/run/frame;
* способствовали формированию Temporality event stream.

---

# 4. Temporal как execution substrate

Исследовать и проверить использование **Temporal** как базового механизма durable execution.

Temporal должен использоваться для:

* workflows;
* retries;
* crash recovery;
* resumability;
* timers;
* signals;
* cancellation;
* child workflows;
* external events;
* human approval;
* длительных задач;
* deterministic replay.

Нужно отдельно определить:

### Что является Temporal Workflow

Например:

```text
AgentRun
Task
Delegation
Approval
Long-running execution
Scheduled execution
```

### Что является Activity

Например:

```text
LLM call
MCP call
Sandbox operation
Repository operation
External API
```

### Что остаётся обычным runtime operation

Необходимо исследовать, нужен ли для каждой короткой операции отдельный Temporal workflow/activity или нужен fast-path внутри Agent Kernel.

Особенно проверить архитектуру:

```text
short / interactive execution
        |
        v
Agent Kernel
        |
        +---- ephemeral operation

long-running / durable execution
        |
        v
Temporal Workflow
```

Не принимать автоматически решение, что абсолютно каждый turn должен быть отдельным Temporal Workflow.

---

# 5. Temporality Contract

Спроектировать canonical event contract.

Минимальный envelope:

```json
{
  "event_id": "...",
  "timestamp": "...",

  "run_id": "...",
  "frame_id": "...",
  "parent_frame_id": "...",

  "actor": {
    "id": "...",
    "type": "agent|human|system|service"
  },

  "operation": {
    "type": "..."
  },

  "context": {
    "project_id": "...",
    "task_id": "...",
    "parent_run_id": "..."
  },

  "causality": {
    "caused_by": []
  },

  "payload": {},

  "evidence": [],

  "knowledge": {
    "created": [],
    "used": [],
    "challenged": [],
    "modified": []
  }
}
```

Это только исходная модель.

Нужно критически её пересмотреть и предложить окончательную версию.

---

# 6. Runtime events

Определить стандартные runtime events.

Минимально исследовать:

```text
agent.started
agent.completed
agent.failed

run.started
run.completed
run.failed
run.cancelled

turn.started
turn.completed

model.started
model.completed
model.failed

tool.started
tool.completed
tool.failed

mcp.call.started
mcp.call.completed
mcp.call.failed

sandbox.created
sandbox.destroyed

delegation.started
delegation.completed
delegation.failed

approval.requested
approval.granted
approval.rejected

context.created
context.compacted

memory.read
memory.write
```

Необходимо определить:

* обязательные поля;
* correlation IDs;
* parent/child relationship;
* causality;
* idempotency;
* timestamps;
* actor;
* resource references;
* error model.

---

# 7. Knowledge events

Отдельно определить semantic knowledge lifecycle.

Минимально:

```text
knowledge.discovered
knowledge.created
knowledge.asserted
knowledge.inferred
knowledge.adopted
knowledge.reused

knowledge.confirmed
knowledge.challenged
knowledge.corrected

knowledge.superseded
knowledge.invalidated
knowledge.expired
knowledge.forgotten
```

Важно не смешивать:

```text
runtime event
```

и

```text
knowledge event
```

Например:

```text
tool.completed
```

не означает автоматически:

```text
knowledge.created
```

Однако runtime event может породить knowledge event:

```text
MCP result
   |
   v
agent inference
   |
   v
knowledge.asserted
```

Необходимо определить связь между этими событиями.

---

# 8. Knowledge model

Исследовать и определить минимальную модель знания.

Минимальные сущности:

```text
Actor
KnowledgeNode
KnowledgeEvent
Evidence
Relationship
Frame
Execution
Project
Task
```

### KnowledgeNode

Может представлять:

* fact;
* claim;
* hypothesis;
* decision;
* constraint;
* assumption;
* solution;
* observation;
* requirement;
* conclusion.

Не нужно пытаться сразу построить универсальную ontology.

Главное — обеспечить:

* identity;
* temporal validity;
* provenance;
* evidence;
* relationships;
* lifecycle.

---

# 9. Provenance

Provenance должен быть first-class сущностью.

Для любого значимого знания должно быть возможно узнать:

```text
кто создал
↓
на основании чего
↓
каким инструментом
↓
в каком execution
↓
в каком контексте
↓
кто использовал
↓
кто подтвердил
↓
кто оспорил
↓
было ли исправлено
```

Пример:

```text
Knowledge K123
    |
    +-- created_by -> Agent A
    |
    +-- derived_from -> Tool Result R55
    |
    +-- evidence -> commit abc123
    |
    +-- reused_by -> Agent B
    |
    +-- challenged_by -> Agent C
    |
    +-- superseded_by -> Knowledge K201
```

---

# 10. Evidence

Исследовать модель Evidence.

Типы:

```text
document
URL
repository file
commit
pull request
test result
command output
tool result
MCP result
execution result
human statement
other knowledge
```

Evidence должно иметь:

* stable ID;
* type;
* source;
* timestamp;
* content reference;
* hash;
* optional metadata.

Большие данные не должны храниться внутри event payload.

Использовать ссылки на object storage.

---

# 11. Storage

Для MVP использовать:

## PostgreSQL

Исследовать схему для:

```text
events
frames
actors
executions
knowledge_nodes
knowledge_events
relationships
evidence
projects
tasks
```

Допускается:

```text
JSONB
pgvector
```

Не вводить без необходимости:

* Neo4j;
* Weaviate;
* Milvus;
* Kafka;
* ClickHouse.

Нужно объяснить для каждого из них, почему он сейчас не нужен.

Архитектура должна позволять добавить их позднее.

---

# 12. Event sourcing / projections

Ключевой принцип:

> **Immutable temporal event stream — source of truth.**

Текущее состояние должно быть projection.

Например:

```text
events
   |
   +----> current knowledge projection
   |
   +----> agent projection
   |
   +----> project projection
   |
   +----> timeline
   |
   +----> provenance graph
```

Не делать текущий граф единственным source of truth.

Нужно обеспечить возможность:

```text
state(T1)
state(T2)
diff(T1, T2)
```

и:

```text
replay(run)
```

---

# 13. Model Gateway

Исследовать использование **LiteLLM** как model gateway.

Проверить:

* provider abstraction;
* model aliases;
* routing;
* fallbacks;
* retries;
* rate limits;
* usage;
* cost accounting;
* streaming;
* structured output;
* tool calling.

Сделать так, чтобы Agent Kernel не зависел от конкретного LLM provider.

Каждый model call должен иметь:

```text
actor
run
frame
model
provider
input reference
output reference
token usage
latency
cost
```

и быть связан с Temporality.

---

# 14. MCP

Использовать официальный MCP SDK / protocol.

MCP должен быть first-class capability runtime.

Минимально:

```text
discover tools
call tool
receive result
handle errors
timeouts
authorization
```

Каждый MCP call должен иметь Temporality representation:

```text
mcp.call.started
mcp.call.completed
mcp.call.failed
```

с:

```text
server
tool
actor
arguments hash
result reference
execution
frame
evidence
```

Не хранить большие tool arguments/results непосредственно в event stream.

---

# 15. Sandbox

Спроектировать abstraction:

```text
Sandbox
```

с возможностью backend:

```text
local
Docker
Kubernetes
Daytona
remote worker
```

Не делать Daytona архитектурной зависимостью.

Минимальный API:

```text
create()
exec()
read()
write()
destroy()
```

Исследовать:

* isolation;
* lifecycle;
* persistence;
* filesystem;
* network;
* resource limits;
* credentials;
* observability.

Sandbox operations должны быть связаны с execution/frame.

---

# 16. Delegation

Delegation должен быть native primitive Agent Kernel.

Пример:

```text
Lead
 ├── delegate(Coder)
 ├── delegate(Reviewer)
 └── delegate(QA)
```

Для delegation фиксировать:

```text
parent_run
child_run

delegated_by
delegated_to

task
constraints
context_inherited

knowledge_inherited
knowledge_created
knowledge_returned
```

Нужно проверить, насколько естественно это моделируется через Temporal child workflows.

---

# 17. Human approval

Human-in-the-loop должен быть first-class.

Примеры:

```text
approval.requested
approval.granted
approval.rejected
```

Approval может происходить:

* в UI;
* через API;
* через Matrix;
* через другой notification channel.

Не делать approval hack поверх обычного tool call.

Проверить Temporal Signals / Updates / external events как механизм ожидания approval.

---

# 18. Memory

Не делать отдельную магическую систему:

```text
agent memory = vector database
```

Вместо этого:

```text
Temporality knowledge events
        |
        +---- current memory projection
        |
        +---- semantic retrieval
        |
        +---- temporal retrieval
```

Исследовать:

* PostgreSQL;
* pgvector;
* temporal filtering;
* provenance-aware retrieval.

Memory должна позволять ответить:

```text
что агент знает?
```

но также:

```text
почему он это знает?
```

и:

```text
когда он это узнал?
```

---

# 19. Context management

Исследовать context management.

Нужны:

```text
context window management
compaction
summarization
retrieval
evidence preservation
```

Критическое требование:

> Context compaction не должна уничтожать provenance.

Если:

```text
100 событий
    ↓
summary
```

должна сохраняться связь:

```text
summary
   |
   +-- derived_from -> original frames/events
```

Не допускать появления "магических summary", происхождение которых невозможно установить.

---

# 20. Code Mode

Исследовать подход Code Mode:

```text
LLM
 |
 | generates program
 v
execution environment
 |
 +-- tool calls
 +-- APIs
 +-- filesystem
 +-- MCP
```

Проверить, имеет ли смысл такой режим для нового harness.

Особенно важно:

* durable execution;
* approvals;
* sandbox;
* observability;
* provenance;
* ограничения ресурсов.

Не обязательно включать Code Mode в MVP, но архитектура не должна его блокировать.

---

# 21. Open-source research

Провести актуальное исследование существующих OSS-компонентов.

Минимально исследовать:

### Durable execution

```text
Temporal
Restate
DBOS
Hatchet
Inngest
Trigger.dev
```

### Agent frameworks

```text
PydanticAI
OpenAI Agents SDK
LangGraph
Temporal Agent Harness
TrueForge
SandBase
```

### Model gateways

```text
LiteLLM
```

### MCP

```text
official MCP SDKs
```

### Sandbox

```text
Docker
Daytona
Kubernetes
other relevant open-source runtimes
```

Для каждого компонента определить:

```text
что даёт
что забирает
архитектурная цена
license
maturity
community
extensibility
durability
observability
Temporality integration
```

Не делать обзор ради обзора.

Исследование должно привести к конкретному выбору.

---

# 22. Особое внимание Temporal Agent Harness

Обязательно подробно исследовать:

**Temporal Agent Harness**

Проверить:

* его execution model;
* agent/workflow mapping;
* tool/activity mapping;
* event stream;
* replay;
* approval model;
* subagents;
* Code Mode;
* integrations;
* typed interfaces;
* насколько его архитектуру можно использовать как reference design.

Не копировать проект автоматически.

Нужно ответить:

> Какие идеи стоит взять, какие не стоит брать и почему?

---

# 23. Что НЕ использовать как фундамент без доказательства необходимости

Не строить архитектуру вокруг:

```text
LangGraph
Neo4j
Kafka
ClickHouse
vector DB
LangChain
specific LLM provider
Daytona
GoClaw
TrueForge
```

Это не означает запрет.

Для каждого такого компонента допускается использование, если PoC/исследование показывает объективную необходимость.

---

# 24. OpenTelemetry

Использовать OpenTelemetry для:

```text
metrics
logs
infrastructure traces
```

Но:

> OpenTelemetry не должен определять semantic model Temporality.

Например:

```text
OTel span:
    model.call
```

не заменяет:

```text
Temporality event:
    knowledge.asserted
```

OTel и Temporality должны быть связаны correlation IDs.

---

# 25. Event bus

Для MVP:

```text
Agent Kernel
      |
      v
PostgreSQL
```

без Kafka/NATS.

Если появится необходимость нескольких consumers:

```text
Agent Kernel
      |
      v
NATS JetStream
      |
      +---- Knowledge projection
      +---- Analytics
      +---- Live UI
      +---- Search
```

Исследовать NATS JetStream как возможный следующий шаг.

---

# 26. Large artifacts

Большие данные:

* tool output;
* logs;
* files;
* model responses;
* screenshots;
* execution artifacts;

не хранить в PostgreSQL event payload.

Использовать:

```text
S3-compatible storage
```

например:

```text
MinIO
```

Event хранит:

```text
artifact_id
uri
hash
size
mime_type
```

---

# 27. UI

MVP UI должен быть не generic graph viewer.

Основная концепция:

```text
Knowledge Cloud
      +
Timeline
      +
Provenance
```

Основные режимы:

### Timeline

Показывает развитие работы во времени.

### Knowledge Cloud

Показывает актуальные знания и их состояние.

### Provenance

Показывает происхождение конкретного знания.

### Agent view

Показывает:

```text
что создаёт агент
что использует
что оспаривает
что передаёт другим
```

### Project progress

Показывает прогресс через изменения знания.

### Problem hotspots

Показывает:

* repeated failures;
* repeated rediscovery;
* conflicting knowledge;
* stale knowledge;
* knowledge silos.

### Replay

Позволяет смотреть состояние:

```text
T0
T1
T2
...
```

---

# 28. Graph usage

Граф допускается как internal data model / drill-down.

Но UI не должен начинаться с:

```text
billions of nodes + edges
```

Основной интерфейс должен быть temporal.

Граф используется для ответа на конкретные вопросы:

```text
откуда пришло это знание?
кто его использовал?
какие знания от него зависят?
```

---

# 29. Temporal queries

Архитектура должна поддерживать запросы:

```text
What did the team know at T?

What changed between T1 and T2?

Where did this knowledge come from?

Who introduced this knowledge?

Who reused it?

Who challenged it?

What knowledge became invalid?

Which agent spreads a particular knowledge?

Which knowledge is reused across projects?

Where is knowledge being rediscovered?

Which knowledge is stale?

What happened after event X?

What changed after commit Y?

What knowledge did agent A inherit from agent B?
```

Нужно показать SQL/examples для нескольких таких запросов.

---

# 30. Security

Исследовать:

* tenant isolation;
* actor identity;
* authorization;
* secrets;
* sandbox credentials;
* tool permissions;
* model permissions;
* approval policies.

Особенно важно:

```text
agent should not automatically receive all project knowledge
```

Knowledge retrieval должен учитывать authorization/context.

---

# 31. Idempotency

Определить idempotency strategy для всех важных операций.

Особенно:

```text
tool call
MCP call
knowledge event
Temporality event emission
Temporal activity retry
projection
```

Повторная доставка event не должна приводить к повреждению состояния.

---

# 32. Failure model

Нужно явно описать поведение при:

```text
LLM timeout
LLM failure
MCP timeout
MCP failure
sandbox crash
worker crash
Temporal worker restart
Postgres outage
Temporality storage outage
duplicate event
lost event
partial execution
agent cancellation
human approval timeout
```

Особенно важно определить:

> Что происходит, если Agent Kernel успешно выполнил действие, но не смог записать Temporality event?

Это принципиальный вопрос архитектуры.

Необходимо выбрать и обосновать модель:

```text
at-most-once
at-least-once
effect + event transaction
outbox
local durable buffer
Temporal activity + outbox
```

---

# 33. Event delivery semantics

Определить:

```text
ordering
causality
delivery guarantee
deduplication
partitioning
retention
replay
schema evolution
```

Не считать глобальный ordering обязательным.

Достаточно обеспечить причинный порядок:

```text
A caused B
```

через:

```text
parent_frame_id
caused_by
sequence
logical clock
```

если это необходимо.

---

# 34. Schema evolution

Temporality contract должен иметь:

```text
schema_version
event_type
```

Нужно описать:

* backward compatibility;
* evolution;
* deprecated events;
* migration;
* unknown fields;
* consumer compatibility.

---

# 35. Language choice

Исследовать два варианта:

### Option A

```text
Go Agent Kernel
Python/TS agent workers
```

### Option B

```text
Python Agent Kernel
```

### Option C

```text
TypeScript Agent Kernel
```

Оценить:

* Temporal SDK;
* MCP ecosystem;
* LLM ecosystem;
* sandbox ecosystem;
* type safety;
* performance;
* developer velocity;
* operational simplicity.

Выбор должен быть аргументирован.

Не выбирать язык только по привычке.

---

# 36. MVP

MVP должен включать:

```text
Temporal
Agent Kernel
Model Gateway
MCP
Sandbox
Temporality event contract
PostgreSQL
S3-compatible artifact storage
basic knowledge model
basic provenance
basic UI
```

Минимальный сценарий:

```text
User
 |
 v
Lead Agent
 |
 +-- delegate --> Coder
 |
 +-- delegate --> Reviewer
 |
 +-- delegate --> QA
```

Например, задача:

```text
"Исправить баг в repository"
```

В процессе:

```text
Lead starts
   |
Coder starts
   |
repository inspected
   |
knowledge discovered
   |
code changed
   |
test executed
   |
knowledge confirmed
   |
Reviewer challenges assumption
   |
Coder corrects
   |
QA confirms
   |
knowledge stabilized
   |
Lead receives result
```

Temporality должна позволить визуально восстановить эту историю.

---

# 37. Минимальный PoC

PoC должен демонстрировать:

1. запуск agent run;
2. model call;
3. MCP/tool call;
4. sandbox command;
5. delegation;
6. human approval;
7. создание knowledge node;
8. использование knowledge;
9. challenge;
10. correction;
11. provenance;
12. replay;
13. temporal query;
14. отображение timeline.

---

# 38. Expected repository structure

Предлагаемая структура:

```text
/
├── docs/
│   ├── architecture.md
│   ├── decisions/
│   ├── temporality-contract.md
│   ├── event-model.md
│   ├── knowledge-model.md
│   ├── failure-model.md
│   └── oss-research.md
│
├── kernel/
│
├── temporality/
│
├── runtime/
│
├── model-gateway/
│
├── mcp/
│
├── sandbox/
│
├── storage/
│
├── examples/
│
├── ui/
│
├── tests/
│
└── README.md
```

Структуру можно изменить после исследования.

---

# 39. Architecture Decision Records

Все существенные решения оформить как ADR.

Минимально:

```text
ADR-001 Temporal as durable execution substrate
ADR-002 Temporality as separate semantic event stream
ADR-003 PostgreSQL as initial event/knowledge store
ADR-004 S3-compatible storage for artifacts
ADR-005 Model gateway choice
ADR-006 MCP integration
ADR-007 Sandbox abstraction
ADR-008 Knowledge model
ADR-009 Event delivery semantics
ADR-010 Language choice
ADR-011 Fast-path vs Temporal execution
ADR-012 Memory as Temporality projection
```

Каждый ADR должен содержать:

```text
Context
Decision
Alternatives
Consequences
```

---

# 40. Research methodology

Не ограничиваться документацией.

Для ключевых компонентов:

1. изучить официальную документацию;
2. посмотреть актуальный GitHub;
3. проверить license;
4. проверить release activity;
5. изучить архитектуру;
6. запустить минимальный пример;
7. проверить интеграцию;
8. сделать вывод.

Для критических утверждений приводить ссылки на первоисточники.

Не использовать старые статьи вместо актуальной документации, если проект быстро меняется.

---

# 41. Deliverables

В результате должны быть предоставлены:

## 1. Architecture

```text
docs/architecture.md
```

С полной схемой компонентов и потоков данных.

## 2. Temporality Contract

```text
docs/temporality-contract.md
```

С конкретной схемой событий.

## 3. Knowledge Model

```text
docs/knowledge-model.md
```

## 4. OSS Research

```text
docs/oss-research.md
```

Сравнение и конкретные рекомендации.

## 5. ADRs

Набор ADR по ключевым архитектурным решениям.

## 6. Working PoC

Минимальный запускаемый runtime.

## 7. Database schema

SQL migrations.

## 8. Example

End-to-end пример:

```text
Lead → Coder → Reviewer → QA
```

## 9. Tests

Минимальные integration tests.

## 10. README

Должен позволять разработчику:

```text
clone
install
run
execute example
open UI
inspect timeline
```

---

# 42. Definition of Done

Работа считается выполненной, если:

Статус на 2026-09-24: `[x]` — решение или реализация есть в репозитории и покрыта доступной проверкой; `[ ]` — критерий ещё не выполнен либо требует end-to-end проверки на сервисах. Отметки не означают, что весь Definition of Done закрыт.

### Architecture

* [x] выбран durable execution substrate — Temporal; решение в `docs/decisions/001-temporal-execution.md`;
* [x] выбран язык — Go; `docs/decisions/010-language.md`;
* [x] определена Agent Kernel abstraction — `docs/architecture.md`;
* [x] определено разделение Temporal / Temporality;
* [x] определена event model — `docs/temporality-contract.md`;
* [x] определена knowledge model — `docs/knowledge-model.md`;
* [x] определён provenance model;
* [x] определён failure model — `docs/failure-model.md`;
* [x] определена delivery semantics — PostgreSQL outbox и at-least-once доставка;
* [x] определена schema evolution strategy.

### Runtime

* [x] Agent Kernel запускает agent через Temporal `AgentRun`;
* [x] выполняется model call через OpenAI-compatible клиент;
* [x] выполняется MCP/tool call — allowlisted stdio adapter и встроенный tool; реальный MCP сервер ещё не прогонялся end-to-end;
* [ ] работает sandbox — Docker backend и unit tests добавлены, но запуск через живой Docker daemon не проверен;
* [ ] работает delegation в полном сценарии — child workflows и opt-in пример реализованы и проходят workflow test с моделью-заглушкой; полный сервисный прогон не выполнен;
* [x] работает approval — Temporal signal, обязательное ожидание и timeout;
* [x] операции durable там, где это требуется — Workflow history и outbox; reconciliation внешних эффектов ещё не завершён.

### Temporality

* [x] runtime events генерируются автоматически Kernel;
* [x] knowledge events генерируются отдельно от runtime events (`knowledge.proposed`, `knowledge.used`);
* [x] события имеют provenance — source, actor, run/task и evidence refs;
* [x] события имеют causality — `parent_event_id`, `caused_by`, frame и sequence;
* [x] события immutable — observation journal append-only;
* [x] knowledge projection восстанавливается из event stream;
* [ ] replay trajectory AgentRun из Temporality event stream — существующий FRP replay не восстанавливает полный Kernel run;
* [x] можно получить state at T для knowledge через `as_of` и `known_at`; реконструкция полного состояния внешнего workspace ещё не реализована.

### Knowledge

* [x] knowledge nodes имеют project-scoped identity;
* [x] knowledge имеет lifecycle;
* [x] evidence first-class в event contract;
* [x] provenance восстанавливается из истории событий;
* [x] knowledge можно challenge/correct/supersede/invalidate через observation contract/API;
* [x] memory является projection Temporality и передаётся как отдельный контекст.

### PoC

* [ ] Lead/Coder/Reviewer/QA scenario работает end-to-end с реальной моделью, tools и sandbox — orchestration тестируется с заглушками; подключается отдельно через build tag `agent_examples`;
* [x] UI показывает timeline observation events;
* [x] можно открыть provenance и source event;
* [x] можно посмотреть evolution конкретного знания;
* [x] можно выполнить temporal query по knowledge через `as_of`/`known_at`;
* [ ] можно воспроизвести полный AgentRun execution из Temporality event stream.

**Незакрытые блокирующие пункты:** live multi-service прогон Temporal + PostgreSQL + Temporality + Docker; sandbox security validation; полноценный Lead/Coder/Reviewer/QA run; replay AgentRun из семантических событий; reconciliation неопределённых внешних side effects. Полный `go build ./...` и целевые Kernel workflow/unit tests проходят; полный repository test suite ограничен loopback-запретом sandbox и существующими тестами путей macOS.

---

# 43. Важные архитектурные ограничения

Не превращать проект в:

### Generic APM

```text
CPU
RAM
latency
request rate
```

Это не цель проекта.

### Langfuse clone

LLM tracing сам по себе недостаточен.

### Generic graph database

Граф не является главным UX.

### Vector-memory product

Embeddings не являются моделью знания.

### Workflow engine with LLM attached

Durable execution необходим, но сам по себе не решает проблему collective knowledge.

### AI dashboard

Цель — не красивый dashboard, а возможность реконструировать эволюцию работы и знания.

---

# 44. Главный архитектурный инвариант

Система должна позволять перейти от:

```text
"What did the agent do?"
```

к:

```text
"What did the team learn?"
```

и дальше:

```text
"How did the team learn it?"
```

и:

```text
"How did that knowledge change over time?"
```

и:

```text
"How did that knowledge affect subsequent work?"
```

---

# 45. Главный принцип интеграции с Temporality

Agent Kernel должен считать Temporality частью своего runtime contract.

Не должно существовать архитектуры:

```text
Agent Kernel
     |
     v
runtime
     |
     +---- optional observability plugin
                 |
                 v
             Temporality
```

Предпочтительно:

```text
                Agent Kernel
                     |
       +-------------+-------------+
       |             |             |
    Runtime       Evidence     Knowledge
       |             |             |
       +-------------+-------------+
                     |
                     v
             Temporality Contract
                     |
                     v
             Temporal Event Stream
```

При этом Temporality не должна становиться причиной жёсткой связанности всего runtime с конкретной реализацией хранилища.

Нужен protocol/contract, а не database API.

---

# 46. Ключевой вопрос исследования

В процессе работы необходимо постоянно проверять архитектуру одним вопросом:

> **Можно ли на основании одного и того же immutable event/frame stream восстановить как trajectory выполнения агента, так и эволюцию коллективного знания?**

Если ответ отрицательный — найти причину и изменить модель.

---

# 47. Финальный результат

В конце исследования должен появиться не просто список технологий.

Нужен конкретный ответ:

```text
Вот из каких компонентов мы строим harness.
Вот почему.
Вот что пишем сами.
Вот что берём OSS.
Вот где проходит граница Agent Kernel.
Вот где проходит граница Temporal.
Вот где проходит граница Temporality.
Вот event contract.
Вот knowledge model.
Вот storage model.
Вот execution model.
Вот failure semantics.
Вот работающий PoC.
```

Главная цель:

> Получить минимальный, но промышленно расширяемый Agent Harness, в котором **durable execution, agent runtime и temporal knowledge observability спроектированы как единая система**, а Temporality является её каноническим семантическим контрактом.
