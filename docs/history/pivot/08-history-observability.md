# Temporality — полный pivot проекта

## 1. Контекст

Temporality изначально задумывался как реализация Frame Runtime Protocol (FRP) — протокола и инфраструктуры для наблюдения за исполнением агентных систем во времени.

В ходе развития концепции стало понятно, что простой tracing/observability подход недостаточен.

Современные agentic systems образуют не только цепочки вызовов:

```text
agent → LLM → tool → result → LLM → tool → ...
```

Они формируют и изменяют **коллективное знание**:

* агенты находят новую информацию;
* формируют утверждения и гипотезы;
* другие агенты их используют;
* знания подтверждаются или оспариваются;
* появляются новые зависимости;
* старые знания устаревают;
* ошибочные утверждения аннулируются;
* знания переходят между задачами и агентами;
* отдельные агенты становятся источниками определённых областей знаний;
* ошибки и исправления распространяются по команде.

Поэтому новый Temporality должен быть не очередным tracing UI и не очередным графом вызовов.

## 2. Новая концепция

### Temporality

> **Temporality — runtime observability system for the evolution of agentic knowledge and collective work over time.**

Система должна позволять увидеть:

> **как команда агентов приобретает, распространяет, использует, проверяет, исправляет и теряет знания во времени.**

Главный объект наблюдения — не отдельный LLM call, а **эволюция runtime + knowledge graph во времени**.

---

# 3. Главный продуктовый вопрос

Temporality должен отвечать на вопросы:

### Про знания

* Что команда знает сейчас?
* Что она знала в прошлом?
* Какие знания появились недавно?
* Какие знания стали устойчивыми?
* Какие знания перестали использоваться?
* Какие знания были признаны ошибочными?
* Какие знания были заменены другими?
* Какие знания активно переиспользуются?

### Про происхождение

* Кто первым создал знание?
* На основании какого evidence?
* Какой агент его обнаружил?
* Кто его подтвердил?
* Кто его оспорил?
* Кто его исправил?
* Какие документы, tool calls, commits, тесты или другие события лежат в основании?

### Про распространение

* Какие агенты используют конкретное знание?
* Сколько раз оно переиспользовалось?
* Как оно распространилось между агентами?
* Какие знания переходят из одной задачи в другую?
* Есть ли knowledge silos?
* Какие агенты являются основными knowledge producers?
* Какие агенты в основном потребляют знания?

### Про качество

* Какие знания часто оспариваются?
* Какие знания часто приводят к ошибкам?
* Какие знания были позднее аннулированы?
* Сколько времени знание существовало до обнаружения ошибки?
* Какие знания имеют большое downstream impact?
* Какие области системы создают больше всего corrections?

### Про прогресс

* Как меняется коллективное знание команды?
* Какие области проекта становятся более зрелыми?
* Где команда действительно накапливает знания?
* Где она постоянно ходит по кругу?
* Где знания исчезают быстрее, чем создаются?
* Где появляются устойчивые knowledge foundations?

---

# 4. Главный pivot

Не строить:

```text
LLM trace viewer
```

Не строить:

```text
обычный knowledge graph
```

Не строить:

```text
миллионы точек с labels
```

Не строить:

```text
ещё один Langfuse
```

Строить:

```text
Temporal Knowledge Observability
```

где knowledge graph является **временной проекцией event/frame stream**.

---

# 5. Архитектурный принцип

## Event/frame stream — source of truth

Необходимо сохранить идею FRP.

Система должна хранить поток наблюдаемых событий/frames:

```text
Frame 1
Frame 2
Frame 3
...
Frame N
```

Каждый frame/event должен позволять восстановить:

* actor;
* timestamp;
* execution/context;
* operation;
* affected knowledge;
* evidence;
* provenance;
* relationships;
* resulting state.

Текущий knowledge graph должен быть **derived projection**.

То есть:

```text
                  Temporal Event Stream
                          │
              ┌───────────┼───────────┐
              │           │           │
              ▼           ▼           ▼
        Knowledge      Agent       Project
          Graph       Activity     Progress
              │           │           │
              └───────────┼───────────┘
                          │
                     UI projections
```

Это принципиально.

Нельзя делать текущий граф единственным источником истины.

---

# 6. Основные сущности

Минимальная модель должна позволять представить следующие сущности.

## Actor

Кто произвёл действие:

* agent;
* human;
* system;
* external service.

Пример:

```json
{
  "actor": "qa-01",
  "type": "agent"
}
```

---

## Knowledge Node

Единица коллективного знания.

Это может быть:

* fact;
* claim;
* hypothesis;
* decision;
* constraint;
* discovered behavior;
* architectural rule;
* project knowledge;
* domain concept.

Пример:

```text
"Synapse requires version 1.155 for certification"
```

Knowledge Node должен иметь lifecycle.

---

## Knowledge Event

Изменение состояния знания.

Минимальный набор операций:

```text
discover
create
infer
assert
adopt
reuse
confirm
challenge
correct
supersede
invalidate
forget
expire
```

Не обязательно ограничиваться этим списком, но semantics должны быть явными.

---

## Evidence

Источник знания.

Например:

* document;
* URL;
* tool result;
* repository file;
* commit;
* pull request;
* test;
* execution;
* another knowledge node;
* human statement.

Evidence должен быть связан с provenance.

---

## Relationship

Связь между knowledge nodes:

```text
supports
contradicts
derived_from
depends_on
supersedes
related_to
used_by
```

---

## Frame

Frame описывает наблюдаемое состояние runtime в определённый момент.

Пример:

```json
{
  "timestamp": "...",
  "actor": "analyst-02",
  "operation": "discover",
  "knowledge": ["claim-812"],
  "evidence": ["document-37"],
  "context": {
    "project": "project-a",
    "task": "task-91"
  }
}
```

Frame не обязан содержать весь graph.

Он фиксирует **изменение / наблюдение**, из которого graph может быть восстановлен.

---

# 7. Knowledge lifecycle

Temporality должен явно моделировать жизненный цикл знания.

Пример:

```text
             ┌───────────┐
             │ DISCOVERED│
             └─────┬─────┘
                   │
                   ▼
             ┌───────────┐
             │ PROPOSED  │
             └─────┬─────┘
                   │
             ┌─────┴─────┐
             ▼           ▼
         CONFIRMED    CHALLENGED
             │           │
             │           ▼
             │        CORRECTED
             │           │
             └─────┬─────┘
                   ▼
                ACTIVE
                   │
          ┌────────┼────────┐
          ▼        ▼        ▼
       REUSED    AGING   SUPERSEDED
                            │
                            ▼
                       INVALIDATED
```

Это не обязательно реализовывать именно как конечный автомат.

Важно обеспечить семантику переходов.

---

# 8. Главный UX

## 8.1 Temporal Knowledge Cloud

Основной экран — не обычный graph visualization.

Он должен совмещать:

* timeline;
* knowledge cloud;
* semantic clustering;
* activity density;
* provenance;
* agent activity.

Концептуально:

```text
                         TIME →

 Authentication    ████████████████████████
                   █████████████████████████
                   ███████████

 API                ███████████████████
                    █████████████████████

 Deployment                    ███████████████
                               █████████████████

 Testing                              ████████████
                                     █████████████
```

Каждая область/облако представляет группу связанных знаний.

---

# 9. Knowledge Cloud

Визуальная плотность должна отражать не просто количество nodes.

Необходимо исследовать комбинированную семантику:

### Размер

Количество / влияние знаний.

### Плотность

Интенсивность активности и reuse.

### Время существования

Возраст знания.

### Lifetime

Период между созданием и последним meaningful use.

### Activity

Частота обращений к знанию.

### Stability

Количество подтверждений относительно challenge/correction.

### Influence

Количество downstream effects.

### Freshness

Насколько недавно знание использовалось/подтверждалось.

### Health

Комбинация validation, contradiction, invalidation и других сигналов.

Конкретную визуальную кодировку выбрать в процессе UX prototyping.

---

# 10. Timeline

Пользователь должен иметь возможность:

* двигаться по времени;
* zoom in/out;
* выбирать период;
* смотреть diff между двумя моментами;
* фиксировать temporal snapshot;
* видеть появление новых знаний;
* видеть исчезновение/затухание;
* видеть corrections;
* видеть invalidations.

Ключевой сценарий:

> «Покажи состояние коллективного знания на 10 сентября 14:00».

И:

> «Покажи, что изменилось между 10 сентября 14:00 и 12 сентября 18:00».

---

# 11. Knowledge birth / death visualization

Должно быть видно:

```text
          NEW
           ↓
        ┌──────┐
        │claim │
        └──┬───┘
           │
      ┌────┴─────┐
      │          │
    reuse     validate
      │          │
      └────┬─────┘
           │
         active
           │
       ┌───┴────┐
       │        │
     decay   supersede
                │
             invalid
```

Пользователь должен визуально видеть **жизнь знания**, а не только его текущее состояние.

---

# 12. Provenance view

При выборе knowledge node показывать его lineage.

Например:

```text
Claim #812
│
├── discovered
│   └── Analyst-02
│       └── Document #37
│
├── adopted
│   └── Lead-01
│
├── reused
│   ├── Coder-03
│   ├── Coder-04
│   └── Reviewer-02
│
├── challenged
│   └── QA-01
│       └── Test #221
│
├── corrected
│   └── Analyst-02
│
└── current state
    └── confirmed
```

Должна быть возможность перейти из provenance в исходные события.

---

# 13. Agent view

Отдельный режим:

```text
Agent activity
```

Для каждого агента показывать:

* knowledge created;
* knowledge reused;
* knowledge validated;
* knowledge challenged;
* knowledge invalidated;
* knowledge transferred;
* areas of expertise;
* downstream influence.

Важно:

**не превращать это в рейтинг агентов.**

Цель — понять структуру коллективной работы, а не выставлять агентам оценки.

---

# 14. Knowledge flow

Нужен отдельный вид, показывающий распространение знания:

```text
Analyst-01
    │
    ▼
Lead-01
   ├──────────► Coder-01
   │
   ├──────────► Coder-02
   │
   └──────────► QA-01
                     │
                     ▼
                  correction
                     │
                     ▼
                 Lead-01
```

Это должно позволять видеть:

* hubs;
* silos;
* bottlenecks;
* dead ends;
* распространение corrections;
* cross-agent knowledge reuse.

---

# 15. Project progress через knowledge

Добавить projection:

```text
Project
   │
   ├── architecture
   ├── implementation
   ├── testing
   ├── deployment
   └── operations
```

Для каждой области показывать:

* amount of active knowledge;
* knowledge growth;
* reuse;
* validation;
* corrections;
* unresolved contradictions;
* decay.

Главная идея:

> прогресс проекта оценивается не только количеством завершённых задач, но и тем, как меняется коллективное knowledge state.

Это аналитическая projection, а не единственный KPI.

---

# 16. Problem hotspots

Temporality должен автоматически выявлять области, где:

* одно и то же знание многократно исправляется;
* знания часто оспариваются;
* invalidated knowledge продолжает использоваться;
* большое количество решений строится на нестабильном knowledge;
* knowledge rapidly decays;
* появляется много conflicting claims;
* агенты повторно исследуют один и тот же вопрос.

Например:

```text
⚠ Authentication

43 active claims
17 challenges
8 corrections
3 invalidations
92 reuses
4 agents affected
```

При переходе пользователь получает provenance.

---

# 17. Reuse graph

Очень важный аналитический слой.

Для каждого knowledge node:

```text
created:       1
confirmed:     3
reused:       47
agents:        8
tasks:        19
projects:      4
corrections:   2
```

Особенно интересен **cross-task reuse**.

Это показывает, превращается ли локальное знание в коллективное.

---

# 18. Knowledge decay

Нужно экспериментально определить модель decay.

Например:

```text
last_use
last_validation
age
usage_frequency
domain
```

Визуально:

```text
██████████████ active

████████░░░░░░ aging

███░░░░░░░░░░ stale

░░░░░░░░░░░░░ invalid
```

Важно не вводить искусственный "truth score".

Decay должен быть наблюдаемым свойством поведения знания.

---

# 19. Temporal queries

Нужен query layer.

Минимальные запросы:

```text
What did the team know at T?

What changed between T1 and T2?

Where did this knowledge come from?

Who first introduced this claim?

Who reused it?

How many times was it reused?

Who challenged it?

What knowledge was invalidated later?

Which knowledge crossed task boundaries?

Which agents share knowledge?

Which knowledge became stale?

What changed after event X?
```

Желательно иметь и API, и UI.

---

# 20. Replay / time travel

Должна быть возможность:

1. выбрать момент времени;
2. восстановить соответствующий knowledge projection;
3. проиграть изменения;
4. наблюдать propagation.

Например:

```text
10:00 ─────── 11:00 ─────── 12:00 ─────── 13:00
   │             │             │             │
 claim A       reuse A       challenge A   correction A
                               │             │
 claim B                     claim C       reuse C
```

Это один из ключевых differentiators Temporality.

---

# 21. Agent runtime и knowledge должны быть связаны

Нельзя полностью отделять:

```text
runtime tracing
```

от:

```text
knowledge graph
```

Например:

```text
Agent
  │
  └── Frame
       │
       ├── LLM decision
       ├── tool call
       ├── observation
       ├── knowledge created
       ├── knowledge reused
       └── knowledge changed
```

Это позволяет перейти:

```text
Knowledge
   ↓
Event
   ↓
Frame
   ↓
Agent execution
   ↓
Tool / evidence
```

и обратно.

---

# 22. Evidence-first architecture

Особое внимание уделить provenance.

Нельзя допускать ситуацию:

```text
knowledge = "X is true"
```

без ответа на вопрос:

```text
Why do we think X is true?
```

Для knowledge event желательно хранить:

```text
source
evidence
actor
timestamp
context
causal parents
```

Это необходимо для:

* debugging;
* auditing;
* evaluation;
* replay;
* trust;
* invalidation propagation.

---

# 23. Связь с guardrails

Первоначальная идея Temporality включала возможность использовать исторические frames/context как материал для runtime guidance.

Эта возможность сохраняется, но перестаёт быть главным продуктом.

Теперь:

```text
Temporal Memory
      │
      ├── Observability
      │
      ├── Visualization
      │
      ├── Analytics
      │
      ├── Replay
      │
      └── Runtime Guidance
```

В будущем knowledge patterns могут использоваться агентом как:

* contextual hints;
* known failure patterns;
* reusable evidence;
* warnings;
* learned procedures;
* guardrail-like constraints.

Но **runtime guidance является consumer Temporality, а не его основным назначением**.

---

# 24. Интеграция с GoClaw

GoClaw должен стать одним из первых reference runtimes.

Интегрировать:

* lead;
* coder;
* reviewer;
* QA;
* delegation;
* MCP calls;
* skills;
* workstations;
* sandbox;
* memory;
* cron;
* webhooks;
* Matrix;
* Telegram.

Особенно важно протрассировать delegation:

```text
Lead
 └── DelegatedRun
      └── Coder
           ├── MCP
           ├── workstation
           ├── sandbox
           └── QA
```

и knowledge propagation через эту структуру.

---

# 25. Не делать зависимость от GoClaw

Temporality должен иметь независимую event protocol model.

GoClaw — adapter/reference implementation.

В перспективе должны быть adapters для:

* GoClaw;
* LangGraph;
* OpenHands;
* другие agent runtimes.

Цель:

```text
Runtime
   │
   ▼
Temporality protocol
   │
   ▼
Temporal Knowledge Store
   │
   ├── UI
   ├── analytics
   ├── replay
   └── runtime guidance
```

---

# 26. Отношение к Langfuse

Langfuse не считать прямым конкурентом.

Возможны два режима:

```text
GoClaw
  │
  ├── OpenTelemetry / Langfuse
  │
  └── Temporality
```

или:

```text
GoClaw
  │
  ▼
Temporality
  │
  ├── runtime observability
  ├── knowledge evolution
  └── export → Langfuse
```

Temporality должен решать задачу, которую generic LLM tracing не решает:

> **эволюция коллективного знания и его provenance во времени.**

---

# 27. MVP

Первый milestone не должен пытаться решить всё.

## MVP-1

Реализовать:

### Data

* event/frame schema;
* actor;
* knowledge node;
* knowledge event;
* evidence;
* provenance;
* timestamp;
* relationships.

### Storage

Любое разумное хранилище, позволяющее:

* temporal queries;
* graph queries;
* event retrieval;
* reconstruction of projection.

Не делать premature distributed architecture.

### GoClaw adapter

Минимально:

```text
agent
LLM call
tool call
tool result
knowledge create
knowledge reuse
knowledge challenge
knowledge confirm
knowledge invalidate
delegation
```

### UI

Один основной экран:

```text
Temporal Knowledge Cloud
+
Timeline
+
Knowledge detail
+
Provenance
```

---

# 28. MVP UI — обязательный сценарий

Должна быть возможность:

1. открыть проект;
2. увидеть timeline;
3. увидеть semantic knowledge clusters;
4. увидеть появление новых знаний;
5. выбрать knowledge cluster;
6. увидеть individual knowledge nodes;
7. выбрать node;
8. увидеть lifecycle;
9. увидеть кто создал;
10. увидеть кто использовал;
11. увидеть evidence;
12. увидеть challenges/corrections;
13. перейти к исходному execution;
14. изменить временной диапазон;
15. увидеть, как knowledge state изменился.

Если эти сценарии не работают — MVP нельзя считать готовым.

---

# 29. Что НЕ нужно делать

## Не делать generic APM

Temporality не должен становиться:

```text
Datadog for agents
```

## Не делать очередной Langfuse

Не копировать:

* prompt management;
* generic LLM analytics;
* generic cost dashboards;
* generic experiment management.

Если это нужно — интегрироваться.

## Не делать обычный knowledge graph viewer

Не делать:

```text
nodes + edges + force layout
```

основным UX.

Graph view может существовать как drill-down.

## Не делать artificial scoring

Не вводить:

```text
Knowledge score = 87
Agent intelligence = 92
```

без строгой семантики.

Цель — показывать наблюдаемую динамику.

## Не делать opaque AI summaries

AI-generated summary может существовать, но каждое утверждение должно иметь provenance.

---

# 30. Ключевая продуктовая метафора

Temporality должен восприниматься как:

> **flight recorder + temporal knowledge map + team progress observability**

а не как:

> trace viewer.

Можно использовать метафору:

```text
                   TEAM KNOWLEDGE

        birth → adoption → reuse → validation
          ↑                         ↓
      discovery                  correction
          ↑                         ↓
        agent ←────── time ─────→ agent
```

---

# 31. Долгосрочная архитектура

Целевая модель:

```text
                         TEMPORALITY
                              │
                    Frame Runtime Protocol
                              │
                     Temporal Event Store
                              │
             ┌────────────────┼────────────────┐
             │                │                │
             ▼                ▼                ▼
        Agent Graph      Knowledge Graph    Project Graph
             │                │                │
             └────────────────┼────────────────┘
                              │
                       Temporal Engine
                              │
              ┌───────────────┼────────────────┐
              │               │                │
              ▼               ▼                ▼
          Timeline       Knowledge Cloud    Provenance
              │               │                │
              └───────────────┼────────────────┘
                              │
                       User / Analyst
                              │
                 ┌────────────┼────────────┐
                 │            │            │
              Explore       Replay      Explain
```

---

# 32. Главный критерий успеха

После внедрения Temporality пользователь должен иметь возможность открыть работающую agentic team и **за несколько минут понять**:

> Что команда узнала?

> Что она уже закрепила?

> Что сейчас активно используется?

> Что оказалось ошибочным?

> Где происходят постоянные corrections?

> Кто создаёт и распространяет знания?

> Какие знания переходят между задачами?

> Где команда реально продвигается?

> Где она просто повторяет уже сделанную работу?

> Как изменилось состояние коллективного знания за выбранный период?

Если пользователь видит только список traces — проект не достиг цели.

Если пользователь видит красивый graph, но не понимает динамику — проект не достиг цели.

Если пользователь может **провести взглядом историю развития коллективного знания во времени и провалиться от облака до конкретного agent execution и evidence** — pivot удался.

---

# 33. Первые deliverables

1. **Новая архитектурная концепция Temporality**

   * revised domain model;
   * event/frame model;
   * temporal projection model.

2. **Protocol specification**

   * schema;
   * semantics;
   * lifecycle events;
   * provenance;
   * temporal queries.

3. **GoClaw adapter**

   * минимальный production-quality integration.

4. **Temporal Knowledge Store**

   * event ingestion;
   * projection;
   * temporal reconstruction.

5. **UX prototype**

   * Temporal Knowledge Cloud;
   * timeline;
   * drill-down;
   * provenance;
   * replay.

6. **Working MVP**

   * end-to-end GoClaw → Temporality → UI.

7. **Architecture decision record**

   * почему выбранная модель хранения;
   * почему graph является projection;
   * как обеспечивается temporal consistency;
   * как обеспечивается provenance.

---

# 34. Финальная формулировка проекта

Старый Temporality:

> Frame Runtime Protocol for observing agent execution.

Новый Temporality:

> **A temporal runtime protocol and observability system for understanding how agentic teams create, propagate, validate, reuse, correct, and retire knowledge over time.**

Ключевой объект:

> **не trace, не graph и не LLM call — а эволюция коллективного знания во времени.**

Ключевой UX:

> **Knowledge Cloud × Timeline × Provenance.**

Ключевая техническая идея:

> **event/frame stream → temporal projections → knowledge graph / agent graph / project graph.**

Ключевой пользовательский результат:

> **видеть прогресс команды через эволюцию её коллективного знания.**
