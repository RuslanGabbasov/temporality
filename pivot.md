# ТЗ: возвращение FRP к исходной гипотезе — temporal-semantic memory и когнитивная наблюдаемость

## 1. Цель

Перенастроить текущий FRP-прототип с гипотезы «новый runtime должен быть эффективнее классического agent harness» на исходную гипотезу:

> **Память агента должна быть навигируемым temporal-semantic пространством, где релевантность определяется одновременно семантической и временной близостью, а наблюдатель может видеть изменение этого пространства и траекторию cognition агента.**

Runtime, Frame, Attention, Render и Cognitive Debugger являются средствами реализации этой гипотезы, а не самостоятельными целями.

Текущее состояние уже содержит Event Log, Claims, Frame/Reducer, Render, Attention, Affordance/Executor, Time Travel, Fork/A-B, Procedures и Cognitive Debugger. Не переписывать эти подсистемы с нуля. Модифицировать их так, чтобы главным объектом эксперимента стала память и навигация по ней.

Основание для работ: текущий benchmark показал, что свежая задача в полном FRP существенно дороже классического harness по токенам и времени, а Claims/Procedures фактически не переиспользуются между эпизодами. При этом audit/replay/debugging уже работают. Поэтому необходимо измерить исходную гипотезу отдельно от стоимости полного runtime.

---

## 2. Главные исследовательские вопросы

### Q1. Temporal-semantic retrieval

Может ли модель быстрее находить релевантные элементы памяти, если retrieval учитывает одновременно:

- semantic proximity;
- temporal proximity;
- graph proximity;
- текущий Frame/focus?

### Q2. Memory reuse

Дает ли накопленная карта мира измеримый выигрыш на втором и последующих эпизодах в том же мире?

### Q3. Cognitive navigation

Помогает ли Frame как компактная точка навигации уменьшить объём контекста по сравнению с полным RenderPacket?

### Q4. Observability

Позволяет ли визуализация temporal-semantic memory и траектории Frame существенно быстрее понять:

- что модель знала;
- что она считала релевантным;
- куда она перемещала внимание;
- какие гипотезы возникли;
- когда возникла ошибка;
- какую информацию модель имела, но не использовала?

### Q5. Runtime overhead

Какую минимальную стоимость добавляет Frame + memory projection + audit по сравнению с обычным native-tool harness?

---

# 3. Архитектурный принцип

Целевой цикл:

```text
                         DURABLE MEMORY
                    ┌─────────────────────┐
                    │ events              │
                    │ claims              │
                    │ entities            │
                    │ relations           │
                    │ embeddings          │
                    │ procedures         │
                    └──────────┬──────────┘
                               │
                   semantic + temporal query
                               │
                               ▼
                         ┌───────────┐
                         │   FRAME   │
                         │           │
                         │ focus     │
                         │ attention │
                         │ time      │
                         │ objective │
                         └─────┬─────┘
                               │
                               ▼
                         LOCAL MEMORY VIEW
                               │
                               ▼
                         MODEL CONTEXT
                               │
                               ▼
                         MODEL EMISSION
                               │
                    ┌──────────┴──────────┐
                    ▼                     ▼
                  ACTION              MEMORY EVENT
                    │                     │
                    ▼                     │
                  WORLD ──────────────────┘

Ключевой принцип:

Render должен быть локальным представлением памяти вокруг Frame, а не сериализацией всей текущей истории.

4. Что считать памятью

Память должна существовать независимо от Episode.

4.1. Durable memory

Минимальные типы:

Observation
Claim
Entity
Relation
Execution
Procedure
Event

Episode — это история конкретного процесса работы, но не граница долговременной памяти.

4.2. Provenance

Каждый durable memory object должен сохранять:

источник;
event/episode provenance;
timestamp;
validity interval, если применимо;
confidence;
relation to conflicting/superseding knowledge.

Нельзя превращать raw observation непосредственно в высокоуверенное знание без provenance.

5. Temporal-semantic memory space

Для каждого memory item необходимо иметь возможность вычислить координаты относительно текущего Frame.

5.1. Semantic distance

Использовать существующие embeddings/pgvector.

Минимально:

semantic_distance(item, frame.focus)

где focus может содержать несколько объектов.

Для нескольких focus:

semantic_distance(item) =
    min(distance(item, focus_i))

или другой детерминированный агрегатор, зафиксированный в конфигурации.

5.2. Temporal distance

Минимально:

temporal_distance(item, frame.time_anchor)

Поддержать:

absolute distance;
signed distance;
configurable temporal window.

Важно различать:

время события;
время создания knowledge object;
validity interval знания.
5.3. Graph distance

Использовать существующий граф Entity/Relation/Claim.

Минимум:

graph_distance(item, focus)

с ограниченной глубиной поиска.

Graph proximity не должна заменять semantic/temporal proximity.

5.4. Combined relevance

Ввести отдельную projection/query model:

relevance =
    w_semantic * semantic_score
  + w_temporal * temporal_score
  + w_graph    * graph_score
  + w_recency  * recency_score
  + w_trust    * trust_score
  + w_task     * task_score

Весовые коэффициенты должны быть:

явными;
конфигурируемыми;
versioned;
записываемыми в benchmark manifest.

Не считать эту формулу окончательной научной моделью. На первом этапе это измерительный механизм.

6. Frame — минимизировать

Frame не должен содержать весь cognitive state.

Целевая минимальная форма:

{
  "objective": "...",
  "focus": ["claim:...", "entity:..."],
  "attention": ["...", "..."],
  "time_anchor": "...",
  "time_window": "..."
}

Frame должен отвечать только на вопрос:

Где сейчас находится cognition агента в temporal-semantic memory space и на что она направлена?

Frame должен оставаться:

immutable;
content-addressed;
deterministic;
replayable.

Не добавлять в Frame данные, которые можно получить из Memory projection.

7. Memory Query API

Добавить внутренний API:

memory.query(frame, query)

Минимальный запрос:

{
  "focus": ["claim:balance-invariant"],
  "time": {
    "center": "...",
    "before": "15m",
    "after": "15m"
  },
  "semantic_limit": 20,
  "graph_depth": 2,
  "temporal_limit": 50
}

Ответ:

{
  "items": [
    {
      "id": "c7",
      "kind": "claim",
      "semantic_distance": 0.08,
      "temporal_distance": 14,
      "graph_distance": 1,
      "relevance": 0.91,
      "provenance": [...]
    }
  ]
}

Внутри runtime можно использовать полные UUID, но в model-facing representation применять короткие стабильные aliases.

8. Две модели внимания

Сохранить различие:

Deliberate attention

Модель сама меняет focus:

focus(A) -> focus(B)
Ambient attention

Runtime предлагает:

suggested:
  - related claim
  - recent observation
  - previous episode result
  - nearby graph node

Ambient suggestions не должны автоматически становиться Frame.

Каждое suggestion должно иметь:

score;
причины попадания;
semantic distance;
temporal distance;
graph distance;
provenance.
9. Новый компактный Render

Сделать два режима.

9.1. render=compact

Model получает:

IDENTITY
OBJECTIVE

CURRENT FOCUS
  claim:c7 "balance invariant violated"

NEARBY MEMORY
  [c8] semantic .08 / temporal +2m
  [e3] semantic .11 / temporal -1m
  [c2] semantic .16 / temporal -3d

RECENT
  [e12] test failed
  [e13] read transaction.go

AMBIENT SUGGESTIONS
  [c2] previous episode found same invariant

OUTSIDE CURRENT VIEW
  147 memory items
9.2. render=legacy

Сохранить существующий RenderPacket для regression comparison.

Главный benchmark должен сравнивать:

Classic native-tool;
FRP legacy;
FRP compact temporal-semantic.
10. Native tool calling

Убрать JSON-in-text из основного benchmark path.

Использовать native function/tool calling там, где модель/провайдер поддерживает его.

Модель должна разделять:

THINK
  ↓
TOOL CALL
  ↓
RESULT
  ↓
THINK

Не заставлять модель вручную генерировать JSON envelope для каждого действия.

Если native tool calling невозможно для конкретного провайдера, этот прогон помечается как отдельный compatibility benchmark.

11. Inter-episode memory

Это обязательная часть работ.

Сценарий:

Episode 1
  discover repository
  find bug
  fix bug
  record knowledge
       ↓
     MEMORY
       ↓
Episode 2
  same repository
  related task

Episode 2 должен видеть:

claims Episode 1;
relevant observations;
entities;
relations;
procedures;
provenance.

При этом не надо передавать Episode 2 всю историю Episode 1.

Нужно измерить:

memory query → relevant context

вместо:

episode transcript → huge prompt
12. Benchmark suite

Нужны не один, а четыре класса benchmark.

B1. Retrieval benchmark

Цель: проверить саму temporal-semantic модель памяти без стоимости LLM.

Для фиксированных queries иметь ground truth relevant items.

Сравнить:

R0

Semantic-only:

embedding similarity
R1

Temporal-only:

time distance
R2

Semantic + temporal:

combined score
R3

Semantic + temporal + graph.

Метрики:

Recall@5;
Recall@10;
Recall@20;
MRR;
nDCG;
query latency;
number of returned irrelevant items.

Особенно важна ситуация, когда:

семантически похожий item старый и нерелевантный;
семантически умеренно похожий item произошёл непосредственно перед текущим событием;
нужный item находится через graph edge.
13. Benchmark B2 — Fresh coding task

Сохранить текущую fixture example.com/ledger, --scale 8, два скрытых бага, тесты менять нельзя.

Сравнить:

C — Classic

Существующий classic native-tool harness.

F0 — Current FRP

Текущая реализация.

F1 — Compact FRP
compact render;
short aliases;
native tools;
answer только final/on-demand;
calibrated token estimate;
canonical marshal.
F2 — Compact FRP + temporal-semantic memory

То же + новая memory projection.

Минимум:

5 прогонов каждой конфигурации;
одинаковая модель;
одинаковый provider;
одинаковая temperature/reasoning;
одинаковая fixture;
случайное распределение порядка прогонов, если возможно.

Основная статистика:

median;
p25/p75;
total input tokens;
cached input tokens;
uncached input tokens;
output tokens;
wall time;
number of model calls;
number of tool calls;
number of file reads;
repeated reads;
success rate.

Нельзя делать выводы по одному прогону.

14. Benchmark B3 — Warm memory

Это главный эксперимент.

Episode 1

Задача A:

исследовать repository, найти и исправить bug A.

После завершения память сохраняется.

Episode 2

Тот же repository.

Задача B должна быть:

связанной с той же предметной областью;
не идентичной задаче A;
содержащей возможность использовать знания Episode 1.
Episode 3

Связанная задача C.

Сравнить:

Cold Classic
Cold FRP
Warm FRP
Warm FRP + temporal-semantic memory

Метрики:

total tokens;
uncached tokens;
time;
steps;
tool calls;
reads;
repeated reads;
relevant-memory retrievals;
missed relevant memories;
success rate.

Главная метрика:

WarmCost / ColdCost
15. Benchmark B4 — Long horizon

Нужна искусственно/реально расширенная задача на 50+ cognitive steps.

Цель не показать скорость.

Цель:

проверить, сохраняет ли temporal-semantic memory способность агента ориентироваться в мире при росте истории.

Сценарий должен содержать:

несколько гипотез;
ложные leads;
повторные возвращения к старым фактам;
изменения состояния файлов;
минимум несколько временных фаз.

Метрики:

context size;
retrieval quality;
repeated retrieval;
attention collapse;
context overflow;
success;
number of compactions;
recovery after wrong hypothesis.
16. Benchmark B5 — Human observability

Это отдельный benchmark и один из главных.

Создать 10–20 записанных episodes с заранее известными событиями:

observation
→ hypothesis
→ wrong turn
→ discovery
→ correction
→ action
→ result

Дать наблюдателю интерфейс Cognitive Debugger.

Задания:

Где агент впервые сформировал неправильную гипотезу?
Какие факты он уже знал в этот момент?
Какие релевантные факты находились вне текущего focus?
Почему attention перешёл в другой регион?
Когда агент получил доказательство, опровергающее гипотезу?
Что было причиной конкретного tool call?
Что изменилось между двумя Frame?
Что агент знал в момент T, но позже перестал учитывать?

Сравнить два интерфейса:

H0

Обычный transcript/timeline.

H1

Temporal-semantic graph + timeline + Frame trajectory.

Метрики:

time-to-answer;
correctness;
number of navigation actions;
субъективная уверенность;
число просмотренных событий.

Нужна фиксированная инструкция и одинаковые episodes.

17. Cognitive trajectory visualization

Cognitive Debugger должен получить отдельное представление:

                 semantic proximity
                         ↑

              c2 ●
                  ╲
                   ╲
        e7 ●────────◎ Frame 12
                       ╲
                        ● c9
                         ╲
                          ● e15

                         └──────────────→ time

Для каждого Frame показывать:

current focus;
selected memory;
ambient suggestions;
semantic radius;
temporal window;
graph neighbors;
outside-frame count.

При проигрывании episode Frame должен перемещаться по карте.

18. Главное новое представление: Memory Delta

Для каждого перехода:

Frame_t → Frame_t+1

показывать не весь context, а delta:

ADDED
  claim:c7
  event:e12

REMOVED FROM ATTENTION
  entity:transaction

FOCUS CHANGED
  c4 → c7

TIME WINDOW
  ±5m → ±15m

NEW KNOWLEDGE
  claim:c8 supported by e12

Это должно стать основным инструментом объяснения динамики cognition.

19. Memory map API

Добавить API:

GET /episodes/{id}/memory-map?frame={frame_id}

Ответ должен содержать:

nodes;
edges;
semantic coordinates;
temporal coordinates;
focus;
attention;
provenance;
frame;
memory version.

Также:

GET /episodes/{id}/frames/{frame_id}/memory-delta

и:

GET /episodes/{id}/trajectory
20. Replay

Replay должен позволять:

episode
  ↓
Frame_t
  ↓
memory projection
  ↓
visualization

без повторного выполнения внешних действий.

Replay должен использовать сохранённые Results.

Для каждого replay manifest фиксировать:

model;
provider;
renderer version;
attention version;
memory projection version;
embedding version;
scoring configuration.
21. Что НЕ делать

На этом этапе запрещается расширять проект ради расширения.

Не добавлять:

Kafka/Redpanda;
Neo4j;
отдельную vector DB;
Redis;
Temporal;
LangGraph;
LangChain;
сложный distributed event bus;
сложный planner;
multi-agent orchestration;
новые типы cognition без benchmark;
новый DSL для агентов.

PostgreSQL + pgvector достаточно для эксперимента.

22. Сохранение существующего кода

Не удалять существующие M0–M16.

Разделить систему на:

CORE
  Event Log
  Memory
  Frame
  Memory Projection
  Render
  Replay

EXPERIMENTAL
  Attention
  Procedures
  Planner
  Fork/A-B

OBSERVABILITY
  Cognitive Debugger

EXECUTION
  Affordance / Executor

Текущие Claims/Entities/Relations использовать как материал для memory space.

23. Критерии успеха
Gate 1 — Memory retrieval

Temporal + semantic retrieval должен давать статистически измеримое преимущество над semantic-only хотя бы на одном из сценариев:

Recall@k;
MRR;
nDCG;
irrelevant retrievals.

При этом latency должна оставаться практически приемлемой для runtime.

Gate 2 — Warm memory

На связанных задачах:

Warm FRP < Cold FRP

по median total uncached tokens.

Целевой ориентир:

Warm / Cold ≤ 0.7

Этот порог является исследовательским gate, а не заранее доказанным свойством системы.

Gate 3 — Fresh overhead

Compact FRP не должен иметь принципиально больший overhead, чем текущий FRP.

Целевой ориентир:

median Compact FRP ≤ 1.5 × Classic

по total tokens.

Gate 4 — Long horizon

50+ шагов без:

context overflow;
catastrophic attention collapse;
потери ранее найденных критических фактов.
Gate 5 — Human observability

H1 должен уменьшить median time-to-answer по сравнению с H0 на заранее определённом наборе задач.

24. Главный эксперимент

Если времени мало, выполнить только этот эксперимент:

              SAME REPOSITORY
                    │
        ┌───────────┴───────────┐
        │                       │
    Episode 1                Episode 2
    discover                related task
        │                       │
        ▼                       ▼
   MEMORY MAP ───────────────► MEMORY QUERY
        │                       │
        └───────────┬───────────┘
                    ▼
             TEMPORAL +
              SEMANTIC
              RETRIEVAL
                    │
                    ▼
                  FRAME
                    │
                    ▼
              COMPACT CONTEXT
                    │
                    ▼
                 MODEL

Сравнить:

Classic cold
FRP cold
FRP warm
FRP warm + temporal-semantic retrieval

по 5 прогонам.

Если warm episode не получает существенного выигрыша, исходная гипотеза о memory-as-asset требует пересмотра.

25. Ожидаемый результат

По итогам должен появиться не просто benchmark report, а ответ на четыре конкретных вопроса:

Есть ли практическая ценность у temporal-semantic memory?
Нужен ли Frame как отдельная сущность для навигации cognition?
Можно ли сделать локальный memory view дешевле полного RenderPacket?
Действительно ли Cognitive Debugger позволяет увидеть cognition лучше обычного transcript?

Только после ответа на эти вопросы принимать решение о дальнейшем развитии runtime.

26. Новая формулировка проекта

Рабочее описание:

FRP — runtime для навигации агента по temporal-semantic memory space.

Memory хранит не только факты и события, но и их связи во времени и по смыслу.

Frame определяет текущую позицию и фокус cognition.

Renderer строит локальное представление памяти вокруг Frame.

Runtime сохраняет изменения Frame и Memory и делает траекторию воспроизводимой.

Cognitive Debugger визуализирует эту траекторию для человека.

Ключевая формула:

memory = temporal + semantic + graph structure

frame = current position / focus

context = render(memory, frame)

frame' = model(context)

memory' = append(observations, claims, results)

Главная ценность проекта — не новый способ вызвать LLM и не новый tool harness, а новый способ представить, навигировать и наблюдать память агента.
```
