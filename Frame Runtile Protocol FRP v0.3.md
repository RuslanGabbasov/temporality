# Frame Runtime Protocol — FRP v0.3

# 

```
┌───────────────────────────────────────────┐
│ COGNITIVE LOOP                            │
│                                           │
│ Frame → Render → Model → Emission → Frame │
└─────────────────────┬─────────────────────┘
                      │
                 Affordance
                      │
┌─────────────────────▼─────────────────────┐
│ EXECUTION LOOP                            │
│                                           │
│ Request → Plan → Execute → Result         │
└─────────────────────┬─────────────────────┘
                      │
                    Events
                      │
┌─────────────────────▼─────────────────────┐
│ MEMORY LOOP                               │
│                                           │
│ Events → Claims → Regions → Procedures    │
└───────────────────────────────────────────┘
```

# 1. Core model

```
Objective + Policy + Memory + Frame + Execution State
                         |
                         v
                   Attention Engine
                         |
                         v
                    Render Layer
                         |
                         v
                    RenderPacket
                         |
                         v
                       Model
                         |
                         v
                CognitiveEmission AST
                         |
                         v
                      Reducer
              +----------+----------+
              |          |          |
              v          v          v
            Frame      Events    Executions
              |          |          |
              +----------+----------+
                         |
                         v
                  Durable Memory
```

Формула:

```
context_t  = render(memory_t, objective, policy, frame_t, execution_state_t)
emission_t = model(context_t)
decision_t  = reduce(objective, policy, frame_t, execution_state_t, emission_t)

frame_t+1  = decision_t.frame
events     = decision_t.events
executions = decision_t.executions
memory_t+1 = append(events)
```

Главный принцип:

> **Модель предлагает когнитивные переходы. Runtime определяет, что реально произошло.**

---

# 2. State model

## 2.1 Cognitive State

Frame описывает положение агента в памяти:

- focus;

- working set;

- as_of;

- branch;

- mode;

- attention policy;

- budget.

Frame не содержит всё состояние мира.

## 2.2 Execution State

Отдельно живут:

- shell/process;

- test run;

- browser session;

- build;

- MCP call;

- deployment;

- planner;

- human approval;

- timeout/retry.

Execution может продолжаться, пока Frame меняется.

## 2.3 Durable Memory State

- Events;

- Claims;

- Regions;

- Edges;

- Procedures;

- provenance;

- trust;

- metrics;

- snapshots.

## 2.4 Ephemeral Cognition

Не всё рассуждение модели должно становиться durable memory:

- черновые гипотезы;

- candidate actions;

- transient attention candidates;

- промежуточные оценки.

Durable становится только значимое cognition state.

---

# 3. Frame

```json
{
  "frame_id": "uuid",
  "parent_frame_id": "uuid|null",
  "agent_id": "uuid",
  "episode_id": "uuid",
  "branch_id": "uuid",
  "objective_id": "uuid",

  "as_of": "2026-09-14T10:00:00Z",

  "focus": {
    "type": "region|claim|event|query",
    "id": "uuid|null",
    "query": "string|null"
  },

  "working_set": [
    {"type": "region|claim|event|execution", "id": "uuid"}
  ],

  "mode": "explore|exploit|reflect|verify",

  "attention": {
    "policy": "balanced",
    "deliberate": true,
    "ambient": true,
    "max_candidates": 32
  },

  "zoom": 2,

  "filters": {
    "trust_min": 0.5,
    "agent_ids": [],
    "region_kinds": []
  },

  "budget": {"tokens": 8000},
  "revision": 1
}
```

### Invariants

- Frame не мутируется.

- Каждый новый Frame имеет `parent_frame_id`.

- Transition логируется.

- Frame воспроизводим.

- Frame не содержит secrets.

- Frame не содержит полный stdout/stderr.

- Explicit `attend()` может делать разрешённый semantic jump.

- Hysteresis не блокирует deliberate attention.

---

# 4. Objective

Objective — отдельная сущность, чтобы одна память могла использоваться разными задачами.

```json
{
  "objective_id": "uuid",
  "episode_id": "uuid",
  "text": "Find root cause of intermittent empty document after refresh",
  "success_conditions": [
    "root_cause_supported",
    "reproduction_or_counterevidence",
    "fix_or_next_action"
  ],
  "constraints": {
    "max_cost": 5.0,
    "deadline": "2026-09-14T18:00:00Z"
  }
}
```

Objective участвует в:

- attention scoring;

- render;

- procedure matching;

- completion;

- evaluation.

---

# 5. CognitiveEmission

LLM **не пишет Event напрямую**. Он выдаёт структурированный AST намерений.

```json
{
  "schema": "frp.cognitive-emission.v1",
  "emission_id": "uuid",
  "frame_id": "uuid",

  "observation": [
    {
      "ref": "event:E10",
      "interpretation": "route state is restored before microfrontend mount"
    }
  ],

  "reasoning": [
    {
      "kind": "hypothesis",
      "text": "restoration may race with initialization"
    }
  ],

  "claims": [
    {
      "proposition": "navigation restoration races with mount",
      "confidence": 0.87,
      "status": "candidate"
    }
  ],

  "attention": [
    {
      "op": "attend",
      "target": {
        "type": "query",
        "text": "evidence contradicting race hypothesis"
      }
    }
  ],

  "actions": [
    {
      "affordance": "run_reproduction_test",
      "args": {
        "scenario": "refresh_during_navigation"
      }
    }
  ],

  "frame_ops": [
    {"op": "pin", "ref": "claim:C4"}
  ],

  "completion": null
}
```

Runtime semantics:

| Поле        | Семантика                   |
| ----------- | --------------------------- |
| observation | ссылки на увиденное         |
| reasoning   | обычно ephemeral            |
| claims      | кандидаты durable knowledge |
| attention   | Frame transition            |
| actions     | Affordance requests         |
| frame_ops   | Frame transition            |
| completion  | закрытие Episode            |

---

# 6. Reducer

```
reduce(
  objective,
  policy,
  frame,
  execution_state,
  cognitive_emission
) -> Decision
```

```json
{
  "frame": "Frame'",
  "events": [],
  "executions": [],
  "rejections": [],
  "warnings": []
}
```

Reducer:

1. валидирует schema;

2. проверяет policy;

3. проверяет права;

4. проверяет refs;

5. разрешает affordances;

6. создаёт execution requests;

7. фиксирует durable claims;

8. создаёт новый Frame;

9. пишет transition;

10. возвращает structured result.

Reducer не выполняет shell напрямую.

---

# 7. Event Log

Event Log — единственный source of truth.

```sql
events(
  event_id UUID PRIMARY KEY,
  tx_time TIMESTAMPTZ NOT NULL,
  valid_time TIMESTAMPTZ NOT NULL,

  agent_id UUID,
  episode_id UUID,
  branch_id UUID,
  parent_id UUID NULL,

  type TEXT NOT NULL,
  payload JSONB NOT NULL,
  provenance JSONB NOT NULL,

  embedding VECTOR(1536),

  source_trust REAL,
  evidence_strength REAL,
  freshness REAL,
  agent_trust REAL,
  consensus REAL,
  contradiction REAL
)
```

Единого scalar `trust` больше нет.

```
effective_confidence =
  f(source_trust,
    evidence_strength,
    freshness,
    agent_trust,
    consensus,
    contradiction)
```

---

# 8. Claims

Claim — слой между raw events и semantic regions.

```sql
claims(
  claim_id UUID PRIMARY KEY,
  proposition TEXT NOT NULL,
  confidence REAL,
  status TEXT,
  created_event UUID NOT NULL,
  valid_from TIMESTAMPTZ,
  valid_to TIMESTAMPTZ NULL
)
```

Relations:

```
claim_relations(
  src_claim UUID,
  dst_claim UUID,
  type TEXT,
  weight REAL,
  evidence_event UUID
)
```

Типы:

```
supports
contradicts
derived_from
supersedesМодель:
```

```
Event
  |
  +--> Claim <--> Claim
  |
  +--> Region projectionRegion не является первичной истиной.
```

---

# 9. Regions

Region — rebuildable projection.

```sql
regions(
  region_id UUID PRIMARY KEY,
  kind TEXT,
  label TEXT,
  parent_region_id UUID NULL,
  created_ts TIMESTAMPTZ,
  retired_ts TIMESTAMPTZ NULL
)

region_versions(
  region_id UUID,
  version INT,
  valid_from TIMESTAMPTZ,
  valid_to TIMESTAMPTZ,
  label TEXT,
  centroid VECTOR(1536),
  activation REAL,
  projection_version TEXT,
  PRIMARY KEY(region_id, version)
)
```

Merge/split/rename/recluster должны быть воспроизводимы из Event Log + версии projection algorithm.

---

# 10. Attention Engine

Attention — самостоятельная runtime-подсистема.

## Deliberate attention

Выбрано агентом:

```json
{
  "op": "attend",
  "target": {
    "type": "query",
    "text": "evidence against race hypothesis"
  }
}
```

## Ambient attention

Runtime предлагает кандидатов:

```
semantic relevance
+ graph proximity
+ recency
+ activation
+ trust
+ task relevance
+ surprise
+ agent relevance
+ explicit pins
```

Концептуальный score:

```
score(x) =
  w1 semantic_relevance
+ w2 graph_proximity
+ w3 recency
+ w4 activation
+ w5 trust
+ w6 task_relevance
+ w7 surprise
+ w8 agent_relevance
+ w9 pinHysteresis применяется к ambient attention.
```

---

# 11. Render Protocol

## Request

```
POST /v1/render
```

```json
{
  "frame_id": "uuid",
  "objective_id": "uuid",
  "budget_tokens": 8000
}
```

## RenderPacket

```json
{
  "render_id": "uuid",
  "frame_id": "uuid",
  "memory_version": "event:123456",
  "renderer_version": "render-0.3.2",

  "sections": [
    {"kind": "identity", "attention": "ambient", "items": []},
    {"kind": "objective", "items": []},
    {"kind": "map", "attention": "ambient", "items": []},
    {"kind": "focus", "attention": "deliberate", "items": []},
    {"kind": "periphery", "attention": "ambient", "items": []},
    {"kind": "working_set", "attention": "deliberate", "items": []},
    {"kind": "procedures", "attention": "ambient", "items": []},
    {"kind": "recent", "attention": "ambient", "items": []}
  ],

  "outside_frame": {
    "nearby_regions": 17,
    "related_claims": 31,
    "available_executions": 3,
    "hint": "17 related regions are outside the frame"
  },

  "provenance": {
    "projection_version": "region-0.3",
    "attention_version": "attention-0.3",
    "embedding_model": "model-x",
    "as_of": "2026-09-14T10:00:00Z"
  },

  "token_usage": {
    "estimated": 7340,
    "budget": 8000
  }
}
```

Render deterministic при фиксированных:

- memory version;

- Frame;

- objective;

- policy;

- projection versions;

- renderer version;

- attention version;

- embedding version.

---

# 12. Identity и Runtime Policy

Identity хранит mutable:

- роль;

- проектный контекст;

- рабочие предпочтения;

- долгосрочные свойства агента.

Immutable Runtime Policy содержит:

- security contract;

- sandbox restrictions;

- allowed affordances;

- data access policy;

- tenant isolation;

- audit requirements;

- output protocol.

Итого:

```
Agent Context =
  immutable runtime policy
  + identity
  + objective
  + frame render
  + execution summary
```

Большой статичный system prompt не является хранилищем cognition state.

---

# 13. Affordance Protocol

Affordance — семантическая возможность, а не физический tool.

Примеры:

```
run_reproduction_test
inspect_execution_environment
inspect_logs
modify_file
run_build
run_existing_tests
open_browser
query_repository
request_human_approval
```

Агент не обязан знать, реализована ли возможность через shell, MCP, API, browser, workflow или planner.

## Affordance Registry

```json
{
  "id": "run_reproduction_test",
  "execution_mode": "adaptive",

  "input_schema": {
    "scenario": "string"
  },

  "capabilities": [
    "filesystem.read",
    "filesystem.write",
    "process.execute",
    "network.optional"
  ],

  "limits": {
    "timeout_sec": 300,
    "cpu": 2,
    "memory_mb": 4096,
    "disk_mb": 2048
  },

  "planner": {
    "enabled": true,
    "model": "small-reasoning-model",
    "max_steps": 12
  },

  "failure_policy": {
    "retry_transient": true,
    "allow_strategy_change": true,
    "max_retries": 2
  }
}
```

---

# 14. Physical Execution

```
CognitiveEmission
       |
       v
Affordance Resolver
       |
       +--> deterministic workflow
       |
       +--> adaptive planner
                    |
                    v
              Execution Plan
                    |
                    v
             Execution Runtime
              /      |       \
           shell    MCP    browser
              \      |       /
                    |
                    v
              Execution Events`run_reproduction_test` может раскрыться в:
```

```
inspect environment
→ locate repository
→ check dependencies
→ start service
→ run test
→ inspect logs
→ collect artifact
→ stop service
→ summarize result
```

## Deterministic executor

Для известных workflow:

```
resolve workspace
→ verify repository
→ execute configured command
→ collect output
→ classify result
→ emit result
```

LLM не нужен.

## Adaptive planner

Для задач, где последовательность заранее неизвестна:

```
"Reproduce intermittent empty document after refresh"
```

Planner получает:

- execution objective;

- affordance contract;

- environment;

- ограниченный relevant context;

- предыдущие execution results.

Planner может быть child Episode:

```
EP-42
 |
 +-- main Frame F100
 +-- Execution X55
 |
 +-- EP-42/P1 planner
      +-- Frames
      +-- actions
      +-- results
      +-- completion
```

Planner не получает полный cognition universe.

---

# 15. Execution State

```json
{
  "execution_id": "uuid",
  "episode_id": "uuid",
  "affordance": "run_reproduction_test",
  "status": "running",
  "phase": "starting_environment",

  "workspace": {"id": "workspace-123"},

  "resources": {
    "cpu": 2,
    "memory_mb": 4096,
    "disk_free_mb": 8120
  },

  "started_at": "2026-09-14T10:01:00Z"
}
```

В Frame не помещается полный Execution State.

В Render попадает summary:

```
Execution X55: running
phase: starting_environment
elapsed: 18s
```

---

# 16. Failure Semantics

Execution failure — событие substrate.

## Permission

```json
{
  "type": "execution.failed",
  "execution_id": "X55",
  "error": {
    "class": "permission_denied",
    "retryable": false,
    "resource": "/workspace/.cache"
  },
  "diagnostics": {
    "uid": 1001,
    "path": "/workspace/.cache"
  }
}
```

## Disk full

```json
{
  "type": "execution.failed",
  "execution_id": "X55",
  "error": {
    "class": "resource_exhausted",
    "resource": "disk",
    "retryable": true
  },
  "diagnostics": {
    "required_mb": 1200,
    "available_mb": 140
  }
}
```

Следующий render может показать:

```
Execution X55 failed:
disk space exhausted

Suggested affordances:
- inspect_execution_environment
- cleanup_workspace
- retry_execution
```

Агент может выбрать:

```
cleanup → retry
```

или:

```
switch workspace
```

или:

```
change reproduction strategy
```

## Responsibility split

| Слой                | Ответственность                         |
| ------------------- | --------------------------------------- |
| Execution Runtime   | timeout, process death, transient retry |
| Affordance Executor | semantic fallback                       |
| Planner             | локальная последовательность            |
| Main Agent          | изменение общей стратегии               |
| Policy Engine       | окончательное разрешение                |

---

# 17. Action lifecycle

Нельзя превращать:

```
"I ran tests and they passed"
```

в один Event.

Канонический lifecycle:

```
OBSERVE
   ↓
THINK
   ↓
DECIDE
   ↓
ACT
   ↓
RESULT
   ↓
REFLECT
```

Пример:

```
CognitiveEmission
  action: run_reproduction_test
        ↓
ExecutionStarted
        ↓
CommandStarted
        ↓
CommandResult
        ↓
ExecutionCompleted
        ↓
Claim:
  reproduction_failed_under_race_condition
```

Так `blame()` может построить lineage от Claim до физического действия.

---

# 18. Main Agent vs Planner

Не нужно создавать sub-agent на каждый affordance.

Плохая модель:

```
main LLM
  ↓
tool
  ↓
sub-agent
  ↓
commandПравильная:
```

```
main agent
   |
   | semantic affordance
   v
runtime
   |
   +--> deterministic executor
   |
   +--> adaptive planner
```

Sub-agent — реализационный механизм конкретного adaptive affordance, а не архитектурная примитива cognition.

---

# 19. Step API

```
POST /v1/step
```

```json
{
  "frame_id": "uuid",
  "objective_id": "uuid",
  "model": "model-id"
}
```

Pipeline:

```
load Frame
→ load Objective
→ load execution summaries
→ render
→ model
→ validate CognitiveEmission
→ reduce
→ persist transition/events
→ start executions
→ return new Frame
```

Response:

```json
{
  "frame_id": "new-frame",
  "parent_frame_id": "old-frame",

  "events": ["event:123"],

  "executions": [
    {
      "execution_id": "X55",
      "status": "running"
    }
  ]
}
```

---

# 20. Replay

Для replay сохраняются:

```
event log
+ initial Frame
+ Objective
+ Runtime Policy version
+ renderer version
+ attention version
+ projection versions
+ embedding model version
+ model identifier
+ generation parameters
```

Replay не повторяет irreversible side effects.

Вместо этого используются recorded results / replay adapters.

Цель:

> Что именно агент видел, какой Frame у него был и почему он принял это решение?

---

# 21. Fork

```
Memory M100
    |
    v
Frame F100
    |
    +-----------+
    v           v
 Model A     Model B
    |           |
 Branch A    Branch B
```

Можно сравнить:

- выбранные регионы;

- claims;

- affordances;

- executions;

- cost;

- success;

- missed evidence.

---

# 22. Episode

```
episodes(
  episode_id UUID PRIMARY KEY,
  parent_episode_id UUID NULL,
  root_frame_id UUID,
  head_frame_id UUID,
  branch_id UUID,
  status TEXT,
  objective_id UUID,
  started_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ NULL,
  cost NUMERIC
)
```

Episode объединяет Frames, Events, Executions, branches, outcome, cost и duration.

Повторяющиеся успешные Episodes становятся источником Procedures.

---

# 23. Procedures

Procedure — скомпилированный опыт:

```
Situation
   ↓
Action sequence
   ↓
Outcome
```

Она может состоять из:

```
semantic trigger
+ preconditions
+ affordance graph
+ expected outcomes
+ historical success rate
```

Procedure появляется в Render как affordance при совпадении Frame + Objective.

---

# 24. Multi-agent

```
                  Shared Memory
              /       |        \
             /        |         \
        Agent A    Agent B    Agent C
        Frame A    Frame B    Frame C
```

Коммуникация может быть событием:

```
Agent A
  ↓
claim/event
  ↓
shared substrate
  ↓
attention
  ↓
Agent B render
```

Права записи в Identity другого агента запрещены без явного trust/policy.

---

# 25. Human Cognitive Debugger

Человек использует тот же substrate, но другой renderer.

```
open Frame
→ inspect render
→ inspect outside_frame
→ inspect provenance
→ blame claim
→ rewind
→ fork
→ change model
→ continue
```

Ключевые вопросы:

- Что агент знал?

- Что он не видел?

- Почему это попало в focus?

- Почему альтернативная гипотеза не попала?

- Какая версия памяти была доступна?

- Что изменилось после execution?

- Где возникла ошибка?

- Можно ли воспроизвести решение?

---

# 26. Blame

Для Claim/Region/Event:

```
blame(ref)
```

возвращает:

```
Region
  ↓
Claim
  ↓
Evidence
  ↓
Execution
  ↓
Affordance
  ↓
CognitiveEmission
  ↓
Frame
  ↓
Episode
```

Blame — одновременно debugging, provenance и trust mechanism.

---

# 27. Security Boundary

Runtime является security boundary.

LLM не может самостоятельно:

- открыть произвольный filesystem;

- получить secrets;

- изменить policy;

- выбрать неразрешённый tenant;

- обойти sandbox;

- изменить чужой Identity;

- повторить irreversible action при replay.

Все внешние действия:

```
LLM intent
→ schema validation
→ policy
→ capability check
→ resource limits
→ execution
→ result event
```

---

# 28. Minimal API

Обязательные:

```
POST /v1/render
POST /v1/step
POST /v1/replayСлужебные:
```

```
GET  /v1/frames/{id}
GET  /v1/events/{id}
GET  /v1/claims/{id}
GET  /v1/executions/{id}
POST /v1/fork
POST /v1/blame
```

---

# 29. Minimal PostgreSQL PoC

Для PoC:

```
PostgreSQL
 ├── events
 ├── claims
 ├── claim_relations
 ├── frames
 ├── episodes
 ├── branches
 ├── executions
 └── projections

pgvector
 ├── event embeddings
 ├── claim embeddings
 └── region embeddings
```

Большие artifacts/snapshots:

```
MinIO/S3
```

Runtime:

```
FRP Runtime
 ├── reducer
 ├── renderer
 ├── attention
 ├── projection worker
 ├── affordance resolver
 └── execution adapter
```

Kafka/Redpanda, graph DB и отдельный TSDB не обязательны до появления реальной нагрузки.

---

# 30. Canonical Event Registry

```
episode.started
episode.completed
episode.failed

frame.created
frame.transitioned
frame.forked

observation.recorded

claim.candidate
claim.supported
claim.refuted
claim.superseded

attention.selected
attention.suggested

affordance.requested

execution.created
execution.started
execution.progress
execution.failed
execution.completed
execution.cancelled

command.started
command.result

procedure.matched
procedure.outcome

metric.recorded

branch.created
branch.merged
branch.abandoned
```

Важно:

> Event type описывает факт runtime/history, а не намерение модели.

Поэтому `run_reproduction_test` — Affordance Request, а не Event type.

---

# 31. Durable vs ephemeral

| Данные              | По умолчанию             |
| ------------------- | ------------------------ |
| Raw model reasoning | ephemeral                |
| Observation refs    | transient                |
| Candidate claim     | durable                  |
| Supported claim     | durable                  |
| Frame transition    | durable                  |
| Affordance request  | durable                  |
| Execution lifecycle | durable                  |
| stdout/stderr       | artifact + summary event |
| Final outcome       | durable                  |
| Attention score     | projection/metric        |
| Region clustering   | projection               |
| Procedure           | derived projection       |
| Secrets             | never memory             |

Это снижает риск self-poisoning substrate.

---

# 32. Context budget

При недостатке бюджета:

```
KEEP:
  runtime policy
  objective
  identity
  focus
  critical execution failures
  pinned working set

TRIM:
  recent
  periphery
  map detail
  low-confidence ambient suggestions
```

Attention не должен вытеснять Objective.

---

# 33. Interruption / Recovery

При:

- LLM kill;

- network loss;

- runtime restart;

- execution still running

runtime делает:

```
load head Frame
+
load active executions
+
load durable events
+
render
+
continue
```

Состояние не восстанавливается через угадывание из последнего prompt.

---

# 34. Killer Test #1 — Crash recovery

1. Создать Frame F100.

2. Зафиксировать memory version.

3. Render.

4. Call model.

5. Сохранить CognitiveEmission.

6. Reducer создаёт F101.

7. Убить runtime.

8. Восстановить.

9. Replay F100.

10. Получить идентичный RenderPacket.

11. Продолжить с F101.

Критерий:

```
same inputs
→ same render
→ same reducer result
```

---

# 35. Killer Test #2 — Fork cognition

Один и тот же Frame + Memory:

```
          F100
         /    \
     Model A  Model B
       |        |
    Branch A  Branch B
```

Сравниваются не только ответы, но и когнитивные траектории:

- attention;

- claims;

- affordances;

- executions;

- cost;

- success;

- missed evidence.

---

# 36. Observability

## Render

```
render_latency
render_tokens
focus_tokens
ambient_tokens
outside_frame_count
```

## Attention

```
attention_entropy
focus_switch_rate
ambient_hit_rate
missed_relevant_items
attention_collapse_score
```

## Memory

```
event_count
claim_count
region_count
branch_count
projection_lag
snapshot_size
```

## Execution

```
execution_latency
failure_rate
retry_rate
resource_exhaustion
planner_steps
```

## Agent

```
task_success
cost
tokens
time_to_success
frame_transitions
affordance_count
```

---

# 37. Failure modes

| Failure              | Mitigation                            |
| -------------------- | ------------------------------------- |
| Attention collapse   | exploration, decay, ambient diversity |
| Context thrash       | ambient hysteresis                    |
| Memory poisoning     | provenance, trust, claims             |
| Region explosion     | hierarchy, merge policy               |
| Identity drift       | immutable policy + re-anchor          |
| Render bias          | OUTSIDE_FRAME                         |
| Execution failure    | structured result events              |
| Planner loop         | max steps / budget                    |
| Disk exhaustion      | resource preflight                    |
| Permission failure   | capability check                      |
| Projection drift     | replay                                |
| Snapshot bloat       | compaction                            |
| Procedure poisoning  | outcome-weighted success              |
| Claim overconfidence | evidence/contradiction model          |

---

# 38. Development order

| Milestone | Scope                                |
| --------- | ------------------------------------ |
| M0        | Event Log + replay                   |
| M1        | Claims + evidence relations          |
| M2        | Immutable Frame + reducer            |
| M3        | Render + Objective                   |
| M4        | Attention                            |
| M5        | Affordance + deterministic Execution |
| M6        | Adaptive Planner                     |
| M7        | Time Travel + snapshots + blame      |
| M8        | Fork + A/B cognition                 |
| M9        | Procedures                           |
| M10       | Human Cognitive Debugger             |

---

# 39. Definition of Done

- Агент получает RenderPacket + immutable runtime policy.

- Memory tools отсутствуют.

- Frame immutable.

- Frame transitions deterministic.

- CognitiveEmission валидируется schema.

- Emission не является Event.

- Claims отделены от Regions.

- Deliberate attention отделён от ambient.

- Outside-frame информация видима.

- Execution State отделён от Frame.

- Affordance может работать без LLM.

- Adaptive affordance может использовать planner.

- Permission/disk/timeout failures становятся Events.

- Replay не повторяет irreversible side effects.

- Provenance доступен для rendered knowledge.

- Fork позволяет сравнить две когнитивные траектории.

- Kill/restart не теряет рабочее состояние.

- Render и reducer имеют versioned deterministic behavior.

---

# 40. Final architectural thesis

Старый harness:

```
Prompt
+ conversation
+ memory tools
+ RAG
+ skills
+ MCP
+ scratchpad
+ tool calls
+ ad-hoc state
```

Новый runtime:

```
                  Frame
                    |
                  Render
                    |
                  Model
                    |
          CognitiveEmission
                    |
                  Reducer
              /      |       \
           Frame   Events   Execution
              \      |       /
               Memory Substrate
```

Главная архитектурная ставка:

> **Агент больше не является процессом, который каждый раз собирает своё состояние из prompt + tools + memory. Агент становится политикой переходов над persistent Frames, Execution State и Memory Substrate.**  
