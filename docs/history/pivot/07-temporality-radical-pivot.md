# Temporality — Radical Pivot: Adaptive Experience Memory Layer

## 0. Статус

Это радикальный pivot проекта Temporality.

Предыдущая гипотеза о построении нового agent runtime / FRP runtime больше не является целью. Не нужно заменять существующий harness, его event loop, planner, tool execution или orchestration.

Новая цель:

> Построить внешний к существующему agent harness слой адаптивной эпизодической памяти, который умеет находить релевантный прошлый опыт, оценивать его актуальность во времени, своевременно подсказывать его агенту и изменять уверенность в памяти на основании результата последующего действия.

Ключевая гипотеза:

> Проблема долговременной памяти агента состоит не столько в хранении знаний, сколько в своевременной активации релевантного прошлого опыта в момент принятия решения.

---

# 1. Что НЕ строим

В этой версии проекта запрещено превращать Temporality в новый runtime.

Не делать:

- новый agent loop;
- новый planner;
- новую модель рассуждения;
- замену существующего harness;
- обязательный FRP protocol;
- собственный orchestration framework;
- сложный knowledge graph ради самого графа;
- новый формат tool execution;
- fine-tuning модели;
- обязательную генерацию permanent/pinned skills;
- изменение основной логики агента.

Если существующий harness уже умеет запускать агента, вызывать tools и хранить историю — Temporality должен подключаться к нему сбоку.

---

# 2. Основная модель

Система работает как observer + memory activator.

```text
                    existing agent harness
                           │
                           │ events / context
                           ▼
                 ┌─────────────────────┐
                 │ Experience Recorder │
                 └──────────┬──────────┘
                            │
                            ▼
                 ┌─────────────────────┐
                 │  Experience Memory  │
                 │ episodes / claims   │
                 │ evidence / confidence
                 │ temporal state      │
                 └──────────┬──────────┘
                            │
                    current context
                            │
                            ▼
                 ┌─────────────────────┐
                 │ Semantic Retrieval  │
                 └──────────┬──────────┘
                            │
                            ▼
                 ┌─────────────────────┐
                 │ Temporal Ranking    │
                 └──────────┬──────────┘
                            │
                            ▼
                 ┌─────────────────────┐
                 │ Memory Activation   │
                 │ / Hint Injection    │
                 └──────────┬──────────┘
                            │
                            ▼
                    existing agent
                            │
                         tool call
                            │
                            ▼
                    tool observation
                            │
                            ▼
                 ┌─────────────────────┐
                 │ Outcome Evaluation  │
                 └──────────┬──────────┘
                            │
                 ┌──────────┴──────────┐
                 ▼                     ▼
             reinforce              weaken
```

Главный принцип:

> Memory activation не принимает решение вместо агента. Она только предоставляет агенту релевантный прошлый опыт в подходящий момент.

---

# 3. Семантическая близость должна быть первым фильтром

Перед началом работы из постановки задачи необходимо определить предметную область / scope текущей работы.

Примеры:

```text
repository = directum-omni
area = Matrix
component = Synapse
problem = authentication
```

или:

```text
product = RX
module = business processes
entity = document
operation = approval
```

или:

```text
domain = agent memory
topic = episodic retrieval
```

Scope должен использоваться для поиска памяти.

Важно:

> Сначала определить, о чём текущая работа. Только потом выбирать актуальный опыт внутри этого semantic scope.

Не использовать глобальную recency как основной retrieval criterion.

---

# 4. Временная актуальность — второй этап

После semantic retrieval может существовать несколько подходящих memory assets, созданных в разные моменты.

Пример:

```text
2025-11:
API X + PAT → 401 → OAuth → success

2026-02:
API X + PAT → 403 → OAuth → success

2026-07:
API X + PAT → 401 → OAuth → success

2026-09:
API X + PAT → success
environment = staging
```

Все они семантически близки.

Но их актуальность различна.

Temporal ranking должен учитывать не только возраст:

- возраст наблюдения;
- дату последнего подтверждения;
- версию продукта/API;
- environment;
- repository state;
- количество подтверждений;
- количество противоречий;
- confidence;
- outcome;
- контекст, в котором наблюдение было получено.

Важно:

> `recency` не равно `relevance`.

Старое, но многократно подтверждённое знание может быть полезнее свежего единичного наблюдения.

---

# 5. Experience вместо простых facts

Основной объект памяти — не только факт.

Предпочтительная структура:

```text
observation
    ↓
hypothesis / decision
    ↓
tool/action
    ↓
result
    ↓
next decision
    ↓
success / failure
```

Например:

```text
API X
  PAT authentication
      ↓
  401
      ↓
  switch to OAuth
      ↓
  success
```

Это должно позволять отвечать на вопрос:

> «Я уже находился в похожей ситуации? Что я тогда сделал и чем это закончилось?»

а не только:

> «Что я знаю про API X?»

---

# 6. Memory asset

Минимальная модель:

```text
MemoryAsset {
    id
    scope
    proposition / experience
    evidence[]
    confidence
    created_at
    last_confirmed_at
    last_contradicted_at

    confirmation_count
    contradiction_count

    environment
    version_context

    source_sessions[]
}
```

Допускается расширять модель, но не превращать её в сложную онтологию без доказанной необходимости.

---

# 7. Динамическая уверенность

У memory asset должна быть изменяемая confidence.

Упрощённая модель:

```text
successful reuse      → confidence ↑
successful confirmation → confidence ↑
failed reuse          → confidence ↓
direct contradiction  → confidence ↓↓
```

Усиление должно зависеть от независимости наблюдений.

20 одинаковых повторений одного и того же сценария не должны иметь такую же доказательную силу, как подтверждения в разных сессиях / контекстах.

Необходимо хранить evidence, позволяющий объяснить:

> почему confidence сейчас именно такой.

---

# 8. Contradiction и forgetting

Критически важно не только усиливать память, но и быстро ослаблять её при изменении реальности.

Например:

```text
15 успешных подтверждений
       ↓
confidence = high
       ↓
новая версия API
       ↓
3 последовательных противоречия
       ↓
confidence резко падает
```

Не обязательно удалять память физически.

Предпочтительнее:

```text
ACTIVE
  ↓
STALE / LOW_CONFIDENCE
  ↓
ARCHIVED
```

Так сохраняется история эволюции знания.

Противоречие должно быть сильнее обычного temporal decay.

---

# 9. Memory activation

Основной output системы — короткая подсказка, а не большой документ.

Пример:

```text
Relevant prior experience:

Repository X:
password authentication failed in 8 previous sessions.
SSH key authentication succeeded in 8/8.
Most recent confirmation: 2 days ago.
```

Нельзя автоматически отправлять агенту большие объёмы старой истории.

Цель:

> минимальный контекст с максимальной полезностью.

---

# 10. Две точки активации

## 10.1. Startup / task-start activation

Когда начинается задача:

```text
task
  ↓
semantic scope
  ↓
retrieve relevant experience
  ↓
temporal ranking
  ↓
small memory hint
  ↓
agent starts work
```

## 10.2. Pre-action / tool-call activation

Особенно важная часть.

Перед выполнением tool call observer должен иметь возможность проверить:

```text
current scope
+
intended action
+
relevant experience
```

Пример:

```text
agent:
call(api.authenticate, method=PAT)
```

Memory layer:

```text
Relevant prior experience:
PAT failed in 8 previous production attempts.
OAuth succeeded in 8/8.
```

Добавляется короткая подсказка в следующий context update.

Агент самостоятельно решает, изменить ли действие.

---

# 11. Repeated failure guardrail

Отдельный механизм должен отслеживать повторение безрезультатных действий.

Пример:

```text
PAT → 401
PAT → 401
PAT → 401
```

После нескольких повторов можно добавить:

```text
You have already tried this approach several times without success.
Prior experience suggests using OAuth instead.
```

Это аналог guardrail, но источник сигнала — longitudinal agent memory.

Важно:

> Это не hard block в первой версии.

Не запрещать tool call.

Сначала только предупреждать и измерять эффект.

---

# 12. Feedback loop

Каждая memory hint должна иметь связь с последующим outcome.

Минимальные события:

```text
RECALL
```

Память была найдена.

```text
INJECT
```

Память была показана агенту.

```text
REUSE
```

Агент использовал рекомендацию / изменил действие в соответствии с ней.

```text
SUCCESS
```

После reuse получен ожидаемый положительный результат.

```text
CONTRADICTION
```

Использование memory recommendation оказалось неверным.

Это позволяет строить цепочку:

```text
memory M42
    ↓
recalled
    ↓
injected
    ↓
agent changed action
    ↓
success
    ↓
confidence ↑
```

или:

```text
memory M42
    ↓
recalled
    ↓
injected
    ↓
agent followed recommendation
    ↓
failure
    ↓
confidence ↓
```

---

# 13. Не делать вид, что причинность доказана

Система должна различать:

```text
memory was shown
```

и:

```text
agent explicitly followed memory
```

и:

```text
following memory preceded success
```

Нельзя автоматически утверждать:

> memory caused success.

Нужно сохранять evidence chain.

---

# 14. Ranking

Реализовать первый простой ranking pipeline:

```text
1. semantic scope filtering
2. semantic similarity
3. contextual compatibility
4. temporal relevance
5. confidence
6. confirmation / contradiction history
7. outcome quality
```

Не пытаться сразу создать идеальную математическую формулу.

На первом этапе ranking должен быть:

- объяснимым;
- детерминированным;
- логируемым;
- легко заменяемым.

Для каждого recall сохранять:

```text
why_selected
```

например:

```text
scope_match = 1.0
semantic_similarity = 0.91
version_match = 1.0
environment_match = 1.0
confidence = 0.94
recency = 0.72
```

---

# 15. Memory hint должен быть маленьким

Ограничить output memory layer.

Ориентир:

- 1–3 relevant memories;
- несколько строк на memory;
- никаких полных chat transcripts;
- никаких огромных graph dumps.

Цель — минимальный контекст с максимальной полезностью.

---

# 16. Что измерять

Главный benchmark не должен проверять качество красивого графа.

Проверять поведение агента.

## Baseline

Обычный harness без Temporality.

## Memory

Harness + memory retrieval, но без автоматической injection.

## Adaptive Memory

Harness + semantic retrieval + temporal ranking + automatic hints.

## Adaptive Memory + Failure Guardrail

Плюс предупреждения при повторении безрезультатных действий.

### Главные метрики

Efficiency:

- total steps;
- tool calls;
- repeated failed tool calls;
- time to successful strategy;
- total tokens.

Memory:

- relevant memories found;
- memories injected;
- memories actually reused;
- successful reuse;
- failed reuse;
- contradiction rate.

Behavioral:

- repeated mistake rate;
- time from first failure to strategy change;
- number of sessions before knowledge becomes reliably reusable.

Memory quality:

- confidence calibration;
- stale memory rate;
- contradiction response time;
- false-positive hint rate;
- irrelevant hint rate.

Главная метрика:

> Сокращает ли система повторение уже известных агенту ошибок без ухудшения успешных сценариев?

---

# 17. Критический эксперимент

Обязательно проверить главный риск проекта:

> Может ли memory injection реально улучшить работу агента, не создавая больше проблем, чем решает?

Минимальные сравнения:

```text
A. no memory
B. semantic memory
C. semantic + temporal memory
D. semantic + temporal + adaptive feedback
```

Особенно важно измерить:

```text
helpful hints
vs
misleading hints
```

Потому что неправильная память потенциально опаснее отсутствия памяти.

---

# 18. Главные риски

## Risk 1 — memory poisoning

Агент сделал неправильный вывод.

Система его сохранила.

Следующая сессия получила неправильную подсказку.

Нужны evidence, contradiction handling и confidence decay.

## Risk 2 — confirmation loop

Агент следует памяти, получает ожидаемый результат и тем самым ещё сильнее укрепляет память, хотя исходное правило было случайным.

Нужны независимые подтверждения и разнообразие контекстов.

## Risk 3 — stale knowledge

Продукт/API/repository изменился.

Нужны temporal/contextual ranking + version/environment awareness.

## Risk 4 — context pollution

Система начинает постоянно вставлять подсказки.

Нужен жёсткий лимит на количество и размер hints.

## Risk 5 — agent ignores hints

Это допустимый результат эксперимента.

Нужно измерять `injected → reused`, а не предполагать reuse.

## Risk 6 — retrieval overhead

Memory system начинает тратить больше ресурсов, чем экономит.

Измерять latency и tokens отдельно.

---

# 19. Критерий успеха pivot

Pivot считается успешным, если на повторяющихся задачах система демонстрирует одновременно:

1. агент чаще избегает ранее совершённых ошибок;
2. уменьшается количество повторных неудачных tool calls;
3. уменьшается время до успешной стратегии;
4. полезные воспоминания автоматически усиливаются;
5. ошибочные воспоминания автоматически ослабевают;
6. количество ложных/нерелевантных подсказок остаётся низким;
7. существующий agent harness практически не меняется.

Если пункт 1 не выполняется — дальнейшее усложнение memory graph не имеет смысла.

---

# 20. Первый implementation milestone

Сделать вертикальный slice:

```text
existing harness
      ↓
capture tool calls + results
      ↓
create episodic memory
      ↓
semantic scope extraction
      ↓
retrieve relevant memories
      ↓
temporal/confidence ranking
      ↓
inject ≤3 short hints
      ↓
capture next outcome
      ↓
reinforce / weaken memory
```

Без FRP.

Без нового runtime.

Без permanent skills.

Без сложного graph UI.

После этого провести baseline-vs-memory эксперимент.

Только если эксперимент показывает измеримое улучшение поведения, развивать:

- graph visualization;
- richer provenance;
- advanced temporal model;
- automatic skill abstraction;
- sophisticated contradiction handling;
- long-horizon observability.

---

# 21. Итоговая формулировка проекта

> **Temporality — adaptive longitudinal memory for AI agents.**
>
> Система наблюдает опыт агента, связывает его с предметной областью, находит семантически релевантный прошлый опыт, ранжирует его по актуальности во времени и автоматически подмешивает небольшие подсказки в нужный момент работы. Результаты последующих действий используются как evidence для усиления или ослабления соответствующих воспоминаний.
>
> Цель — не заставить агента использовать память, а сделать так, чтобы полезный прошлый опыт был доступен ему именно тогда, когда он наиболее нужен.

---

# 22. Принцип реализации

**Do not build the perfect memory system first.**

Сначала доказать одну вещь:

> **Если агент уже однажды решил проблему правильно, может ли Temporality сделать так, чтобы в похожей ситуации через несколько сессий он вспомнил это до того, как повторит старую ошибку?**

Если ответ `yes` — проект имеет основание для дальнейшего развития.

Если `no` — не усложнять архитектуру.