# Архитектура FRP

```
┌──────────────────────────────────────────────────────────────┐
│                         FRP Debugger                         │
│                       React + TypeScript                     │
│   Frame / Map / Claims / Timeline / Replay / Fork / Blame    │
└──────────────────────────────┬───────────────────────────────┘
                               │ HTTP/JSON
┌──────────────────────────────▼───────────────────────────────┐
│                         FRP Runtime                          │
│                              Go                              │
│                                                              │
│  Frame Manager      Render Engine       Attention Engine     │
│  Reducer            Policy Engine       Affordance Resolver  │
│  Episode Manager    Replay/Fork         Model Adapter        │
└──────────────┬───────────────────┬───────────────────────────┘
               │                   │
               │                   │ Execution API
               │                   ▼
               │        ┌──────────────────────────┐
               │        │     FRP Executor         │
               │        │           Go             │
               │        │                          │
               │        │ Docker / processes       │
               │        │ filesystem / network     │
               │        │ browser / MCP adapters   │
               │        │ resources / permissions  │
               │        └────────────┬─────────────┘
               │                     │
               ▼                     ▼
┌──────────────────────────────────────────────────────────────┐
│                         PostgreSQL                           │
│                                                              │
│ Events │ Frames │ Claims │ Episodes │ Branches │ Executions  │
│ Projections │ Regions │ Procedures │ Embeddings (pgvector)   │
└──────────────────────────────────────────────────────────────┘
                               │
                               ▼
                        S3 / MinIO
                  large artifacts / logs
```

Архитектура намеренно разделяется на три независимых контура:

```
COGNITIVE LOOP
Frame → Render → Model → Emission → Reduce → Frame'

EXECUTION LOOP
Affordance Request → Plan → Execute → Result

MEMORY LOOP
Events → Claims → Regions → Procedures
```

Эти контуры имеют разную семантику и разную частоту работы. Нельзя смешивать их в единую «цепочку tool calls».

---

### 2. FRP Runtime

Основной runtime реализуется на **Go**.

Go используется как язык именно для runtime, а не как обязательный язык всего окружения.

Основные обязанности `frp-runtime`:

- управление immutable Frames;
- выполнение `render`;
- управление Attention;
- проверка и редукция `CognitiveEmission`;
- управление Episodes и Branches;
- разрешение Affordances;
- применение Runtime Policy;
- создание Execution;
- запись событий;
- управление Claims;
- replay/fork;
- взаимодействие с model adapters;
- аудит и provenance.

Runtime **не выполняет непосредственно shell-команды, Docker-команды, браузерные операции или MCP-вызовы**.

Его задача — решить:

> что агент хочет сделать, разрешено ли это, какое выполнение этому соответствует и какое состояние должно быть зафиксировано.

Физическое выполнение находится за отдельной границей `frp-executor`.

---

### 3. FRP Executor

`frp-executor` — отдельный execution runtime, также первоначально реализуемый на Go.

Он отвечает за физическое выполнение:

- процессов;
- shell-команд;
- файловых операций;
- Docker-контейнеров;
- browser automation;
- MCP-вызовов;
- сетевых операций;
- resource limits;
- timeout/cancellation;
- filesystem isolation;
- network policy;
- credentials;
- sandbox policy.

Runtime может находиться в том же процессе на раннем PoC, но **граница между Runtime и Executor должна существовать уже в протоколе**.

Это позволит позднее заменить:

```
Docker
```

на:

```
Kata Containers
Firecracker
Kubernetes Jobs
VM
удалённый execution workerбез изменения когнитивной модели FRP.
```

---

### 4. PostgreSQL как canonical substrate

На первом этапе единственным canonical storage является **PostgreSQL**.

Он хранит:

```
events
frames
episodes
branches
claims
executions
region_versions
region_memberships
edges
procedures
snapshots
projections_metadata
```

Для embeddings используется **pgvector**.

На PoC не вводятся отдельные:

```
Kafka / Redpanda
Neo4j
Qdrant
TimescaleDB
Redis
Temporalесли конкретная измеренная нагрузка не докажет необходимость такого компонента.
```

Главный принцип:

> PostgreSQL хранит source of truth, а Regions, Embeddings, Summaries, Attention indexes и другие производные структуры являются rebuildable projections.

При необходимости большие бинарные артефакты, execution logs, screenshots, build artifacts и другие объекты хранятся в **S3-compatible object storage**, например MinIO.

PostgreSQL хранит ссылки и metadata.

---

### 5. Event Store

Первичная запись когнитивного состояния должна быть транзакционной.

Например, обработка одного `CognitiveEmission`:

```
BEGIN

1. lock/read current Frame
2. validate emission
3. validate Runtime Policy
4. resolve references
5. create frame.transition
6. create claims/events
7. create execution records
8. update Episode head
9. persist next Frame
10. persist projection/version metadata

COMMIT
```

Таким образом, нельзя получить состояние:

```
Frame уже изменился,
но событие перехода потерялосьили:
```

```
Execution создан,
но Frame считает, что его не существует.
```

Для внешних операций применяется принцип:

```
intent persisted
        ↓
execution created
        ↓
external action
        ↓
result persisted
```

а не:

```
external action
        ↓
попытка записать результат
```

Это важно для crash recovery и replay.

---

### 6. Model Adapter

FRP не должен зависеть от конкретного LLM API.

Вводится абстракция:

```
ModelAdapter
```

с минимальной семантикой:

```
generate(RenderPacket, ModelConfig)
    → CognitiveEmission
```

Адаптеры могут работать с:

- OpenAI-compatible API;
- OpenRouter;
- локальными моделями;
- Ollama;
- специализированными inference endpoints;
- будущими model providers.

Runtime не должен знать особенности конкретной модели.

Версия модели и конфигурация inference являются частью provenance каждого шага.

Для replay необходимо сохранять как минимум:

```
model_id
model_version
provider
temperature
reasoning configuration
sampling configuration
protocol version
renderer version
attention version
projection versions
```

Если необходим строго детерминированный replay, должен существовать механизм сохранения исходного model output.

---

### 7. Execution Backend

Первым backend является Docker.

Пример:

```
run_reproduction_test
        ↓
Affordance Resolver
        ↓
Execution Plan
        ↓
Docker Execution
        ↓
container
 ├── repository
 ├── dependencies
 ├── test runner
 └── browser
        ↓
Execution ResultПри этом агент не получает Docker API.
```

Он видит только:

```
AFFORDANCE REQUEST
        ↓
EXECUTION
        ↓
RESULT
```

Например:

```json
{
  "affordance": "run_reproduction_test",
  "args": {
    "scenario": "refresh_during_navigation"
  }
}
```

может физически превратиться в:

```
create workspace
checkout commit
restore dependency cache
start services
start browser
run reproduction script
collect logs
collect screenshots
collect exit code
destroy/retain environment
```

Количество физических команд не является частью когнитивного протокола.

---

### 8. Affordance Registry

Affordance описывает **семантическую возможность**, а не физическую команду.

Пример:

```json
{
  "id": "run_reproduction_test",
  "version": "1.0",
  "execution_mode": "adaptive",
  "capabilities": [
    "filesystem.read",
    "process.execute",
    "network.local",
    "browser"
  ],
  "limits": {
    "timeout": "10m",
    "cpu": 4,
    "memory_mb": 8192,
    "disk_mb": 20480
  },
  "planner": {
    "enabled": true,
    "model": "small-fast",
    "max_steps": 12
  },
  "failure_policy": {
    "retry_transient": true,
    "allow_strategy_change": true
  }
}
```

Таким образом, один и тот же Affordance может иметь разные реализации:

```
run_reproduction_test
 ├── deterministic executor
 ├── adaptive planner + executor
 ├── remote CI executor
 └── human-assisted executorА когнитивный слой этого не различает.
```

---

### 9. Adaptive Planner

Отдельная LLM-сессия создаётся **только если конкретный Affordance требует адаптивного планирования**.

Она не является обязательным элементом каждого действия.

Для детерминированной операции:

```
run_unit_tests
build_project
get_git_diff
read_file
run_existing_reproductionникакой дополнительный агент не нужен.
```

Для операции:

```
"разберись, как воспроизвести эту проблему"
```

может понадобиться Planner.

В таком случае создаётся дочерний Episode:

```
EP-42 Main Agent
│
├── Frame F100
│
├── Execution X55
│
└── EP-42/P1 Adaptive Planner
      ├── Frame P1
      ├── actions
      ├── observations
      └── result
```

Planner получает ограниченный контекст:

```
objective
execution environment
available affordances
relevant memory
previous results
resource limitsи не получает полный когнитивный контекст основного агента.
```

Результат Planner записывается в общий substrate и становится обычным результатом для основного агента.

Таким образом:

> Subagent — не архитектурная сущность FRP. Это один из возможных механизмов реализации Affordance.

---

### 10. Ошибки исполнения

Ошибки execution не должны считаться ошибками cognition.

Например:

```
run_reproduction_test
        ↓
Docker
        ↓
permission denied
```

создаёт:

```json
{
  "type": "execution.failed",
  "execution_id": "X55",
  "phase": "workspace_prepare",
  "error_class": "permission_denied",
  "retryable": false,
  "diagnostics": {
    "path": "/workspace/project",
    "operation": "mount"
  }
}
```

Другой пример:

```json
{
  "type": "execution.failed",
  "execution_id": "X55",
  "phase": "dependency_install",
  "error_class": "disk_exhausted",
  "retryable": true,
  "diagnostics": {
    "required_mb": 4200,
    "available_mb": 180
  }
}
```

После этого результат становится частью Memory/Execution State и попадает в следующий Render.

Основной агент может решить:

```
изменить стратегию
```

а не просто получить исключение от tool call.

---

### 11. Ответственность за retry

Retry разделяется на три уровня.

**Execution Runtime**

Автоматически обрабатывает инфраструктурные transient failures:

```
connection reset
temporary container failure
worker unavailable
temporary filesystem race
```

**Affordance Executor**

Может применять семантический fallback:

```
dependency cache
offline install
другой execution worker
другой способ получения артефакта**Cognitive Agent**
```

Решает, когда нужно изменить стратегию:

```
не получилось запустить browser test
→ использовать unit-level reproduction

нет места для полного build
→ использовать существующий artifact

нет прав на production
→ запросить read-only diagnostic pathЭто принципиально отличается от модели:
```

```
LLM → shell → exception → LLM
```

---

### 12. Browser

Browser automation также является execution backend.

Первоначальная реализация:

```
Playwright
+
Chromium
+
отдельный browser container
```

Browser не является частью когнитивного API.

Агент использует, например:

```
inspect_web_application
run_browser_reproduction
capture_page_state
```

а физическая реализация может использовать:

```
Playwright
CDP
Chromium
browser container
remote browser workerРезультаты:
```

```
DOM snapshot
screenshot
console logs
network trace
video
structured observationsзаписываются как Execution Results / Events.
```

---

### 13. MCP

MCP интегрируется не в Cognition Layer, а в Affordance/Execution Layer.

```
Agent
  ↓
Affordance
  ↓
MCP adapter
  ↓
MCP server
  ↓
Result Event
```

Следовательно, модель не должна знать:

```
какой MCP server
какой transport
какой tool name
какие credentialsЭто позволяет в будущем заменить MCP на:
```

```
native API
REST
gRPC
local process
database adapter
browser
human action
```

без изменения CognitiveEmission.

---

### 14. Redis, Kafka и распределённые очереди

На PoC они не требуются.

PostgreSQL может выполнять одновременно:

```
event store
state store
transaction boundary
execution queue
projection metadata
vector index
```

Если нагрузка вырастет, очереди вводятся **по фактическому bottleneck**, а не заранее.

Возможная эволюция:

```
PoC
PostgreSQL

        ↓

несколько runtime/executor workers
PostgreSQL + queue

        ↓

большой deployment
PostgreSQL
+
NATS JetStream / Kafka
+
distributed executors
```

При этом Event Log остаётся canonical source of truth; очередь не становится источником состояния.

---

### 15. API

Первоначально используется HTTP/JSON.

Минимальный внешний API:

```
POST /v1/render
POST /v1/step
POST /v1/replay
POST /v1/fork
GET  /v1/frames/{id}
GET  /v1/episodes/{id}
GET  /v1/executions/{id}
GET  /v1/events/{id}
```

Внутренние API между Runtime и Executor также первоначально могут быть HTTP/JSON.

gRPC вводится только если профилирование покажет, что latency/throughput этого требует.

---

### 16. Protocol serialization

Все основные FRP objects сериализуются в JSON.

Schema фиксируется через JSON Schema:

```
Frame.schema.json
Objective.schema.json
RenderPacket.schema.json
CognitiveEmission.schema.json
Event.schema.json
Claim.schema.json
Execution.schema.json
Affordance.schema.json
Episode.schema.json
```

Каждая схема имеет version:

```json
{
  "protocol": "frp",
  "version": "0.3"
}
```

Для хранения в PostgreSQL используются обычные нормализованные поля там, где необходимы индексы и ограничения, и `JSONB` для extensible payload.

---

### 17. Package boundaries

Внутри Go Runtime рекомендуется сразу разделить модули по архитектурным обязанностям:

```
/frp
    /protocol
    /runtime
    /frame
    /cognition
    /substrate
    /projection
    /attention
    /affordance
    /execution
    /replay
    /policy
    /model
```

Смысл зависимостей:

```
protocol
   ↑
frame ← cognition ← runtime
   ↑        ↑
substrate   attention
   ↑
projection

runtime → affordance → execution
runtime → model
runtime → replay
runtime → policy
```

`cognition` не должен напрямую обращаться к PostgreSQL.

`execution` не должен самостоятельно менять Frame.

`projection` не является source of truth.

`model` не имеет доступа к substrate напрямую.

Это обеспечивает архитектурную границу:

```
Model
  ↓
CognitiveEmission
  ↓
Runtime
  ↓
Substrate / Execution
```

---

### 18. FRP Debugger

Frontend реализуется на React + TypeScript.

Debugger должен быть не обычной административной панелью, а **cognitive debugger**.

Основные представления:

```
Timeline
    ↓
Frame
    ↓
RenderPacket
    ↓
Map / Regions
    ↓
Claims
    ↓
Events
    ↓
Executions
```

Ключевые операции:

```
inspect frame
time travel
replay
fork
compare branches
inspect claim provenance
inspect outside_frame
inspect attention
inspect execution
blame
```

Особенно важен режим:

> «Что агент знал в этот момент?»

Debugger должен показывать не только всё существующее в Memory, но и конкретный Render, который реально получил агент.

То есть можно восстановить:

```
Memory
   ↓
Attention
   ↓
Render
   ↓
Model input
   ↓
Model output
   ↓
Decision
```

---

### 19. Observability

Используются:

```
OpenTelemetry
Prometheus
structured JSON logs
```

Основные метрики:

```
render_latency
render_tokens
attention_latency
step_latency
model_latency
reduce_latency

execution_duration
execution_success_rate
execution_failure_rate
execution_retry_rate

projection_lag
event_append_latency
claim_generation_latency

frame_count
episode_count
branch_count

context_reuse
token_cost
```

Trace одного шага должен позволять увидеть:

```
step
 ├── render
 │    ├── attention
 │    ├── projections
 │    └── tokenization
 │
 ├── model
 │
 ├── reduce
 │    ├── policy
 │    ├── claims
 │    ├── frame transition
 │    └── affordances
 │
 └── execution
      ├── planning
      ├── executor
      └── result
```

---

### 20. Минимальный deployment

Первый PoC может состоять всего из:

```
Docker Compose

┌─────────────────────┐
│     frp-runtime     │
│        Go           │
└──────────┬──────────┘
           │
     ┌─────┴─────┐
     │           │
┌────▼────┐ ┌────▼─────┐
│Postgres │ │ Executor │
│pgvector │ │   Go     │
└─────────┘ └────┬─────┘
                 │
              Docker
```

И отдельно:

```
frp-debugger
React/TypeScript
```

Object storage на первом этапе может отсутствовать, если все артефакты помещаются в локальное execution storage.

Таким образом, минимальный PoC — это фактически **три процесса + PostgreSQL**:

```
Runtime
Executor
Debugger
PostgreSQL
```

Model provider может быть внешним.

---

### 21. Production evolution

При росте нагрузки архитектура развивается независимо по каждому контуру.

Cognitive scaling:

```
multiple Runtime instances
        ↓
shared PostgreSQL
        ↓
distributed frame ownership
```

Execution scaling:

```
Executor
    ↓
N execution workers
    ↓
Docker / Kubernetes / Firecracker
```

Projection scaling:

```
Event Log
    ↓
projection workers
    ↓
region / vector / procedure indexes
```

Storage scaling:

```
PostgreSQL
    +
S3
```

Queue scaling:

```
PostgreSQL
    ↓
NATS/Kafka
```

При этом Cognitive Runtime не должен превращаться в распределённую систему только потому, что Execution Runtime является распределённым.

Это разные масштабируемые контуры.

---

### 22. Главная архитектурная граница

FRP должен принципиально отличаться от классического agent harness:

```
Классический harness

LLM
 ↓
tool call
 ↓
tool
 ↓
result
 ↓
prompt assembly
 ↓
LLMFRP:
```

```
                    ┌───────────────┐
                    │    Memory     │
                    │ Events/Claims │
                    │ Regions/etc.  │
                    └───────┬───────┘
                            │
                            ▼
Frame ────────────────→ Render
  ▲                       │
  │                       ▼
  │                      LLM
  │                       │
  │                CognitiveEmission
  │                       │
  └────── Reduce ←────────┘
             │
       ┌─────┴─────┐
       │           │
       ▼           ▼
   Memory      Affordance
                  │
                  ▼
              Execution
                  │
                  ▼
                EventsТаким образом, **Frame Runtime не является ещё одним orchestration framework для tool calling**.
```

Его задача — сделать состояние, внимание, память, действие и исполнение различными сущностями с чёткими границами:

```
Frame       — текущее когнитивное состояние
Render      — то, что модель реально видит
Emission    — намерение/решение модели
Event       — зафиксированный факт
Claim       — зафиксированное утверждение
Execution   — физический процесс выполнения
Result      — результат физического процесса
Affordance  — семантическая возможность
Procedure   — скомпилированный опыт
Episode     — единица работы
```

Именно эта граница является основой FRP, а конкретные Go/PostgreSQL/Docker/Playwright/MCP реализации могут меняться независимо от когнитивного протокола.
