# Temporality --- продуктовый roadmap

Дата: 2026-09-26

## 0. Главный вывод

Temporality движется в правильную сторону, но текущий план слишком
сильно оптимизирован под завершение Phase 2 и доказательство
корректности внутренней инфраструктуры.

Главный продуктовый сдвиг:

> Temporality должен показывать, как опыт и знания команды/агентов
> возникают, изменяются, используются и умирают во времени --- и
> позволять доказательно восстановить этот процесс.

Это уже не просто event journal, debugger или Agent SRE tooling.

При этом обнаружена важная практическая проблема: **продукт пока трудно
дать живому человеку**. Даже если Timeline, forensic, replay и knowledge
lifecycle работают, пользователю негде нормально: - создать и запустить
задачу; - определить system prompt; - подключить skills; - настроить
sandbox; - подключить MCP-серверы; - выбрать модель; - посмотреть
результат и trajectory; - повторить запуск.

Поэтому следующий этап должен решать две задачи одновременно:

1.  довести Temporality до полезного инструмента исследования agent
    experience;
2.  собрать минимальную agent-execution обвязку, достаточную для
    реального использования небольшой командой.

Не следует превращать Temporality в новый универсальный agent runtime.
Нужна **тонкая рабочая оболочка**, позволяющая запустить реальный
агентский опыт и затем исследовать его в Temporality.

------------------------------------------------------------------------

# 1. Продуктовая модель

Целевой замкнутый цикл:

``` text
                    ┌──────────────────────┐
                    │  Task / Agent setup  │
                    │ prompt / skills /    │
                    │ MCP / sandbox / model│
                    └──────────┬───────────┘
                               │
                               ▼
                         real agent run
                               │
                               ▼
                    ┌──────────────────────┐
                    │    Event stream      │
                    │   source of truth    │
                    └──────────┬───────────┘
                               │
              ┌────────────────┼────────────────┐
              ▼                ▼                ▼
          execution         knowledge       operations
          projection        projection       projection
              │                │                │
              └────────────────┼────────────────┘
                               ▼
                    ┌──────────────────────┐
                    │  Experience UI      │
                    │ timeline / replay /  │
                    │ activation / diff    │
                    └──────────────────────┘
```

Ключевой принцип:

> **Execution создаёт experience, event stream фиксирует его,
> Temporality объясняет его.**

Execution-обвязка нужна не ради отдельного runtime-продукта, а чтобы
Temporality мог использоваться на реальной работе.

------------------------------------------------------------------------

# 2. Что считать ядром Temporality

Центральная сущность --- не event и не operation.

Центральная продуктовая ценность:

``` text
Experience / Knowledge evolution
```

Система должна позволять отвечать на вопросы:

### Почему агент это сделал?

``` text
decision
  ↓
retrieved knowledge
  ↓
knowledge state at T
  ↓
source / lineage / evidence
```

### Почему агент перестал это делать?

``` text
knowledge
  ↓
last activation
  ↓
invalidation / supersession
  ↓
new knowledge
```

### Почему два запуска дали разные решения?

``` text
Run A
  ↓
knowledge state A

Run B
  ↓
knowledge state B

       ↓

      diff
```

### Что команда уже знает?

``` text
scope
  ↓
knowledge set
  ↓
lifecycle
  ↓
provenance
  ↓
recent activation
```

------------------------------------------------------------------------

# 3. Архитектурный принцип

Сохраняем:

> **Event stream --- единственный source of truth.**

Все проекции строятся поверх immutable history:

``` text
              immutable event stream
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
      knowledge     timeline     operations
      projection    projection    projection
          │            │
          └──────┬─────┘
                 ▼
             debugger
```

Не добавлять отдельные persistent stores для: - knowledge graph; -
episode store; - evidence store; - observation store; - experience
graph.

`state-at-T`, activation chain, replay и diff должны по возможности
вычисляться как проекции над существующими событиями.

------------------------------------------------------------------------

# 4. Главный критерий продукта

Следующий этап следует оценивать не количеством закрытых Phase 2
пунктов, а следующим сценарием:

> Может ли инженер взять реальный agent run, открыть Temporality и за
> 2--5 минут понять, что агент знал, почему он это знал, что он
> использовал, что из этого оказалось ошибочным и как коллективный опыт
> изменился после этого?

Если ответ:

> «Да, это видно в интерфейсе»

--- Temporality становится продуктом.

Если ответ:

> «Технически все события есть, это можно реконструировать через API»

--- мы продолжаем строить инфраструктуру вместо продукта.

------------------------------------------------------------------------

# 5. Новый P0 --- Minimal Agent Workspace

## 5.1. Зачем

До сих пор roadmap предполагает, что реальные agent runs уже откуда-то
появляются.

Для живого пользователя это не так.

Чтобы позвать первую команду, нужен минимальный рабочий контур:

``` text
Project
  ├── Agents
  │    ├── system prompt
  │    ├── model
  │    └── skills
  │
  ├── MCP servers
  │
  ├── Sandbox profile
  │
  └── Tasks / Runs
```

Это не означает создание нового полноценного runtime.

Нужно собрать минимальную оболочку над существующим execution stack.

## 5.2. Минимальный setup

Пользователь должен иметь возможность:

1.  создать project;
2.  создать agent/profile;
3.  задать system prompt;
4.  выбрать модель/provider;
5.  подключить skills;
6.  подключить MCP servers;
7.  выбрать sandbox profile;
8.  создать task;
9.  запустить run;
10. увидеть run в Temporality;
11. открыть Timeline / forensic view;
12. повторить run с изменённой конфигурацией.

## 5.3. Что пока НЕ нужно

Не строить: - универсальный workflow designer; - полноценный IDE; -
сложный agent orchestration engine; - marketplace skills; - сложный
secret-management продукт; - собственный scheduling subsystem; - новый
sandbox runtime; - новый model gateway.

Если существующий GoClaw/Temporal/adapter способен предоставить
execution, Temporality должен максимально использовать его.

------------------------------------------------------------------------

# 6. P0 --- Operations UI

Оставить как есть.

Uncertain operation --- важная часть продуктовой модели:

``` text
occurred
none
unknown
```

Система не должна делать вид, что знает эффект операции, если
достоверного наблюдения нет.

------------------------------------------------------------------------

# 7. P0 --- Execution robustness

## 7.1. Duplicate mitigation

Сделать только необходимый минимум:

-   `parallel_tool_calls: false`;
-   exact duplicate suppression;
-   near-duplicate mitigation настолько, насколько требуется для
    стабильного запуска acceptance-сценариев;
-   malformed-argv salvage, если корректные соседние tool calls можно
    сохранить.

Не превращать Temporality в универсальный policy engine против
патологического поведения агентов.

## 7.2. Live MCP E2E

Поднять до P0/P1 boundary.

Нужно доказать полный реальный цикл:

``` text
real agent
   ↓
approval
   ↓
MCP tool
   ↓
real effect
   ↓
observation
   ↓
knowledge
   ↓
future run
```

Acceptance:

-   live AgentRun с MCP;
-   approval flow;
-   `mcp.call.started/completed`;
-   `server` identity;
-   `result_ref`;
-   idempotency key;
-   повтор операции не создаёт дубликат эффекта.

Это важнее большинства внутренних Phase 2 polish-задач, потому что
соединяет execution provenance и experience evolution.

------------------------------------------------------------------------

# 8. P0 --- Runbook

Оставить.

Нужны: - backup/restore PostgreSQL; - migration/upgrade procedure; -
token rotation; - health monitoring; - outbox monitoring; - quota
monitoring; - типовые аварии.

Backup/restore должен быть реально прогнан.

------------------------------------------------------------------------

# 9. P1 --- State-at-T / Replay / Diff

Поднять в приоритетах.

Это не просто backend convenience.

Ключевой вопрос Temporality:

> Что система знала в момент, когда было принято это решение?

Нужны:

``` text
GET /v1/knowledge/state?project=&at=
```

и diff между двумя временными точками/runs.

Acceptance:

-   state-at-T воспроизводит lifecycle exp4;
-   diff показывает изменение knowledge/experience;
-   нет нового persistent storage;
-   reconstruction идёт из event stream.

------------------------------------------------------------------------

# 10. P1 --- Experience Timeline

Это центральная продуктовая часть.

## 10.1. Activation chain

Сделать первым классом:

``` text
memory recalled
      ↓
memory injected
      ↓
decision
      ↓
action
      ↓
outcome
```

По клику на knowledge пользователь должен видеть: - где knowledge
возникло; - где было recalled; - где injected; - какое решение
последовало; - какие действия были совершены; - какой outcome получен; -
где knowledge было validated; - где invalidated; - чем superseded.

Отдельно различать: - reused/validated; - failed; - invalidated; -
superseded; - resurrected.

## 10.2. Semantic zoom

Целевые уровни:

``` text
Knowledge / Experience
        ↓
Episode
        ↓
Event
```

Episode может представлять: - turn; - tool call; - observation; -
formation; - validation; - invalidation; - reuse.

Цель --- перейти от «просмотра event log» к пониманию эпизодов опыта.

## 10.3. URL state

Сохранять в URL: - lens; - filters; - zoom; - selected entity.

Investigation должна быть shareable ссылкой.

------------------------------------------------------------------------

# 11. P1 --- Experience Investigation

Добавить отдельный продуктовый фронт.

Temporality должен не только показывать timeline, но и позволять
исследовать типовые вопросы:

### Why this decision?

Путь от decision к knowledge state и evidence.

### Why did this knowledge disappear?

Путь через invalidation/supersession.

### Why are these runs different?

Сравнение knowledge state и trajectory.

### What does the team know?

Knowledge set в заданном project/scope с lifecycle и provenance.

Это можно реализовывать поверх существующих projections; не требуется
новая модель хранения.

------------------------------------------------------------------------

# 12. P1 --- Minimal user loop

После появления Minimal Agent Workspace должен работать следующий
сценарий:

``` text
1. Создать project

2. Настроить agent:
   system prompt
   model
   skills
   MCP
   sandbox

3. Создать task

4. Run

5. Получить результат

6. Открыть Temporality

7. Посмотреть:
   trajectory
   timeline
   tool calls
   knowledge
   operations

8. Исследовать:
   "почему агент так сделал?"

9. Изменить prompt / skill / MCP / knowledge

10. Повторить run

11. Сравнить два опыта
```

**Это и есть первый реальный product loop.**

Если его нет, дальнейшая работа над глубиной forensic UX будет
происходить на искусственных acceptance fixtures.

------------------------------------------------------------------------

# 13. P1/P2 --- Experiment 9

Experiment 9 нужен после activation chain и semantic zoom.

Но его задача должна быть не только технической.

Exp9 должен стать acceptance corpus для проверки:

> Может ли человек понять эволюцию опыта через UI без знания внутренней
> реализации Temporality?

Корпус: - competing hypotheses; - repeated confirmation; -
contradiction; - resurrection; - stale knowledge; - supersession; -
разные scopes.

Вопросы acceptance:

-   какая гипотеза появилась первой;
-   какая была подтверждена;
-   какая умерла;
-   какая воскресла;
-   почему агент использовал stale knowledge;
-   какое evidence вызвало contradiction;
-   что сейчас считается valid;
-   какие знания реально использовались.

------------------------------------------------------------------------

# 14. P1 --- Trajectory-derived experience

## 14.1. Trajectory extraction

- [ ] Выделять из завершённого run повторяющиеся tool-call sequences, шаги, решения и исходы.
- [ ] Строить extraction только из event stream; не вводить отдельное persistent storage.
- [ ] Для каждого extracted pattern сохранять provenance до исходных events.
- [ ] Разделить deterministic extraction и LLM-derived extraction.

## 14.2. Experience patterns

- [ ] Агрегировать похожие memories/events в experience patterns.
- [ ] При агрегации учитывать relevance, recency, validation, outcome, recurrence, contradiction и supersession.
- [ ] Показывать evidence и исходные memories по каждому pattern.
- [ ] Проверить сценарий: сотни кандидатов → десятки patterns без потери provenance.

## 14.3. Experience Priming

- [ ] Сделать priming опциональным этапом перед AgentRun.
- [ ] Pipeline: candidates → patterns → ranking → 3–7 cues.
- [ ] Ограничить priming жёстким token budget.
- [ ] Не передавать в prompt сырые сотни memories.
- [ ] Дать агенту JIT-доступ к деталям через существующие tools/memory.
- [ ] Сравнить baseline / conventional RAG / priming / priming+JIT.
- [ ] Измерять task success, trajectory length, token usage, unnecessary retrieval/tool calls, wrong-memory activation, contradiction rate и latency.
- [ ] Не делать priming обязательным до подтверждения улучшения относительно baseline.

## 14.4. Trajectory comparison

- [ ] Добавить controlled rerun из одной task/configuration.
- [ ] Поддержать минимум два варианта: no priming и priming.
- [ ] Сравнивать trajectories на уровне steps, tool calls, cost и outcome.
- [ ] Показывать, какие различия возникли из-за configuration/priming.

## 14.5. Trajectory-to-artifact extraction

- [ ] Ввести общий extraction interface для артефактов, получаемых из trajectory.
- [ ] Реализовать два типа: experience pattern и повторяемый workflow fragment.
- [ ] Для каждого артефакта сохранять provenance до trajectory/events.
- [ ] Не реализовывать автоматическую замену agent loop на workflow на этом этапе.

------------------------------------------------------------------------

# 14. P1 --- Sandbox matrix

Оставить, но не делать её центральным продуктовым фронтом.

Минимальная автоматизированная security matrix:

  Сценарий                 Ожидание
  ------------------------ ------------------------------------
  `../` workspace escape   запрещён
  network egress           запрещён согласно profile
  credentials              недоступны
  docker socket            недоступен
  memory/cpu/pids          ограничены
  timeout                  процесс гарантированно завершается
  output                   bounded
  `/scratch`               изолирован
  reviewer/QA              read-only ограничения соблюдаются

Результат: - автоматические тесты; - expected-vs-actual matrix; - явно
зафиксированные принятые риски.

Не превращать это в отдельную sandbox platform.

------------------------------------------------------------------------

# 15. P2 --- Read-after-write probes

Оставить.

Автоматическая проверка:

``` text
operation
   ↓
observable trace
   ↓
probe
   ↓
hint to operator
```

Важно:

> probe даёт подсказку, но не автоматически выносит authoritative
> verdict.

------------------------------------------------------------------------

# 16. P2 --- Cost accounting

Оставить после того, как появится реальное использование.

Нужно: - model price table; - cost per model call; - cost per run; -
cost per project; - aggregation; - потенциально token quotas.

До появления пользователей cost accounting --- вторичен.

------------------------------------------------------------------------

# 17. Что сознательно НЕ делать

Не строить:

-   универсальный knowledge graph;
-   embeddings infrastructure;
-   graph DB;
-   automatic clustering как источник визуальных групп;
-   новый agent runtime;
-   generic event viewer / второй debugger;
-   отдельный scheduler;
-   сложный model gateway до появления реальной потребности;
-   сложную tenant model до появления нескольких организаций;
-   полноценную sandbox platform.

Критерий для возврата к этим направлениям:

> существует конкретный реальный сценарий использования, который
> невозможно удовлетворить текущей архитектурой.

------------------------------------------------------------------------

# 18. Отложенные инфраструктурные темы

  -----------------------------------------------------------------------
  Тема                                Возврат при условии
  ----------------------------------- -----------------------------------
  Tenants                             несколько org-структур

  Helm / не-compose                   выход за один host

  Behavioral SLO                      реальные abuse-сценарии

  Evidence package                    внешний audit/consumer

  Model gateway                       второй provider / fallback /
                                      routing

  Embeddings / graph DB               доказанная необходимость

  сложная sandbox orchestration       реальные требования нескольких
                                      типов workloads
  -----------------------------------------------------------------------

------------------------------------------------------------------------

# 19. Новый порядок исполнения

Предлагаемый порядок:

``` text
P0 — Make it usable
│
├── Minimal Agent Workspace
│   ├── project
│   ├── agent/profile
│   ├── system prompt
│   ├── model
│   ├── skills
│   ├── MCP
│   ├── sandbox profile
│   └── task/run
│
├── Operations UI
├── minimal duplicate mitigation
├── Live MCP E2E
└── runbook
        │
        ▼
P1 — Make experience understandable
│
├── state-at-T
├── semantic replay
├── diff
├── activation chain
├── semantic zoom
├── URL/shareable investigations
└── investigation flows
        │
        ▼
P1/P2 — Prove product value with real use
│
├── small real team usage
├── Experiment 9
├── read-after-write probes
└── feedback-driven iteration
        │
        ▼
P2 — Economics / scale
│
└── cost accounting
```

------------------------------------------------------------------------

# 20. Что должно быть готово перед приглашением первых пользователей

Не нужен production-complete продукт.

Нужен **Minimum Usable Experience**:

### Setup

-   project;
-   agent;
-   system prompt;
-   model;
-   skill;
-   MCP;
-   sandbox.

### Execution

-   task;
-   run;
-   streaming/status;
-   tool execution;
-   approval;
-   результат.

### Observation

-   run timeline;
-   tool calls;
-   model calls;
-   MCP provenance;
-   operations;
-   uncertain operations.

### Experience

-   knowledge;
-   activation chain;
-   state-at-T;
-   replay;
-   diff.

### Iteration

-   изменить prompt/skill;
-   повторить run;
-   сравнить runs.

Если это есть, можно приглашать небольшую команду и перестать полагаться
только на fixtures.

------------------------------------------------------------------------

# 21. Главный стратегический вывод

Сейчас Temporality находится в переходной точке:

``` text
Phase 2
"Мы доказали, что модель корректна"
                ↓
Следующий этап
"Мы сделали систему, которой можно пользоваться"
                ↓
Дальше
"Люди используют её, чтобы понимать и улучшать
 агентский опыт"
```

Поэтому ближайшая цель --- **не закрыть все остатки Phase 2**.

Ближайшая цель:

> **собрать минимальный end-to-end рабочий контур, в котором реальный
> человек запускает реального агента, а Temporality превращает
> полученный опыт в исследуемую временную картину.**

И уже после этого каждый следующий эксперимент должен одновременно
быть: - acceptance corpus; - реальным рабочим сценарием; - источником
требований к продукту.

Так Temporality не скатится ни в «ещё один debugger», ни в «ещё один
agent runtime».
