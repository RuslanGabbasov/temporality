# Разделение Experience, Knowledge, Skills и контекстной эволюции агента

## 1. Цель

Уточнить и зафиксировать в Temporality модель накопления опыта и эволюции агентов.

Ключевая идея:

> **Experience не является памятью агента и не должен превращаться в единую глобальную базу воспоминаний. Experience — это временная доказательная база, из которой могут формироваться Knowledge и предложения по эволюции Skills.**

Агент должен развиваться не за счёт бесконечного увеличения количества воспоминаний, а за счёт **консолидации опыта в устойчивые знания и эволюционирующие процедурные способности (Skills)**.

При этом применимость опыта должна быть контекстной: **организация → организационная единица → проект → задача/run**, а не глобальной по умолчанию.

---

# 2. Основная модель

В системе необходимо явно разделить следующие сущности.

## Experience

Experience — это наблюдаемая история работы агента:

* runs;
* events;
* observations;
* actions;
* outcomes;
* evidence;
* результаты проверок;
* ошибки;
* успешные решения;
* повторяющиеся паттерны.

Experience является временным и может быть большим.

**Experience не должен целиком попадать в execution context агента.**

Experience нужен для:

* анализа;
* forensic/debugging;
* построения Knowledge;
* обнаружения паттернов;
* оценки Skills;
* эволюции Skills;
* доказательства причин изменений поведения агента.

---

## Knowledge

Knowledge — это обобщённый вывод из Experience, который имеет ценность за пределами конкретного run.

Knowledge должен иметь:

* содержание;
* evidence;
* lifecycle;
* степень подтверждённости;
* scope;
* происхождение;
* связи с Experience;
* связи с последующим использованием.

Knowledge не является просто «сохранённым сообщением агента».

Если вывод применим только к конкретному run, он остаётся Experience и не должен автоматически становиться Knowledge.

---

## Skill

Skill — это **процедурная способность агента**, а не разновидность памяти.

Skill отвечает на вопрос:

> «Как агент должен действовать при определённых условиях?»

Skill может развиваться на основании накопленного Experience и Evidence.

Цикл:

```text
Skill vN
    ↓
execution
    ↓
Experience
    ↓
Evidence
    ↓
evaluation
    ↓
┌───────────────┐
│ no change     │
│ или           │
│ evolution     │
└───────┬───────┘
        ↓
Skill vN+1
```

Skill должен быть версионируемым.

Изменение Skill должно иметь возможность быть связано с конкретными Experience/Evidence, которые послужили основанием для изменения.

---

## Hint

Hint — это краткая проекция релевантного Knowledge в execution context конкретного запуска.

Hint не является новой памятью.

Упрощённо:

```text
Experience
    ↓
Knowledge
    ↓
relevance + scope
    ↓
Hint
    ↓
Agent execution
```

Количество Hint должно быть ограниченным.

Необходимо сохранять возможность определить:

* какое Knowledge породило Hint;
* почему оно было выбрано;
* в каком контексте оно было применено;
* оказало ли оно влияние на дальнейшее выполнение.

---

# 3. Главный принцип

В системе должно быть явно закреплено:

```text
Experience ≠ Knowledge
Knowledge ≠ Skill
Skill ≠ Memory
Hint ≠ Memory
```

И также:

```text
Experience → Knowledge
Experience → Skill evolution
Knowledge → Hint
Skill → execution
execution → Experience
```

Таким образом формируется два связанных цикла:

### Цикл знаний

```text
Experience
    ↓
Knowledge
    ↓
Hint
    ↓
Execution
    ↓
Experience
```

### Цикл эволюции навыков

```text
Skill vN
    ↓
Execution
    ↓
Experience
    ↓
Evidence
    ↓
Evaluation
    ↓
Skill proposal
    ↓
Skill vN+1
```

---

# 4. Контекст опыта

Experience, Knowledge и Skills не должны рассматриваться как глобальные по умолчанию.

Необходимо использовать организационный контекст Temporality:

```text
Organization
    ↓
Organization Unit
    ↓
Project
    ↓
Task / Run
```

Scope является не только механизмом доступа.

**Scope определяет область применимости опыта.**

Например:

```text
Organization Knowledge
    ↓
может быть применимо к нескольким проектам

Project Knowledge
    ↓
применимо внутри конкретного проекта

Run Experience
    ↓
относится к конкретному запуску
```

---

# 5. Правило локальности

Новый Experience по умолчанию должен оставаться в том контексте, в котором он возник.

Нельзя автоматически превращать:

```text
Project A experience
```

в:

```text
Organization knowledge
```

только потому, что оно выглядит семантически полезным.

Обобщение должно быть отдельным осмысленным процессом.

Правильная модель:

```text
Project A
    ↓
repeated evidence
    ↓
Project Knowledge
    ↓
evidence across contexts
    ↓
Organization Knowledge
```

То есть:

> **Сначала локальность, затем доказанное обобщение.**

Необходимо избегать автоматического глобального распространения проектных знаний.

---

# 6. Приоритет контекста над семантическим сходством

При выборе Knowledge для нового запуска нельзя руководствоваться только semantic similarity.

Например, Knowledge из:

```text
Project B
```

может быть очень похожим на задачу:

```text
Project A
```

но это не означает, что его следует передавать агенту Project A.

Приоритет должен быть примерно таким:

```text
scope
    ↓
applicability
    ↓
lifecycle / validity
    ↓
evidence
    ↓
relevance
```

Semantic similarity является только одним из факторов отбора.

---

# 7. Experience не должен превращаться в prompt

Нельзя реализовывать модель:

```text
1000 runs
    ↓
10000 memories
    ↓
prompt
```

Execution context должен формироваться из ограниченного набора наиболее релевантных элементов:

```text
Agent definition
+
relevant Skills
+
relevant Knowledge / Hints
```

Большой исторический Experience Store остаётся за пределами prompt и используется системой отбора и эволюции.

---

# 8. Эволюция Skills

Необходимо связать существующий механизм Living Skills с Experience и Evidence.

Для каждого изменения Skill должна существовать возможность установить:

```text
какой Skill изменился
        ↓
какая версия была до изменения
        ↓
какая версия появилась
        ↓
какие Experience/Evidence послужили основанием
        ↓
какое изменение было сделано
```

Желаемая модель:

```text
Skill v7
    │
    ├── executions
    │
    ├── Experience
    │
    ├── Evidence
    │
    └── Evaluation
            ↓
      evolution proposal
            ↓
         Skill v8
```

Skill evolution должна быть объяснимой и трассируемой.

---

# 9. Не превращать Knowledge в Skill автоматически

Knowledge и Skill имеют разное назначение.

Например:

```text
Knowledge:

"В этом проекте изменение API X также требует
обновления generated client."
```

может существовать как Knowledge.

Если подобное правило многократно подтверждается и становится устойчивой процедурой:

```text
Skill:

"При изменении API contract:
1. обновить schema;
2. regenerate client;
3. запустить integration tests."
```

оно может стать частью Skill.

Таким образом:

> **Knowledge может быть основанием для эволюции Skill, но не каждое Knowledge должно становиться Skill.**

---

# 10. Проверка текущей реализации

Необходимо провести аудит существующей реализации после изменений вокруг commit:

`9117d53842bc479028c047df8c4d8f37ec418e9f`

Особенно проверить:

* Knowledge extraction;
* trajectory extraction;
* deduplication;
* reinforcement;
* correction;
* Knowledge lifecycle;
* Experience Priming;
* scope filtering;
* Hint generation;
* Living Skills;
* skill evolution;
* связь Skill ↔ Experience;
* связь Skill ↔ Evidence;
* связь Agent Version ↔ Skills.

Цель аудита — **не переписать существующую реализацию**, а определить, насколько она уже соответствует описанной модели.

Если текущая реализация уже соответствует требованиям, изменений в коде не требуется.

---

# 11. Отдельно проверить защиту от «помойки знаний»

После текущего автоматического extraction необходимо проверить, что система не начинает создавать Knowledge из любого достаточно содержательного run.

Нужно проверить реальные сценарии:

### Сценарий A — одноразовая задача

Run содержит много технических деталей, но они специфичны только для этой задачи.

Ожидается:

```text
Experience: yes
Knowledge: 0 или минимум
```

### Сценарий B — повторяющаяся закономерность

Несколько запусков обнаруживают одинаковое устойчивое правило.

Ожидается:

```text
Experience
    ↓
Knowledge
    ↓
reinforcement
```

### Сценарий C — Knowledge перестало быть актуальным

Позднейший Experience противоречит Knowledge.

Ожидается:

```text
Knowledge
    ↓
contradiction
    ↓
lifecycle update / invalidation
```

### Сценарий D — проектное Knowledge

Правило действительно для Project A, но не доказано для Project B.

Ожидается:

```text
Project A: available
Project B: not automatically available
```

---

# 12. Acceptance Criteria

Задача считается выполненной, если:

### Модель

* [ ] В документации Temporality явно разделены Experience, Knowledge, Skill и Hint.
* [ ] Зафиксировано, что Skill является процедурной способностью, а не памятью.
* [ ] Зафиксировано, что Experience является источником Evidence для последующей эволюции.

### Контекст

* [ ] Зафиксирована иерархия применимости опыта.
* [ ] Project Experience не становится автоматически Organization Knowledge.
* [ ] Semantic similarity не является достаточным условием для передачи опыта между проектами.

### Knowledge

* [ ] Одноразовые детали конкретной задачи не должны автоматически превращаться в долгоживущее Knowledge.
* [ ] Повторяющиеся и подтверждённые закономерности могут становиться Knowledge.
* [ ] Knowledge имеет provenance и связь с Experience/Evidence.
* [ ] Lifecycle Knowledge учитывает подтверждение и противоречия.

### Skills

* [ ] Skill имеет версии.
* [ ] Изменение Skill связано с Evidence.
* [ ] Можно определить, почему появилась конкретная версия Skill.
* [ ] Skill evolution не требует превращения всего Experience в память агента.
* [ ] Knowledge не превращается автоматически в Skill.

### Execution Context

* [ ] Полный Experience не попадает в prompt.
* [ ] В execution context попадает ограниченный набор релевантных Skills и Knowledge/Hints.
* [ ] Отбор учитывает scope и applicability до semantic similarity.

### Наблюдаемость

Для конкретного Skill evolution должна быть восстанавливаема цепочка:

```text
Skill vN
    ↓
execution
    ↓
Experience
    ↓
Evidence
    ↓
evaluation
    ↓
proposal
    ↓
Skill vN+1
```

Для Knowledge:

```text
Experience
    ↓
Knowledge
    ↓
scope
    ↓
Hint
    ↓
execution
    ↓
outcome
```

---

# 13. Что НЕ нужно делать в рамках задачи

Не делать:

* новую глобальную Memory DB;
* универсальный Knowledge Graph;
* embeddings только ради этой задачи;
* автоматическое распространение всех Knowledge на всю организацию;
* автоматическое превращение Knowledge в Skills;
* загрузку большого количества Experience в prompt;
* новую систему retrieval поверх существующей;
* отдельный runtime для Skills;
* масштабный редизайн Experience Timeline;
* новую модель RBAC, если существующая организационная модель уже позволяет выразить требуемый scope.

**Главная задача — выровнять существующую архитектуру с этой моделью, а не добавить ещё один слой абстракций.**

---

# 14. Итоговая концепция

Temporality должна двигаться к модели:

```text
                 ┌──────────────────┐
                 │      Skill       │
                 │      vN          │
                 └────────┬─────────┘
                          │
                      execution
                          │
                          ▼
                 ┌──────────────────┐
                 │    Experience    │
                 │  temporal facts  │
                 └───────┬──────────┘
                         │
              ┌──────────┴──────────┐
              ▼                     ▼
        ┌───────────┐        ┌──────────────┐
        │ Knowledge │        │   Evidence   │
        └─────┬─────┘        └──────┬───────┘
              │                     │
              ▼                     ▼
           Context              Evaluation
              │                     │
              ▼                     ▼
           Hints              Skill Proposal
              │                     │
              │                     ▼
              │              ┌──────────────┐
              │              │   Skill vN+1 │
              │              └──────────────┘
              │
              ▼
          next execution
```

**Ключевой принцип:**

> **Temporality не должна помогать агенту помнить всё, что с ним происходило. Она должна помогать агенту превращать накопленный контекстный опыт в устойчивые, проверяемые и эволюционирующие способности.**

При этом Experience остаётся богатым историческим слоем, Knowledge — контекстным результатом обобщения, а Skill — компактной исполняемой формой накопленного опыта.
