# Roadmap: от M10 до продуктового состояния

## 0. Точка старта

M0–M10 уже реализованы:

- append-only Event Log и deterministic replay;
- Claims / evidence;
- immutable Frame + reducer;
- Objective + RenderPacket;
- attention;
- Affordances + Executor;
- Adaptive Planner;
- time-travel / snapshots / blame;
- branches / A-B;
- Procedures;
- Human Cognitive Debugger.

То есть сейчас существует **когнитивное ядро**, но агент практически изолирован от внешнего мира.

Следующий этап принципиально отличается от M0–M10:

> **M0–M10 доказали, что Frame Runtime технически работает.  
> M11+ должны доказать, что через него можно построить полезного агента.**

---

# 1. M11 — World Interface

### Цель

Дать агенту первый реальный канал восприятия внешнего мира.

Минимальный набор:

- filesystem;
- Git;
- process/environment;
- HTTP;
- browser;
- базовые workspace/project resources.

Примеры read-only affordances:

- `list_files`
- `read_file`
- `git_status`
- `git_diff`
- `git_log`
- `inspect_environment`
- `inspect_processes`
- `inspect_http`
- `inspect_browser_page`

Все наблюдения проходят существующий pipeline:

```text
World
  ↓
Observation
  ↓
Event Log
  ↓
Claims / Regions / Graph
  ↓
Render
  ↓
Frame
```

### Главная гипотеза H1

**Frame-based cognition действительно работает, если агент получает информацию о мире не через memory/RAG tools, а через обычный observation → memory → render цикл.**

### Проверка

Запустить агента на неизвестном Git-репозитории:

> «Найди причину падающего теста».

Агент должен самостоятельно:

1. обнаружить структуру проекта;
2. найти исходный код;
3. найти тесты;
4. получить Git state;
5. сформировать Claims;
6. построить карту проекта;
7. перейти к интересующей области.

Если агент постоянно пытается «спросить память», архитектурная гипотеза не подтверждается.

### Оценка

**5–8 агент-дней.**

---

# 2. M12 — World Actions

### Цель

Перейти от наблюдения к воздействию.

Минимально:

- создание/изменение файлов;
- запуск команд;
- Git operations;
- HTTP requests;
- browser actions;
- получение результатов выполнения.

Обязательный pipeline:

```text
model intent
    ↓
policy
    ↓
persist intent
    ↓
effect
    ↓
result
    ↓
event
    ↓
memory
```

Никакого скрытого side effect внутри модели.

### Главная гипотеза H2

**Модель может нормально работать с миром через абстрактные affordances, не зная конкретного tool/MCP API.**

То есть модель должна мыслить примерно:

```text
inspect repository
run reproduction
modify file
run test
open browser page
```

а runtime уже решает, является ли это:

- shell;
- Git API;
- MCP;
- Playwright;
- локальным adapter'ом.

### Проверка

End-to-end задача:

> найти bug → воспроизвести → изменить код → запустить тест → убедиться, что исправлено.

### Оценка

**7–12 агент-дней.**

---

# 3. M13 — Bootstrap / First Contact

Это, на мой взгляд, одна из самых важных вех.

### Цель

Агент с пустой памятью должен уметь **самостоятельно заселять субстрат знаниями о мире**.

Для нового workspace:

```text
empty substrate
      ↓
bootstrap episode
      ↓
filesystem / Git / docs / environment
      ↓
observations
      ↓
claims
      ↓
entities
      ↓
regions
      ↓
relations
      ↓
embeddings
```

Bootstrap должен стать обычным Episode, а не специальным RAG-механизмом.

### Что исследовать

Например:

```text
Project
 ├── repository
 ├── modules
 ├── services
 ├── tests
 ├── dependencies
 ├── documentation
 ├── deployment
 └── runtime environment
```

При этом обязательно сохранять provenance:

```text
Claim
  ├── source observation
  ├── source resource
  ├── extraction
  ├── confidence
  └── timestamp
```

### Главная гипотеза H3

**Память действительно может самоорганизовываться из наблюдений, и после bootstrap агенту не требуется классический RAG/index-as-context подход.**

### Проверка

Сравнить:

**A.** обычный agent + system prompt + tools + RAG;

**B.** Frame Runtime + bootstrap + memory.

На серии одинаковых задач измерять:

- task success;
- количество обращений к источникам;
- tokens;
- recall@k;
- время до первого полезного действия;
- количество повторных discovery actions.

### Оценка

**10–15 агент-дней.**

---

# 4. M14 — Knowledge Graph / Entity Layer

Сейчас Claims и Regions уже есть, но для реального мира понадобится более явная семантика сущностей.

Добавить:

```text
Entity
EntityType
EntityAlias
EntityMembership
EntityRelation
```

И связать:

```text
Observation
      ↓
Claim
      ↓
Entity ─── Relation ─── Entity
      ↓
Region
```

Например:

```text
Directum Omni
    │
    ├── contains → Matrix
    ├── contains → RX
    ├── uses → Synapse
    ├── built_with → React
    └── repository → omni-web
```

### Гипотеза H4

**Explicit entities + Claims дают агенту более устойчивую долгосрочную модель мира, чем только embeddings + regions.**

### Проверка

После bootstrap дать агенту задачу, которая требует знания, полученного значительно раньше.

Например:

> «Исправь проблему в компоненте X, учитывая архитектурное ограничение Y, которое ты обнаружил вчера».

Сравнить качество с:

- чистым vector retrieval;
- Regions;
- Entities + Claims + Regions.

### Оценка

**8–12 агент-дней.**

---

# 5. M15 — Useful Agent v0

Это первый настоящий **product gate**.

Нужно перестать добавлять инфраструктурные возможности и проверить:

> **Может ли реальный разработческий агент эффективно работать на этой архитектуре?**

### Сценарии

Минимум 5 классов задач:

1. bug fixing;
2. feature implementation;
3. code investigation;
4. repository/documentation exploration;
5. web/browser investigation.

Для каждой задачи сохранять полный cognitive trace.

### Метрики

Обязательные:

```text
task success
time-to-success
tokens
external actions
repeated observations
attention switches
context size
memory retrieval recall
wrong hypotheses
recovery after interruption
```

И главное:

```text
baseline:
LLM + system prompt + RAG + tools

vs

Frame Runtime:
LLM + Frame + Memory + Affordances
```

### Главная гипотеза H5

**Frame Runtime даёт измеримое преимущество над современным tool/RAG harness.**

Преимущество может выражаться не только в success rate:

- меньше контекста;
- меньше повторных действий;
- лучше recovery;
- лучше long-horizon tasks;
- лучше объяснимость;
- лучше reuse опыта.

### Gate

Если после 20–50 репрезентативных задач:

```text
success ≤ baseline
AND
tokens ≥ baseline
AND
recovery ≤ baseline
```

то нельзя просто продолжать строить платформу.

Нужно пересматривать attention/render/memory semantics.

### Оценка

**15–25 агент-дней** на infrastructure + evaluation harness.

Сам эксперимент может занять больше календарного времени из-за прогонов моделей.

---

# 6. M16 — Cognitive Reliability

Если M15 подтверждён — начинается работа уже не над «может ли агент», а над **стабильностью когнитивного процесса**.

### Что закрыть

#### Attention

- attention collapse;
- context thrashing;
- pathological focus switching;
- missed relevant information;
- exploration/exploitation balance.

#### Memory

- poisoning;
- contradictory claims;
- stale knowledge;
- confidence calibration;
- forgetting/decay;
- provenance.

#### Identity

- identity drift;
- prompt/policy conflicts;
- incorrect self-model;
- re-anchoring.

#### Recovery

Обязательный сценарий:

```text
agent works
   ↓
kill process
   ↓
restart
   ↓
restore Frame
   ↓
restore Execution State
   ↓
continue
```

И второй:

```text
Frame_t
 ├── model A
 └── model B
```

сравнение траекторий.

### Гипотеза H6

**Когнитивное состояние действительно можно сделать durable и воспроизводимым, не сохраняя скрытый scratchpad модели.**

### Оценка

**12–18 агент-дней.**

---

# 7. M17 — Production Execution / Security Boundary

До этого момента Executor фактически экспериментальный.

Теперь его надо превратить в настоящий execution runtime.

### Нужно

- sandbox;
- filesystem isolation;
- process isolation;
- resource limits;
- CPU/memory/time limits;
- network policy;
- credentials;
- secret isolation;
- capability-based permissions;
- audit;
- cancellation;
- timeout;
- retries;
- idempotency;
- effect replay adapters;
- malicious tool/response handling.

Особенно важно разделить:

```text
Cognitive Runtime
        │
        │ intent
        ▼
Execution Runtime
        │
        │ effect
        ▼
World
```

Когнитивный runtime **не должен иметь прямого доступа к host environment**.

### Гипотеза H7

**Безопасность можно выразить как policy над affordances/capabilities, не возвращаясь к tool-centric cognition.**

### Оценка

**15–25 агент-дней.**

Это одна из тех областей, где результат агента обязательно должен проходить человеческий security review.

---

# 8. M18 — Multi-Agent Runtime

Теперь можно переходить к флоту.

Модель:

```text
             Shared Memory Substrate
                     │
       ┌─────────────┼─────────────┐
       ↓             ↓             ↓
    Agent A        Agent B        Agent C
     Frame          Frame          Frame
```

Коммуникация:

```text
Agent A
   ↓
Event
   ↓
shared substrate
   ↓
attention
   ↓
Agent B
```

а не:

```text
A → direct message → B
```

### Нужно

- agent identity;
- per-agent Frame;
- visibility policies;
- trust between agents;
- shared/private regions;
- communication events;
- ownership;
- concurrent writes;
- conflict handling;
- branch isolation;
- fleet debugger.

### Главная гипотеза H8

**Общий memory substrate действительно лучше прямого agent-to-agent messaging.**

### Проверка

Например:

```text
Lead
 ├── исследует проблему
 ├── создаёт hypothesis
 │
 ├── Coder
 │    └── реализует
 │
 └── QA
      └── проверяет
```

При этом QA не должен получать специальный handoff message.

Он должен обнаружить relevant knowledge через substrate.

### Оценка

**15–25 агент-дней.**

---

# 9. M19 — Procedures / Experience Compilation

Procedures уже реализованы, но здесь нужно проверить их **реальную ценность**.

Идея:

```text
Episodes
   ↓
successful trajectories
   ↓
Procedure
   ↓
future Frame
   ↓
affordance
```

### Важный эксперимент

Не спрашивать:

> «Работает ли Procedure?»

А сравнить:

```text
cold agent
vs
agent + previous episodes
vs
agent + procedures
```

на повторяющихся классах задач.

### Гипотеза H9

**Опыт агента может компилироваться в reusable Procedures и реально уменьшать стоимость будущего reasoning.**

Если гипотеза подтверждается — это потенциально одна из самых сильных частей всей архитектуры.

### Оценка

**8–15 агент-дней** сверх уже существующего M9.

---

# 10. M20 — Human Cognitive Debugger → Product UI

M10 уже дал технический debugger.

Теперь его надо превратить в **главный продуктовый интерфейс**.

Человек должен уметь:

```text
Episode
  ↓
Frame timeline
  ↓
What agent saw
  ↓
What agent ignored
  ↓
Why attention moved
  ↓
Which claim was believed
  ↓
Which action was taken
  ↓
What changed in world
```

Особенно важны операции:

- «что агент знал в этот момент?»;
- «почему он решил это?»;
- «что было за пределами Frame?»;
- `blame`;
- fork;
- replay;
- A/B;
- diff;
- branch comparison.

### Гипотеза H10

**Cognitive Debugger — не просто observability UI, а принципиально более полезный способ отладки AI-систем.**

### Оценка

**10–15 агент-дней.**

---

# 11. M21 — Product API / SDK

До этого runtime можно использовать внутренне.

Теперь появляется стабильный внешний контракт.

Минимальный API:

```text
createAgent
createEpisode
createFrame
render
step
execute
replay
fork
inspect
```

И SDK минимум для:

- Go;
- TypeScript/Python — в зависимости от реальных потребителей.

При этом внутренние PostgreSQL schemas, projection implementation и конкретные Executor adapters не должны становиться public API.

### Гипотеза H11

**Архитектура достаточно хорошо сформирована, чтобы другой разработчик мог подключить агента без знания внутренностей runtime.**

### Оценка

**8–12 агент-дней.**

---

# 12. M22 — Multi-Tenant / Platform

Если это должен быть настоящий продукт/платформа, а не framework для одной команды:

### Нужно

- tenants;
- projects/workspaces;
- agent identities;
- quotas;
- token accounting;
- execution quotas;
- storage quotas;
- secrets;
- RBAC;
- audit;
- retention;
- tenant isolation;
- per-tenant memory;
- shared/global knowledge policies.

Модель:

```text
Tenant
 ├── Projects
 │    ├── Agents
 │    │    ├── Episodes
 │    │    └── Frames
 │    └── Memory
 │
 └── Policies
```

### Оценка

**15–25 агент-дней.**

---

# 13. M23 — Scale / HA / Operations

Только здесь имеет смысл всерьёз заниматься распределённой архитектурой.

### Проверить

- 10M / 100M+ events;
- concurrent agents;
- concurrent renders;
- concurrent executions;
- projection rebuild;
- snapshot recovery;
- DB failure;
- runtime restart;
- executor failure;
- browser failure;
- network partition;
- long-running episodes.

### Целевые NFR из исходной концепции

В текущем документе заложены:

- Render p95 ≤ 300 ms при бюджете 8k tokens;
- `as_of` p95 ≤ 1 s на 10M events;
- deterministic replay;
- append-only source of truth;
- provenance/audit;
- configurable retention/compaction.

Это теперь превращается из требований PoC в production gates.

### Важный принцип

Не добавлять Kafka/Redpanda/Neo4j/Timescale/Qdrant просто потому, что «продукт».

Сначала нагрузочные тесты.

Если PostgreSQL + pgvector + projection workers выдерживают реальные нагрузки — оставить их.

### Оценка

**15–30 агент-дней**, но здесь значительно больше зависит от инфраструктуры.

---

# 14. M24 — Evaluation / Benchmark Platform

Для продукта это обязательная часть.

Нужен собственный набор benchmark'ов:

### Cognitive

- long-horizon reasoning;
- memory recall;
- temporal reasoning;
- contradiction handling;
- attention;
- exploration;
- recovery.

### World

- filesystem;
- Git;
- browser;
- HTTP;
- code execution.

### Memory

- poisoning;
- stale facts;
- conflicting sources;
- provenance;
- forgetting.

### Runtime

- crash;
- replay;
- fork;
- branch;
- executor failure.

### Product

- cost;
- latency;
- success rate;
- user intervention;
- repeatability.

Каждый релиз runtime должен прогоняться через один и тот же benchmark suite.

### Гипотеза H12

**Качество Cognitive Runtime можно измерять независимо от конкретной LLM.**

Это критически важно: иначе окажется, что вся архитектура — просто артефакт одной модели.

### Оценка

**10–15 агент-дней** на инфраструктуру benchmark'ов.

---

# 15. M25 — Production Hardening

Последний слой перед реальным использованием:

- migrations;
- backward compatibility;
- event schema evolution;
- projection versioning;
- render versioning;
- model versioning;
- embedding versioning;
- snapshot migration;
- backup/restore;
- disaster recovery;
- monitoring;
- alerting;
- tracing;
- security audit;
- rate limits;
- operational runbooks;
- upgrade/rollback;
- data retention;
- GDPR/delete policies, если продукт предполагает соответствующие deployment'ы.

Особенно важен уже заложенный принцип:

```text
event log
    +
version manifest
    +
snapshot
    +
projection version
    +
renderer version
    +
embedding version
    +
model provenance
```

должны позволять объяснить, **почему агент получил именно такой Frame**.

### Оценка

**15–25 агент-дней.**

---

# Сводная карта

| Веха | Результат | Основная гипотеза | Агент-дни |
|---|---|---|---:|
| M11 | World Read | агент может воспринимать мир через substrate | 5–8 |
| M12 | World Actions | affordance abstraction работает | 7–12 |
| M13 | Bootstrap | substrate сам заполняется знаниями | 10–15 |
| M14 | Knowledge Graph | entities усиливают долговременную модель | 8–12 |
| M15 | Useful Agent | Frame Runtime лучше/не хуже baseline | 15–25 |
| M16 | Cognitive Reliability | состояние действительно durable | 12–18 |
| M17 | Security/Execution | capability runtime безопасен | 15–25 |
| M18 | Multi-Agent | shared substrate лучше messaging | 15–25 |
| M19 | Procedures | опыт компилируется в reusable cognition | 8–15 |
| M20 | Cognitive Debugger | новый debugger реально полезнее обычного trace | 10–15 |
| M21 | API/SDK | runtime становится платформой | 8–12 |
| M22 | Multi-Tenant | возможен platform deployment | 15–25 |
| M23 | Scale/HA | архитектура выдерживает production load | 15–30 |
| M24 | Evaluation | качество измеримо независимо от модели | 10–15 |
| M25 | Hardening | система эксплуатационно готова | 15–25 |

### Итого

**~173–277 агент-дней** чистой инженерной работы.

Но это не означает 8–13 месяцев одного человека.

Для coding agent я бы закладывал примерно:

- **1 сильный coding agent:** ~5–8 календарных месяцев при постоянной работе;
- **2 параллельных coding agents:** ~3–5 месяцев;
- **3–4 agents:** ~2–4 месяца, но эффективность начнёт падать из-за конфликтов архитектуры, интеграции и необходимости человеческих решений.

При этом **security, архитектурные решения, benchmark design и финальная оценка качества я бы не отдавал агентам полностью**.

---

# Что я считаю критическими hypothesis gates

Из всех гипотез я бы выделил только **6 действительно судьбоносных**.

## Gate A — H1/H2: World Interface

Может ли агент нормально работать с реальным миром через:

```text
observation → substrate → render → cognition → affordance → execution
```

Если нет — чинить архитектуру до продолжения.

---

## Gate B — H3: Bootstrap

Может ли новый агент за разумное количество действий превратить:

```text
empty memory
+
unknown repository
```

в полезную модель мира?

Это, вероятно, **самый важный ближайший эксперимент**.

---

## Gate C — H5: Baseline

Frame Runtime должен быть сравнен с:

```text
LLM
+ system prompt
+ RAG
+ tools
+ conventional agent loop
```

Не с игрушечным baseline.

Если преимущества нет — нельзя оправдать дополнительную сложность архитектуры.

---

## Gate D — H6: Recovery

Убить агента в произвольной точке:

```text
Frame_t
Execution State_t
Memory_t
```

и продолжить.

Если после restart когнитивная траектория ломается — «durable cognition» пока не доказана.

---

## Gate E — H8: Multi-Agent

Сравнить:

```text
A → message → B
```

с:

```text
A → shared substrate → B discovers relevant state
```

Это проверяет саму идею **substrate как коммуникационной среды**.

---

## Gate F — H9: Experience Compilation

Самая интересная долгосрочная проверка:

```text
100 episodes
      ↓
experience
      ↓
procedures
      ↓
101st task
```

Если 101-я задача существенно дешевле/быстрее/надёжнее — появляется очень сильный аргумент в пользу всей архитектуры.

---

# Я бы немного изменил порядок разработки

Не обязательно делать всё строго последовательно.

Оптимальный путь я вижу так:

```text
                    ┌───────────────┐
                    │    M0–M10     │
                    │ Cognitive Core│
                    └───────┬───────┘
                            │
                    ┌───────▼───────┐
                    │ M11 World Read│
                    └───────┬───────┘
                            │
                    ┌───────▼───────┐
                    │ M12 World Act  │
                    └───────┬───────┘
                            │
                ┌───────────▼───────────┐
                │ M13 Bootstrap + M14 KG│
                └───────────┬───────────┘
                            │
                     ★ FIRST GATE ★
                            │
                    ┌───────▼───────┐
                    │ M15 Useful     │
                    │ Agent + A/B    │
                    └───────┬───────┘
                            │
                 ┌──────────▼──────────┐
                 │ M16 Reliability     │
                 │ M19 Procedures     │
                 └──────────┬──────────┘
                            │
                    ★ PRODUCT GATE ★
                            │
              ┌─────────────▼─────────────┐
              │ M17 Security / Execution │
              │ M18 Multi-Agent          │
              └─────────────┬─────────────┘
                            │
                    ┌───────▼───────┐
                    │ M20 Debugger   │
                    │ M21 API/SDK    │
                    └───────┬───────┘
                            │
                    ┌───────▼───────┐
                    │ M22 Platform   │
                    │ M23 Scale/HA   │
                    └───────┬───────┘
                            │
                    ┌───────▼───────┐
                    │ M24 Evaluation │
                    │ M25 Hardening  │
                    └───────────────┘
```

---

# Самое важное

Я бы **не ставил целью «закончить M25»**.

У проекта есть три реально значимых состояния.

### State 1 — Cognitive Prototype

Это уже практически достигнуто:

> Агент существует внутри Frame, память воспроизводима, есть attention, claims, procedures, branches и debugger.

### State 2 — Useful Agent

Это следующий настоящий рубеж:

> Агент получает реальный мир, самостоятельно исследует его, строит память, выполняет действия и решает реальные задачи лучше или дешевле conventional harness.

**M11–M16.**

### State 3 — Product Platform

И только после подтверждения State 2:

> Несколько агентов могут безопасно работать с общим substrate, система имеет API, isolation, quotas, observability, HA, benchmark и production lifecycle.

**M17–M25.**

И именно **State 2 я бы сделал главным экспериментальным рубежом**. В исходном документе PoC уже формулирует правильные базовые критерии — рост recall@k, снижение context tokens, не худший task success, быстрый `as_of` и deterministic replay. Но теперь к ним нужно добавить главное: **реальные world tasks против сильного conventional agent baseline**.

Если M15 проходит — тогда у вас уже не просто интересная архитектурная идея, а появляется основание вкладываться в полноценную платформу.