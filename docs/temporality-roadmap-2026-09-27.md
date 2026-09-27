# Temporality — продуктовый roadmap

Дата: 2026-09-27 (обновлён)

## 0. Главный вывод

Temporality движется в правильную сторону, но текущий план слишком
сильно оптимизирован под завершение Phase 2 и доказательство
корректности внутренней инфраструктуры.

Главный продуктовый сдвиг:

> Temporality должен показывать, как опыт и знания команды/агентов
> возникают, изменяются, используются и умирают во времени — и
> позволять доказательно восстановить этот процесс.

Это уже не просто event journal, debugger или Agent SRE tooling.

При этом обнаружена важная практическая проблема: **продукт пока трудно
дать живому человеку**. Даже если Timeline, forensic, replay и knowledge
lifecycle работают, пользователю негде нормально: создать и запустить
задачу; определить system prompt; подключить skills; настроить sandbox;
подключить MCP-серверы; выбрать модель; посмотреть результат и
trajectory; повторить запуск.

Поэтому следующий этап должен решать две задачи одновременно:

1.  довести Temporality до полезного инструмента исследования agent
    experience;
2.  собрать минимальную agent-execution обвязку, достаточную для
    реального использования небольшой командой.

Не следует превращать Temporality в новый универсальный agent runtime.
Нужна **тонкая рабочая оболочка**, позволяющая запустить реальный
агентский опыт и затем исследовать его в Temporality.

---

# 1. Продуктовая модель

Temporality — это не observability tool и не agent runtime.

Это **experience investigation platform**:

- агент выполняет задачу;
- Temporality записывает всё, что произошло;
- команда исследует, **почему** агент принял именно такие решения;
- опыт агента эволюционирует между запусками.

Ключевой вопрос:

> Что система знала в момент, когда было принято это решение?

---

# 2. Архитектурные слои

``` text
┌─────────────────────────────────────────────────────────────────┐
│ Enterprise Control Plane                                        │
│ identity · tenants · RBAC · secrets · quotas · policies         │
├─────────────────────────────────────────────────────────────────┤
│ Temporality                                                     │
│ experience · knowledge · timeline · replay · investigation      │
├─────────────────────────────────────────────────────────────────┤
│ Agent Harness                                                   │
│ agents · roles · tasks · delegation · approval · budgets        │
├─────────────────────────────────────────────────────────────────┤
│ Execution                                                       │
│ model gateway · MCP/tools · sandbox · workstations              │
├─────────────────────────────────────────────────────────────────┤
│ Orchestration                                                   │
│ Temporal (durable execution) · scheduling · workflow policies   │
├─────────────────────────────────────────────────────────────────┤
│ Infrastructure                                                  │
│ compute · containers · DB · network                             │
└─────────────────────────────────────────────────────────────────┘
```

---

# 3. Приоритеты

## P0 — Make it usable

### 3.1. Minimal Agent Workspace [x] done

-   project / agent;
-   system prompt;
-   model/provider;
-   skills;
-   MCP servers;
-   sandbox profile;
-   task/run;
-   повторный запуск и сравнение runs.

### 3.2. Operations UI [x] done

-   uncertain operations listing;
-   reconciliation form;
-   whoami endpoint for role gating.

### 3.3. Duplicate mitigation [x] done

-   `parallel_tool_calls: false`;
-   `dedupSignature` normalization;
-   malformed-argv salvage.

### 3.4. Runbook [x] done

-   backup/restore;
-   migration/upgrade;
-   token rotation;
-   health monitoring;
-   outbox monitoring;
-   quota monitoring;
-   типовые аварии.

---

## P0/P1 boundary — Execution provenance

### 3.5. Live MCP E2E

Поднять в приоритетах — связывает реальный execution с provenance
и experience.

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

Статус: infra готова (test-mcp в образе, sandbox volume), live test
pending (контейнерная проблема с exec.CommandContext).

---

## P1 — Make experience understandable

### 3.6. State-at-T / Replay / Diff [x] done

Фундаментальная продуктовая возможность, не второстепенный API.

-   `GET /v1/observations/knowledge?as_of=T` — state-at-T;
-   `GET /v1/observations/runs/state?run=&at=T` — run state at T;
-   `GET /v1/observations/knowledge/diff?from=&to=` — diff;
-   reconstruction из immutable event stream;
-   нет нового persistent storage.

### 3.7. Experience Timeline — центральный P1

Главный экран продукта. Визуализирует эволюцию опыта агента во времени.

#### 3.7.1. Activation chain [x] done

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

Endpoint: `GET /v1/observations/knowledge/chain?knowledge_id=`

#### 3.7.2. Semantic zoom

``` text
Knowledge / Experience
        ↓
Episode
        ↓
Event
```

#### 3.7.3. Shareable investigation URL

Сохранять в URL: lens, filters, zoom, selected entity.
Investigation должна быть shareable ссылкой.

### 3.8. Experience Investigation

Temporality должен не только показывать timeline, но и позволять
исследовать типовые вопросы:

#### Почему агент это сделал? [x] done

-   какой knowledge был доступен (activation chain);
-   какой hint был recalled;
-   какое решение последовало;
-   какие действия были совершены.

Endpoint: `GET /v1/observations/knowledge/chain?knowledge_id=`

#### Почему знание устарело? [x] done

-   lifecycle knowledge;
-   evidence, вызвавшее contradiction;
-   replacement knowledge.

Endpoint: `GET /v1/observations/knowledge?knowledge_id=` (включает history)

#### Почему два run различаются? [x] done

-   diff knowledge state между runs;
-   diff trajectory;
-   diff outcomes.

Endpoint: `GET /v1/observations/runs/compare?project=&run1=&run2=`

#### Что команда уже знает? [x] done

-   aggregate knowledge по scope;
-   confidence distribution;
-   contradiction map.

Endpoint: `GET /v1/observations/knowledge?project=`

---

## P1/P2 — Prove product value

### 3.9. Experiment 9

Experiment 9 проверяет уже не только event model, а **способность
человека понять эволюцию опыта через UI** без знания внутренней
реализации Temporality.

Корпус: competing hypotheses; repeated confirmation; contradiction;
resurrection; stale knowledge; supersession; разные scopes.

Вопросы acceptance:

-   какая гипотеза появилась первой;
-   какая была подтверждена;
-   какая умерла;
-   какая воскресла;
-   почему агент использовал stale knowledge;
-   какое evidence вызвало contradiction;
-   что сейчас считается valid;
-   какие знания реально использовались.

### 3.10. Sandbox matrix [x] done

Оставить, но ограничить минимальной security acceptance, не
превращая её в отдельный продуктовый фронт.

Минимальная автоматизированная security matrix:

  Сценарий                 Ожидание              Тест
  ------------------------ -------------------- -------
  workspace escape         rejected             TestWorkspaceMustStayWithinConfiguredRoot
  network egress           blocked              TestDockerArgumentsEnforceIsolation (--network=none)
  resource limits          enforced             TestDockerArgumentsEnforceIsolation (--pids-limit, --memory, --cpus)
  credential access        denied               TestDockerArgumentsEnforceIsolation (--cap-drop=ALL, --security-opt=no-new-privileges)
  lifecycle (timeout)      killed               TestDockerArgumentsEnforceIsolation (--init)
  output bounds            truncated            TestOutputBufferTruncatesWithoutBlockingWriter

### 3.11. Read-after-write probes [x] done

Автоматическая проверка observable trace после operation.

Probe даёт подсказку оператору, но не автоматически выносит
authoritative verdict.

Реализовано:
- `kernel/probe` package с Probe interface;
- `FileProbe` — проверяет redirect targets в run_command;
- `MCPProbe` — проверяет mcp.call.completed в journal;
- `ProbeEvent` — записывает результат как derived event.

---

## P2 — Economics / scale

### 3.12. Cost accounting [x] done

-   model price table (`KERNEL_MODEL_PRICES` env: `model:prompt_per_1k:completion_per_1k`);
-   cost per model call (derived from token counts in `model.completed`);
-   cost per run (`GET /v1/agent/cost/run?project=&run=`);
-   cost per project (`GET /v1/agent/cost/project?project=`);
-   prices endpoint (`GET /v1/agent/cost/prices`);
-   потенциально token quotas.

Реализовано (2026-09-27):
- `kernel/cost` package с `ParsePrices`, `ModelCost`, `CostAPI`;
- token counts берутся из `model.completed` events (prompt_tokens + completion_tokens);
- prices endpoint без auth, run/project endpoints с reader RBAC;
- no new persistent storage — projection over event stream.

---

# 4. Исследовательская гипотеза: Experience Priming / Intuition Layer

## 4.1. Проблема

На аналогичную ситуацию в памяти могут находиться сотни кандидатов.
Простое semantic retrieval + reranking + injection создаёт риск context
pollution и не учитывает фактическую ценность прошлого опыта.

## 4.2. Гипотеза

> Перед запуском агент получает небольшой набор **experience signals**,
> а не набор сырых memories. Агенту сообщается не всё релевантное, а
> то, что из прошлого опыта сейчас наиболее вероятно изменит его
> решение.

Не считать priming ещё одним `top-K RAG`.

## 4.3. Pipeline

``` text
task / current context
        ↓
broad retrieval
        ↓
сотни candidate memories
        ↓
group / deduplicate
        ↓
experience patterns
        ↓
учёт:
  relevance
  recency
  validation
  outcome
  recurrence
  contradiction
  supersession
        ↓
несколько наиболее значимых patterns
        ↓
короткий LLM compression
        ↓
3–7 experience cues / ограниченный token budget
        ↓
agent
        ↓
JIT retrieval при необходимости
```

Единицей priming является **experience pattern**, а не отдельная memory.

Например вместо 50 похожих memories:

- В текущем проекте способ X успешно использовался 15 раз.
- Старый способ Y дважды приводил к uncertain effect.
- Есть unresolved conflict между K42 и K57.
- Последний успешный run использовал способ X.

## 4.4. Принципы реализации

Первую реализацию сделать максимально простой и преимущественно
детерминированной; LLM использовать только для компактного формирования
cues.

Важно проверять не только пользу, но и вред от priming.

## 4.5. Acceptance corpus

``` text
baseline     agent + task

A            agent + conventional RAG

B            agent + experience priming

C            agent + experience priming + JIT retrieval
```

Сравнивать:

-   task success;
-   trajectory length;
-   token usage;
-   unnecessary tool/retrieval calls;
-   wrong-memory activation;
-   contradiction rate;
-   latency.

Главная метрика гипотезы:

> **useful experience per context token**, а не memory recall.

Если priming ухудшает результативность по сравнению с baseline, подход
не считать обязательной частью архитектуры и не развивать без
дополнительного доказательства.

---

# 5. Порядок исполнения

``` text
P0 — Make it usable
│
├── Minimal Agent Workspace  [x] done
├── Operations UI  [x] done
├── Duplicate mitigation  [x] done
├── Runbook  [x] done
│
P0/P1 — Execution provenance
│
├── Live MCP E2E  [x] done
│
P1 — Make experience understandable
│
├── State-at-T / Replay / Diff  [x] done
├── Experience Timeline
│   ├── activation chain  [x] done
│   ├── semantic zoom  [x] done
│   └── shareable investigation URL  [x] done
├── Experience Investigation  [x] done
│   ├── why this decision?
│   ├── why knowledge disappeared?
│   ├── why runs differ?
│   └── what does the team know?
│
P1/P2 — Prove product value
│
├── Experiment 9 (experience understanding, not just event model)
├── Sandbox matrix (minimal security acceptance)
├── Read-after-write probes
│
P2 — Economics / scale
│
└── Cost accounting  [x] done

Research — Experience Priming
│
├── broad retrieval → patterns → cues pipeline
├── acceptance corpus (baseline / RAG / priming / priming+JIT)
└── metric: useful experience per context token
```

---

# 6. Что сознательно НЕ делать

-   универсальный knowledge graph;
-   embeddings-инфраструктуру;
-   graph DB;
-   автокластеризацию как источник визуальных групп;
-   новый runtime;
-   «ещё один debugger» / generic event viewer;
-   сложный visual editor;
-   marketplace skills;
-   scheduling subsystem (оставить Temporal).

---

# 7. Что должно быть готово перед приглашением первых пользователей

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

---

# 8. Главный стратегический вывод

Сейчас Temporality находится в переходной точке:

``` text
Phase 2 (validation) → Phase 3 (product)
```

Phase 2 доказала: event model корректен, kernel работает, knowledge
lifecycle воспроизводим, reconciliation семантика верна.

Phase 3 должна доказать: **живой человек может понять эволюцию опыта
агента через UI и использовать это знание для улучшения агента**.

Experience Priming — исследовательская гипотеза, которая может стать
ключевым дифференциатором Temporality. Если priming действительно
позволяет агенту получать больше useful experience per context token,
это фундаментально меняет ценность продукта.

При этом priming не должен быть blocker'ом для базового продукта.
Сначала — usable experience investigation, потом — priming как
следующий уровень.