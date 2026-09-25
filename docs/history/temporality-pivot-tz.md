# ТЗ: Pivot Temporality — память как история исследования агента

## 0. Назначение

Это ТЗ фиксирует смену фокуса проекта Temporality.

Не требуется переписывать FRP M0–M16 и не требуется строить новый runtime.

Цель pivot — проверить исходную идею проекта:

> Агенту нужна не просто память о фактах, а память о том, **как эти факты были получены, проверены, опровергнуты и как на их основании менялось внимание агента**.

Temporality должен сделать эту историю:
- пригодной для повторного использования следующим эпизодом;
- пригодной для навигации самим агентом;
- пригодной для наблюдения человеком;
- пригодной для replay/debugging.

Главный исследовательский вопрос:

> **Что именно должен помнить агент о прошлом исследовании, чтобы прошлый опыт помогал ему, а не мешал?**

---

# 1. Что НЕ является целью

В рамках pivot не нужно:

- создавать новый M17;
- переписывать существующий runtime;
- добавлять новый orchestration framework;
- строить отдельную temporal database;
- строить Neo4j;
- добавлять Kafka/Redpanda/Redis/Temporal/LangGraph/LangChain;
- реализовывать сложную математическую модель temporal-semantic retrieval;
- оптимизировать токены до проведения основного эксперимента;
- превращать Frame в универсальную модель состояния всего runtime;
- делать из проекта очередную систему vector search.

Существующие M0–M16 считаются инфраструктурой, на которой проводится эксперимент.

---

# 2. Новая формулировка идеи

Текущее представление памяти:

```text
claim → факт
```

Нужное представление:

```text
наблюдение
    ↓
гипотеза
    ↓
проверка
    ↓
подтверждена / опровергнута
    ↓
изменение фокуса
    ↓
действие
```

То есть память должна хранить не только **что агент знает**, но и **историю формирования знания**.

Пример:

```text
10:01
run_tests
    ↓
тест series.Accumulate падает
    ↓
10:02
гипотеза: int32 overflow
    ↓
10:03
прочитан series.go
    ↓
10:04
гипотеза подтверждена
    ↓
10:05
patch
```

Другой пример:

```text
10:05
наблюдение: internal/reports содержит много похожих тестов
    ↓
гипотеза: проблема находится здесь
    ↓
10:06
проверка
    ↓
10:07
ОПРОВЕРГНУТО
    ↓
10:08
внимание переключено на internal/util
```

Второй граф принципиально отличается от первого.

Для будущего агента:

```text
"internal/reports содержит много тестов"
```

— полезный факт.

Но:

```text
"internal/reports подозревался как источник проблемы,
но гипотеза была проверена и опровергнута"
```

— гораздо более полезное знание.

---

# 3. Главная проблема текущей реализации

Последний longitudinal benchmark показал:

- A-fresh не решила первую задачу;
- B-warm получила 9–13 claims из прошлого эпизода на каждом шаге;
- механическая доставка памяти работала;
- warm-агент явно не использовал эти claims;
- одна неподтверждённая гипотеза из A с confidence 0.85 направила B в decoy-каталог `internal/reports`;
- procedure из неудачного эпизода корректно была отфильтрована как анти-паттерн;
- B-warm потратила 20 шагов и не решила задачу;
- C-control решила задачу за 10 шагов.

Следовательно, проблема сейчас не в том, что память невозможно доставить.

Проблема:

> **В памяти смешаны факты мира и эпизодные гипотезы, а история проверки этих гипотез теряется.**

Текущий pipeline фактически делает:

```text
плохое исследование
    ↓
claims
    ↓
confidence
    ↓
world_memory
    ↓
следующий агент
```

Нужен:

```text
исследование
    ↓
наблюдения
    ↓
гипотезы
    ↓
evidence
    ↓
verification
    ↓
knowledge state
    ↓
следующий агент
```

Источник этого вывода: longitudinal benchmark от 2026-09-18. Особенно важны разделы 10–17 отчёта.

---

# 4. Основное изменение модели памяти

## 4.1. Claim не должен существовать в изоляции

Claim должен позволять ответить:

1. Что утверждается?
2. Когда это было установлено?
3. На основании какого наблюдения?
4. Кто/какой эпизод это установил?
5. Как проверяли?
6. Было ли подтверждение?
7. Было ли опровержение?
8. Какая версия гипотезы существовала до проверки?
9. Что произошло с вниманием агента после проверки?

Минимальная логическая цепочка:

```text
Observation
    ↓
Hypothesis / Claim
    ↓
Evidence
    ↓
Verification
    ↓
Claim status
```

---

# 5. Статусы знания

Необходимо различать как минимум:

```text
OBSERVED
HYPOTHESIS
CONFIRMED
REFUTED
SUPERSEDED
```

### OBSERVED

Непосредственно наблюдаемое утверждение.

Пример:

```text
go test сообщает failure в series_test.go
```

### HYPOTHESIS

Предположение агента.

Пример:

```text
series.Accumulate, вероятно, переполняет int32
```

### CONFIRMED

Гипотеза получила подтверждение через evidence/execution.

Пример:

```text
прочитан код + воспроизведён сценарий →
int32 overflow подтверждён
```

### REFUTED

Гипотеза была проверена и опровергнута.

Пример:

```text
internal/reports является источником failure
→ проверено
→ тесты там зелёные
→ гипотеза опровергнута
```

### SUPERSEDED

Знание заменено более новой версией.

---

# 6. Происхождение знания

Для каждого значимого Claim должна быть доступна provenance chain:

```text
claim
  ↓
observation
  ↓
execution / tool result
  ↓
frame
  ↓
episode
  ↓
timestamp
```

Необходимо иметь возможность перейти от утверждения к его происхождению.

Например:

```text
Claim:
  "internal/reports — источник проблемы"

Status:
  REFUTED

Created:
  Episode A
  17:18:03

Evidence:
  run_tests
  read_file internal/reports/desk1.go
  run_tests

Refuted:
  17:18:42

Next focus:
  internal/util
```

---

# 7. История изменения фокуса

Необходимо фиксировать переходы внимания между существенными объектами.

Пример:

```text
series.Accumulate
      ↓
store.Remove
      ↓
internal/reports
      ↓
internal/util/format.go
```

Каждый переход должен иметь причину.

Минимально:

```text
from
to
trigger
evidence
frame_before
frame_after
timestamp
```

Пример:

```text
from: internal/reports
to: internal/util/format.go

reason:
  hypothesis internal/reports refuted

evidence:
  execution.failed / test result

timestamp:
  ...
```

Это одна из центральных частей проекта.

---

# 8. Frame

Frame не расширять до универсального runtime state.

Frame должен оставаться:

> **текущей позицией агента в истории собственного исследования.**

Он должен позволять понять:

```text
что сейчас интересно;
какие гипотезы активны;
какие знания используются;
какие направления уже проверены;
какие направления были отвергнуты;
```

Минимальная концепция:

```text
Frame
├── objective
├── focus
├── active hypotheses
├── relevant confirmed knowledge
├── relevant refuted hypotheses
├── recent evidence
└── current time / episode position
```

Не требуется запихивать всё это непосредственно в сериализованный Frame, если существующая архитектура уже позволяет получить эти данные через projections.

Главное — Frame должен иметь возможность однозначно восстановить текущую позицию.

---

# 9. Memory Delta

Для каждой существенной смены Frame необходимо уметь получить:

```text
что появилось;
что исчезло;
что подтвердилось;
что опровергнуто;
что стало новым фокусом;
что перестало быть фокусом.
```

Пример:

```text
Frame 41 → Frame 42

ADDED
  hypothesis: parse.ParseAmount loses last digit

CONFIRMED
  format.FormatID byte order

REFUTED
  internal/reports is source of failure

FOCUS CHANGED
  internal/reports
      →
  internal/util
```

Это называется `Memory Delta`.

---

# 10. Доставка памяти следующему эпизоду

Текущая механика:

```text
ListClaimsByWorld
    ↓
top claims by confidence
    ↓
world_memory
```

Необходимо изменить её так, чтобы агент получал не только proposition, но и состояние знания.

Пример:

```text
CONFIRMED
  series.Accumulate uses int32

HYPOTHESIS
  store.Remove may leave stale count
  evidence: ...

REFUTED
  internal/reports is source of failing tests
  evidence: ...
```

Особенно важно:

> Нельзя выдавать REFUTED гипотезу как обычный факт.

Она может быть показана агенту как часть истории:

```text
Ранее проверено:
  internal/reports
Результат:
  гипотеза опровергнута
```

Это может предотвратить повторное исследование.

---

# 11. Правило качества памяти

Неудачный эпизод не должен автоматически становиться качественной памятью.

Разделить:

```text
world knowledge
```

и

```text
episode experience
```

### World knowledge

Факты, подтверждённые evidence.

### Episode experience

То, что агент предполагал или делал.

Episode experience может быть очень ценным даже после провала:

```text
"мы уже проверяли этот путь"
"эта гипотеза была опровергнута"
"этот тест не объясняет failure"
```

Но это должно сохраняться именно как история опыта, а не как подтверждённый факт мира.

---

# 12. Что должен видеть агент

Нельзя снова делать огромный `world_memory` из 24 claims.

Память должна быть компактной и структурированной.

Например:

```text
PREVIOUSLY CONFIRMED
- series.Accumulate uses int32
- store.Remove is soft-delete

PREVIOUSLY REFUTED
- internal/reports is source of test failures

PREVIOUS INVESTIGATION
- series.go already inspected
- store.go already inspected
- internal/reports already checked

CURRENTLY RELEVANT
- internal/util
```

Это пример структуры, а не требование к конкретному формату.

Существующий render pipeline использовать максимально, не создавая параллельный pipeline.

---

# 13. Визуализация

Главный UI-эксперимент — показать историю исследования.

Не таблицу claims.

Не только timeline событий.

А траекторию:

```text
                         TIME →

series.go       ●────────●───────────────●
                │        │               │
             found    tested          fixed
                         │
reports.go      ●────────X
                         │
                      refuted

parse.go        ────────────────────────●
                                         │
                                      current
                                      focus
```

Для каждого узла доступны:

- объект;
- claim;
- статус;
- evidence;
- timestamp;
- episode;
- Frame;
- причина перехода.

---

# 14. Cognitive Debugger

Debugger должен позволять человеку отвечать на вопросы:

### Что агент знал в момент T?

Показать только знания, существовавшие к этому Frame.

### Почему агент пошёл в этот файл?

Показать:

```text
current focus
+
claim
+
evidence
+
предыдущий Frame
```

### Почему агент перестал рассматривать этот путь?

Показать:

```text
hypothesis → verification → REFUTED
```

### Что агент уже проверял?

Показать историю investigation.

### Какие знания пришли из прошлого эпизода?

Показать их происхождение.

### Что изменилось между двумя Frame?

Показать Memory Delta.

### Где появилась ошибочная гипотеза?

Найти первый Frame, в котором она возникла.

### Почему следующему агенту было показано это знание?

Показать provenance и механизм доставки.

---

# 15. Основной benchmark

Новый benchmark должен проверять не «работает ли vector retrieval», а **полезность истории исследования**.

## Сценарий

Использовать один репозиторий.

### Episode A

Агент успешно исследует и исправляет задачу A.

В процессе он:

- находит несколько кандидатов;
- проверяет минимум один ложный кандидат;
- подтверждает правильный кандидат;
- меняет фокус;
- делает исправление.

### Episode B

На том же репозитории появляется задача B.

Часть структуры пересекается с A.

Некоторые ранее проверенные пути являются ложными кандидатами.

Некоторые знания из A действительно полезны.

---

# 16. Четыре режима

Сравнить:

### A. Classic / cold

Обычный harness.

Никакой памяти Episode A.

### B. FRP / cold

FRP без памяти Episode A.

### C. FRP / warm facts

Episode B получает только подтверждённые claims из A.

### D. FRP / warm investigation

Episode B получает:

- подтверждённые знания;
- опровергнутые гипотезы;
- историю уже проверенных объектов;
- provenance;
- Memory Delta / investigation history.

Главное сравнение:

```text
C vs D
```

Если D работает лучше C, мы проверяем именно ценность **истории исследования**, а не просто наличие facts.

---

# 17. Метрики benchmark

Основные:

- task success;
- steps;
- total tokens;
- wall-clock;
- number of file reads;
- repeated reads;
- number of tool calls;
- number of wrong-path investigations;
- number of already-refuted paths revisited;
- number of useful memories reused;
- number of harmful memories followed.

Особенно важные метрики:

```text
repeated investigation
```

и

```text
refuted hypothesis revisits
```

Например:

```text
Episode A:
reports.go → hypothesis → refuted

Episode B:
reports.go visited again
```

Это прямой показатель потери истории.

---

# 18. Отдельный benchmark на память

Создать искусственно контролируемый набор историй.

Например:

```text
H1:
A → hypothesis X → confirmed

H2:
B → hypothesis Y → refuted

H3:
C → hypothesis Z → confirmed
```

Затем дать агенту задачу:

```text
найди проблему
```

Проверить, выбирает ли он:

```text
X / Z
```

и избегает:

```text
Y
```

Здесь модель cognition можно максимально упростить.

Цель — проверить саму memory representation.

---

# 19. Benchmark на наблюдаемость

Дать человеку 10–20 заранее записанных эпизодов.

Сравнить два интерфейса:

```text
A: обычный transcript + events

B: transcript + trajectory + Frame + Memory Delta
```

Задания:

1. Где возникла ошибочная гипотеза?
2. Почему агент выбрал этот файл?
3. Когда гипотеза была опровергнута?
4. Что агент уже проверял?
5. Что он знал перед конкретным действием?
6. Почему он сменил фокус?
7. Какое знание пришло из предыдущего эпизода?
8. Почему агент повторил исследование?

Метрики:

- время до ответа;
- правильность;
- количество навигационных действий.

---

# 20. Replay

Существующий deterministic replay сохранить.

Replay должен позволять восстановить:

```text
Episode
  ↓
Frame 1
  ↓
Frame 2
  ↓
...
  ↓
Frame N
```

И на любом Frame получить:

```text
knowledge state
investigation history
current focus
memory delta
```

Важно:

> Replay должен показывать не только «что произошло», но и «что агент в этот момент знал».

---

# 21. Версионирование

Для воспроизводимости сохранять версии:

- model;
- provider;
- renderer;
- attention;
- memory projection;
- embedding model, если используется;
- scoring/relevance policy, если используется.

Но не добавлять новые подсистемы только ради этого.

Использовать существующую инфраструктуру M0–M16.

---

# 22. Порядок реализации

## Этап 1 — archaeology

Перед изменением кода определить существующие реализации:

- Claim;
- ClaimRelation;
- Observation;
- Evidence;
- Frame;
- Event;
- Attention;
- RenderPacket;
- Entity;
- Relation;
- Procedure;
- CognitiveEmission;
- replay;
- Cognitive Debugger.

Ничего не переписывать до понимания существующего пути данных.

Результат:

```text
source event
  ↓
projection
  ↓
claim
  ↓
frame
  ↓
render
  ↓
model
```

с конкретными пакетами/типами/методами текущего репозитория.

---

## Этап 2 — knowledge lifecycle

Добавить или переиспользовать существующую модель:

```text
observation
→ hypothesis
→ evidence
→ confirmed/refuted
```

Главный критерий:

агент должен уметь отличить:

```text
"мы предполагаем X"
```

от

```text
"мы проверили X и получили Y".
```

---

## Этап 3 — investigation history

Зафиксировать:

- какие объекты исследовались;
- какие гипотезы возникали;
- какие проверки проводились;
- какие гипотезы подтверждались;
- какие опровергались;
- как менялся focus.

---

## Этап 4 — warm memory

Изменить доставку памяти между эпизодами.

Не передавать всё подряд.

Минимальный render должен разделять:

```text
CONFIRMED KNOWLEDGE
REFUTED HYPOTHESES
PREVIOUSLY INVESTIGATED
CURRENTLY RELEVANT
```

---

## Этап 5 — visualization

Добавить trajectory + Memory Delta в существующий Cognitive Debugger.

Не делать новый frontend.

---

## Этап 6 — benchmark

Сначала добиться стабильного baseline:

> Episode A должен успешно завершаться.

Только после этого запускать warm/cold comparison.

Это обязательно, поскольку предыдущий эксперимент показал: если Episode A сам проваливается в `run_tests`-loop, память наследует шум вместо опыта. fileciteturn7file0L54-L60

---

# 23. Acceptance criteria

## AC1 — происхождение

Для каждого hypothesis/claim можно определить:

```text
episode
frame
observation
evidence
timestamp
status
```

## AC2 — refutation

Опровергнутая гипотеза не представляется как подтверждённый факт.

## AC3 — investigation history

Для объекта можно ответить:

```text
исследовался ли он раньше?
когда?
в рамках какой гипотезы?
чем закончилось?
```

## AC4 — warm reuse

В Episode B агент получает полезные знания Episode A без replay полного transcript.

## AC5 — harmful memory

Неподтверждённая гипотеза Episode A не должна автоматически превращаться в world fact Episode B.

## AC6 — debugger

Человек может восстановить:

```text
почему агент выбрал направление;
что он знал;
какая гипотеза была активна;
какие доказательства её поддерживали;
почему она была подтверждена/опровергнута;
когда изменился focus.
```

## AC7 — replay

Все перечисленные состояния восстанавливаются deterministic replay.

## AC8 — benchmark

D (`warm investigation`) сравнивается с C (`warm facts`) и cold baseline.

Не требуется заранее считать D «лучшим».

Эксперимент должен установить, есть ли измеримая ценность истории исследования.

---

# 24. Главный критерий успеха проекта

Не:

> FRP использует меньше токенов.

Не:

> FRP делает больше шагов.

Не:

> temporal-semantic scoring имеет лучший Recall@K.

Главный вопрос:

> **Помогает ли память истории исследования агенту не повторять уже пройденный путь, правильно использовать подтверждённые знания и избегать ранее опровергнутых гипотез?**

И второй вопрос:

> **Позволяет ли эта же модель памяти человеку понять траекторию мышления агента?**

Если ответ «да» — у Temporality появляется собственная идея.

Если ответ «нет» — дальнейшее усложнение FRP не имеет смысла.

---

# 25. Итоговая модель Temporality

Проект можно свести к одной цепочке:

```text
WORLD
  ↓
OBSERVATION
  ↓
HYPOTHESIS
  ↓
EVIDENCE
  ↓
CONFIRMED / REFUTED
  ↓
FOCUS CHANGE
  ↓
ACTION
  ↓
NEW OBSERVATION
```

Это не просто лог действий.

Это **история формирования знания**.

Temporality должен хранить эту историю так, чтобы:

```text
AGENT
  └── мог использовать прошлое исследование

HUMAN
  └── мог увидеть прошлое исследование

RUNTIME
  └── мог воспроизвести прошлое исследование
```

Именно это является предметом pivot.
