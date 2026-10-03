# Организационная структура и модель доступа Temporality

## 1. Цель

Ввести единую модель организационной иерархии и доступа, на которой строятся видимость, использование и управление всеми сущностями Temporality:

* агенты;
* навыки;
* MCP-серверы;
* провайдеры и модели;
* проекты;
* триггеры;
* пользователи;
* роли;
* политики выполнения;
* human-in-the-loop взаимодействия.

Модель должна поддерживать:

* несколько компаний в одной инсталляции;
* иерархию подразделений;
* наследование ресурсов и политик сверху вниз;
* кросс-функциональные проекты;
* явное членство пользователя в проекте;
* внешних участников;
* автоматические запуски через триггеры;
* отдельную execution identity для автоматических операций;
* обращение агента к человеку через настроенный канал;
* аудит изменений доступа.

Ключевой принцип:

> **Оргструктура определяет область видимости и наследования, а не является единственным механизмом авторизации.**

---

# 2. Проблематика текущей модели

Сейчас в системе сосуществуют три несвязанных механизма:

* глобальные списки агентов, навыков, MCP-серверов и провайдеров;
* `workspace_user.projects` — список проектов в JSONB пользователя;
* `workspace_project.allowed_users` — список пользователей в JSONB проекта.

Это не масштабируется, поскольку отсутствуют:

* компании и подразделения;
* наследование ресурсов;
* единая модель видимости;
* кросс-функциональные области;
* нормальная модель membership проекта;
* разграничение видимости и права использования;
* субъект автоматического запуска;
* execution identity;
* наследуемые политики;
* полноценный аудит изменений доступа.

---

# 3. Основные понятия

## 3.1. Org Unit

Узел организационной структуры.

Примеры:

* компания;
* департамент;
* отдел;
* команда;
* другое организационное подразделение.

Org Unit образуют дерево.

```text
Компания A
├── Разработка
│   ├── Платформа
│   │   ├── Temporality
│   │   └── Infrastructure
│   └── Продукт
└── Качество

Компания B
└── ...
```

---

## 3.2. Resource

Ресурс, которым можно управлять или который может использовать агент:

* Agent;
* Skill;
* MCP Server;
* Provider;
* Trigger;
* Policy;
* другие будущие сущности.

---

## 3.3. Project

Проект не является частью дерева оргструктуры.

Проект может объединять:

* несколько подразделений;
* несколько компаний;
* отдельных пользователей;
* внешних участников.

---

## 3.4. Identity

В системе существуют разные субъекты:

* пользователь;
* автоматический триггер;
* execution identity;
* системный субъект.

Нельзя автоматически приравнивать права одного субъекта к другому.

---

# 4. Дерево организационной структуры

Один корневой узел на инсталляцию.

```text
Organization A
├── Development
│   ├── Platform
│   └── Product
└── QA

Organization B
└── ...
```

### Таблица

```text
org_unit
    id
    parent_id
    kind
    name
    path
    created_at
    updated_at
```

### Поля

```text
id            TEXT PRIMARY KEY

parent_id     TEXT NULL
              REFERENCES org_unit(id)

kind          TEXT
              -- organization | department | team | ...

name          TEXT

path          TEXT
              -- materialized path предков

created_at
updated_at
```

`parent_id = NULL` разрешён только у корневого узла.

Глубина дерева в первой версии — до 7–8 уровней.

---

# 5. Материализованный путь

Для определения ancestor/descendant использовать materialized path.

Пример:

```text
a
a.b
a.b.c
a.b.c.d
```

Для узла `a.b.c.d` предки:

```text
a
a.b
a.b.c
a.b.c.d
```

Перемещение узла требует пересчёта `path` всего поддерева.

Перемещение считается редкой административной операцией.

### Требования

* уникальность идентификаторов;
* отсутствие циклов;
* нельзя удалить узел, содержащий пользователей или ресурсы, без явного решения о переносе;
* нельзя переместить узел под собственного потомка;
* транзакционное изменение всего поддерева.

---

# 6. Наследование

Основной принцип:

> Ресурс, привязанный к Org Unit, доступен этому узлу и всему его поддереву.

Например:

```text
Development
├── Platform
└── Product
```

Агент привязан к `Development`.

Он доступен:

* Development;
* Platform;
* Product;
* всем последующим дочерним узлам.

### Глобальный ресурс

```text
org_unit_id = NULL
```

означает:

> ресурс доступен всей инсталляции.

Это сохраняет обратную совместимость с текущей моделью.

---

# 7. Запрет отрицательного наследования

В первой версии отсутствуют:

* deny;
* explicit deny;
* override;
* исключение дочернего узла.

То есть модель только аддитивная:

```text
родитель
   ↓
поддерево
```

Если ресурс привязан к `Development`, невозможно сделать его доступным `Platform`, но скрытым от `Product`.

Это должно быть явно зафиксировано, чтобы не возникла необходимость реализовывать ACL с отрицательными правилами.

---

# 8. Привязка ресурсов

Распределяемые ресурсы получают:

```text
org_unit_id TEXT NULL REFERENCES org_unit(id)
```

Минимально:

```text
workspace_agent.org_unit_id
workspace_skill.org_unit_id
workspace_mcp_server.org_unit_id
workspace_provider.org_unit_id
org_unit_trigger.org_unit_id
```

В дальнейшем аналогичный механизм должен быть доступен для других распределяемых ресурсов.

---

# 9. Видимость ресурса

Для пользователя `U` ресурс `R` видим, если:

```text
R.org_unit_id IS NULL
```

или:

```text
R.org_unit_id
```

является самим Org Unit пользователя либо одним из его предков.

Иными словами:

```text
resource scope
       ↓
     parent
       ↓
      user
```

---

# 10. Видимость и полномочия — разные понятия

Это принципиальное требование.

Видимость ресурса не означает автоматически право:

* использовать его;
* запускать его;
* изменять его;
* удалять его;
* изменять его привязку;
* выдавать к нему доступ.

Минимально различать:

```text
visibility
use
manage
administer
```

### Пример

Пользователь может видеть Agent Reviewer, но не иметь права:

```text
POST /runs
```

с этим агентом.

Аналогично пользователь может видеть MCP Server, но не иметь права привязать его к своему агенту.

---

# 11. Пользователи

В первой версии пользователь имеет одну основную организационную принадлежность:

```text
workspace_user.org_unit_id
```

Это его `primary_org_unit`.

Права и видимость наследуются от этого узла вверх по дереву.

Например:

```text
Company A
└── Development
    └── Platform
        └── User
```

Пользователь получает область видимости:

```text
Company A
Development
Platform
```

---

# 12. Будущая поддержка нескольких Org Unit

Модель должна быть спроектирована так, чтобы в дальнейшем можно было добавить:

```text
workspace_user_org_unit
    user_id
    org_unit_id
    membership_type
```

без изменения основной модели ресурсов.

Основной `workspace_user.org_unit_id` остаётся primary membership.

Дополнительные memberships нужны для случаев:

* матричной структуры;
* архитектурных комитетов;
* совместных подразделений;
* временного назначения;
* участия в другой компании.

В MVP достаточно одной основной принадлежности.

---

# 13. Роли

Существующие роли:

```text
admin
operator
writer
reader
```

сохраняются.

Но область действия роли должна быть явной.

### Уровни

```text
installation
org_unit
project
```

### Installation role

Действует на всей инсталляции.

Например:

```text
installation admin
```

не ограничивается Org Unit.

### Org Unit role

Действует на Org Unit и его поддерево.

### Project role

Действует только внутри конкретного проекта.

---

# 14. Проекты

Проект не является Org Unit.

Проект может быть связан с несколькими узлами:

```text
workspace_project_org_unit

project_id
org_unit_id

PRIMARY KEY (project_id, org_unit_id)
```

Например:

```text
Project X
├── Company A / Development
├── Company A / QA
└── Company B / Product
```

---

# 15. Доступ к проекту

Проект виден пользователю, если существует пересечение между:

* Org Unit пользователя и его предками;
* Org Unit проекта и их поддеревьями.

Иными словами, если проект связан с:

```text
Company A / Development
```

он доступен пользователям:

```text
Development
Development / Platform
Development / Product
...
```

но не:

```text
Company A / QA
Company B / Development
```

если для них нет отдельной связи.

---

# 16. Явное членство в проекте

Организационной принадлежности недостаточно для всех сценариев.

Добавить нормальную таблицу:

```text
workspace_project_member

project_id
user_id
role
created_at
created_by
```

Явное членство позволяет дать доступ конкретному пользователю независимо от его Org Unit.

Например:

```text
Project X
  ├── Development
  └── explicit member: external consultant
```

Это заменяет:

```text
workspace_project.allowed_users
```

JSONB.

### Правило

Доступ к проекту:

```text
org-unit access
OR
explicit project membership
```

---

# 17. Внешние пользователи

Не создавать для каждого подрядчика искусственный полноценный Org Unit.

Пользователь должен иметь признак:

```text
user_type:
    internal
    external
```

Для внешнего участника предусмотреть membership с:

```text
sponsor_org_unit
expires_at
```

Доступ внешнего пользователя должен иметь возможность автоматически истекать.

В MVP допускается использовать explicit project membership.

Полноценная модель внешних организаций может быть реализована позже.

---

# 18. Триггеры

Trigger — самостоятельная сущность, запускающая Agent Run на основании внешнего события.

Примеры:

* Azure DevOps Pull Request;
* Azure DevOps Work Item;
* MindMap test scenario;
* webhook;
* cron;
* другие источники.

Trigger также должен иметь область видимости:

```text
org_unit_id
```

---

# 19. Trigger не является пользователем

Критически важно разделить:

```text
кто создал trigger
```

и:

```text
от чьего имени выполняется операция
```

Например:

```text
Azure DevOps webhook
       ↓
Trigger
       ↓
Reviewer Agent
       ↓
Execution Identity
       ↓
MCP / Provider / Project
```

Trigger не должен автоматически получать права администратора или права создавшего его пользователя.

---

# 20. Execution Identity

Автоматический Run должен выполняться в контексте отдельной execution identity.

Пример:

```text
CI Reviewer
Test Automation
PR Reviewer
```

Execution identity определяет:

* какие агенты можно запускать;
* какие MCP можно использовать;
* какие проекты доступны;
* какие provider разрешены;
* какие действия допустимы;
* кому разрешено задавать вопросы.

### Главное правило

> Автоматический Run не наследует автоматически права пользователя, создавшего Trigger.

---

# 21. Авторизация Trigger

При создании Trigger необходимо проверить:

* имеет ли пользователь право создать Trigger;
* имеет ли он право использовать выбранного агента;
* имеет ли он право выбрать execution identity;
* имеет ли execution identity доступ к нужным ресурсам;
* имеет ли Trigger доступ к соответствующему проекту.

При каждом автоматическом запуске необходимо повторно проверить актуальность критических разрешений.

---

# 22. Authorization на уровне операции

Фильтрация UI/API недостаточна.

Каждая чувствительная операция должна выполнять authorization check.

Минимально:

```text
GET
POST
PUT
PATCH
DELETE
EXECUTE
BIND
UNBIND
```

Например:

```text
POST /runs
{
    agent_id: ...
}
```

должен проверить:

```text
user
    ↓
can use agent?
    ↓
can use project?
    ↓
can use skills?
    ↓
can use MCP?
    ↓
can use provider?
```

Нельзя полагаться на то, что ресурс уже был отфильтрован в предыдущем GET.

---

# 23. Provider и credentials

Provider нельзя рассматривать просто как ещё один ресурс.

Необходимо разделить:

```text
Provider definition
Credential / Secret
Usage policy
```

Видимость Provider не означает доступ к его credentials.

Нельзя возвращать секреты через обычные API ресурсов.

Использование credentials должно происходить только внутри разрешённого execution context.

---

# 24. Политики

Архитектура должна поддерживать отдельную сущность Policy.

Даже если часть политик реализуется позже, необходимо предусмотреть возможность наследования:

```text
Org Unit
├── Resources
├── Roles
└── Policies
```

Потенциальные политики:

* разрешённые модели;
* разрешённые MCP;
* максимальный бюджет;
* лимит токенов;
* timeout;
* network policy;
* sandbox policy;
* human approval;
* retention;
* разрешённые каналы коммуникации.

Политики наследуются сверху вниз по тем же принципам.

В первой версии допускается реализовать только необходимый минимум.

---

# 25. Human-in-the-loop

Агент может в процессе Run запросить дополнительный контекст у человека.

Для этого используется специальный инструмент:

```text
ask_human(...)
```

Это не сообщение в конкретный чат.

Это системное взаимодействие:

```text
Agent Run
   ↓
Human Request
   ↓
User / Role / Project owner
   ↓
Communication channel
   ↓
Response
   ↓
Agent Run
```

---

# 26. Получатель Human Request

Агент не должен напрямую выбирать транспорт:

```text
Matrix
Telegram
...
```

Агент выбирает логического получателя.

Например:

```text
project_owner
qa_lead
specific_user
```

Система определяет конкретного пользователя и канал.

---

# 27. Канал коммуникации

Настройки пользователя определяют предпочтительный канал.

Например:

```text
User
├── Matrix      preferred
├── Telegram    enabled
└── Email       enabled
```

Система использует:

```text
preferred channel
```

и при необходимости fallback.

Агент не должен зависеть от реализации конкретного транспорта.

---

# 28. Human Request как отдельная сущность

```text
human_request

id
run_id
recipient
question
options
status
channel
created_at
delivered_at
answered_at
expires_at
response
```

Статусы:

```text
pending
delivered
waiting
answered
expired
cancelled
```

---

# 29. Приостановка Run

После `ask_human`:

```text
running
    ↓
waiting_for_human
```

При этом:

* токены не расходуются;
* Run не считается завершённым;
* состояние сохраняется в БД;
* ожидание переживает рестарт;
* обычный execution timeout не должен уничтожать ожидающий Run.

После ответа:

```text
waiting_for_human
    ↓
resuming
    ↓
running
```

---

# 30. Human Request и права

Агент не может произвольно обратиться к любому пользователю.

Необходимо проверить:

* имеет ли execution identity право обращаться к пользователю;
* разрешено ли передавать ему данный контекст;
* относится ли пользователь к проекту/Org Unit;
* разрешён ли соответствующий канал.

В дальнейшем получателем может быть:

```text
user
role
org_unit
project owner
```

---

# 31. Контекст Human Request

Запрос человеку должен быть самодостаточным.

Он должен содержать:

* какую операцию выполняет агент;
* почему возник вопрос;
* что уже известно;
* чего не хватает;
* варианты ответа;
* последствия выбора, если они существенны.

Не следует передавать весь технический контекст Run.

---

# 32. Структурированные ответы

Если вопрос допускает варианты:

```text
options:
    - id
      label
```

Ответ должен сохранять структурированное значение.

Свободный текст также поддерживается.

---

# 33. Таймаут Human Request

Human Request должен иметь timeout.

После истечения система выполняет заданную policy:

* fail;
* retry;
* fallback;
* escalate;
* cancel.

Политика должна быть частью модели выполнения, а не транспортного адаптера.

---

# 34. Изменение оргструктуры во время Run

Изменение Org Unit не должно автоматически уничтожать уже созданный Run.

При создании Run необходимо зафиксировать execution context:

```text
agent
skills
MCPs
provider
project
execution identity
effective policies
```

Это позволяет восстановить контекст, в котором Run был создан.

При этом критические действия внутри Run могут повторно проверять актуальные права.

---

# 35. Snapshot execution context

Run должен позволять ответить на вопрос:

> Почему операция имела право использовать этот ресурс в момент выполнения?

Для этого необходимо сохранять как минимум идентификаторы:

```text
agent_id
skill_ids
mcp_ids
provider_id
project_id
execution_identity_id
policy context
```

Полное копирование ресурсов не требуется.

---

# 36. Аудит

Изменения модели доступа должны иметь отдельный audit trail.

Минимальные события:

```text
org_unit.created
org_unit.updated
org_unit.moved
org_unit.deleted

user.org_unit_changed

resource.bound
resource.unbound

project.org_unit_added
project.org_unit_removed

project.member_added
project.member_removed

role.granted
role.revoked

trigger.created
trigger.updated
trigger.deleted

execution_identity.created
execution_identity.updated
execution_identity.deleted
```

Каждое событие должно содержать:

```text
actor
timestamp
target
old_value
new_value
```

По возможности также:

```text
reason
```

---

# 37. Audit Human-in-the-loop

Human interaction также является частью траектории Run.

Фиксировать:

```text
human.requested
human.delivered
human.response_received
run.resumed
```

Это позволит анализировать:

* сколько Run требуют человека;
* кто чаще всего требуется агентам;
* сколько времени занимает ожидание;
* какие типы решений чаще всего требуют вмешательства;
* где агентам не хватает знаний или навыков.

---

# 38. API организационной структуры

```text
GET    /v1/org/units
POST   /v1/org/units

GET    /v1/org/units/{id}
PUT    /v1/org/units/{id}
DELETE /v1/org/units/{id}

GET    /v1/org/units/{id}/users
GET    /v1/org/units/{id}/resources
GET    /v1/org/units/{id}/projects
```

Изменять дерево может только соответствующий администратор.

---

# 39. API привязки ресурсов

```text
PUT    /v1/org/resources/{kind}/{id}/binding
DELETE /v1/org/resources/{kind}/{id}/binding
```

Пример:

```text
PUT /v1/org/resources/agents/reviewer/binding

{
    "org_unit_id": "development"
}
```

`org_unit_id = null` означает глобальную доступность.

---

# 40. API проектов

Добавить:

```text
GET    /v1/projects/{id}/org-units
PUT    /v1/projects/{id}/org-units
DELETE /v1/projects/{id}/org-units/{unit_id}

GET    /v1/projects/{id}/members
POST   /v1/projects/{id}/members
DELETE /v1/projects/{id}/members/{user_id}
```

Старые:

```text
workspace_project.allowed_users
workspace_user.projects
```

после миграции не используются.

---

# 41. API списков

Существующие API:

```text
GET /v1/workspace/agents
GET /v1/workspace/skills
GET /v1/workspace/mcp-servers
GET /v1/workspace/providers
GET /v1/workspace/projects
GET /v1/workspace/triggers
```

должны возвращать только сущности, доступные вызывающему субъекту.

Для системного runtime/API, работающего с внутренней execution identity, допускается отдельный privileged API.

Важно:

> privileged API не должен использоваться для обхода authorization в пользовательском API.

---

# 42. UI — Организация

Добавить раздел:

```text
Организация
```

Возможности:

* дерево Org Unit;
* создание;
* переименование;
* перемещение;
* удаление;
* инспекция узла;
* пользователи;
* ресурсы;
* проекты;
* политики.

Перемещение должно требовать подтверждения, поскольку оно меняет область видимости.

---

# 43. UI — Ресурсы

В формах:

* Agent;
* Skill;
* MCP;
* Provider;
* Trigger;

добавить:

```text
Доступность
```

Варианты:

```text
Вся организация
[Org Unit]
```

Для администратора отображать эффективную область действия.

Например:

```text
Доступно:
Development
└── 14 дочерних узлов
```

---

# 44. UI — Проект

В проекте:

```text
Организационные области
```

Мультивыбор Org Unit.

Отдельно:

```text
Участники
```

для explicit project membership.

Это две разные модели и не должны объединяться в одно поле.

---

# 45. UI — Пользователь

Показывать:

```text
Основное подразделение
Роли
Проекты
```

Основное подразделение выбирается вместо старого списка проектов.

Проекты показываются как вычисленная область доступа плюс явные memberships.

---

# 46. UI — Trigger

При создании Trigger пользователь должен выбрать:

1. источник события;
2. событие;
3. агента;
4. проект/область;
5. execution identity;
6. policy;
7. правила human-in-the-loop.

Не требовать от пользователя написания YAML для типового сценария.

---

# 47. UI — Execution Identity

Отдельная сущность для автоматических запусков.

Например:

```text
PR Reviewer
Test Automation
Work Item Analyst
```

Показывать:

* какие агенты доступны;
* какие проекты доступны;
* какие MCP доступны;
* какие provider доступны;
* какие human recipients разрешены;
* какие политики применяются.

---

# 48. Миграция

Миграция должна быть постепенной.

## Этап 1

Создать корневой Org Unit:

```text
Organization
```

Все существующие пользователи:

```text
org_unit_id = Organization
```

Все существующие ресурсы:

```text
org_unit_id = NULL
```

Поведение системы не меняется.

---

## Этап 2

Создать новую модель проектов:

```text
workspace_project_org_unit
workspace_project_member
```

Перенести существующие связи.

Старые:

```text
workspace_user.projects
workspace_project.allowed_users
```

пока сохраняются.

---

## Этап 3

Переключить чтение на новую модель.

Старые JSONB-колонки больше не участвуют в authorization.

---

## Этап 4

Провести проверку расхождений:

```text
old access model
vs
new access model
```

До удаления старых полей необходимо убедиться, что новая модель не изменила существующие права неожиданным образом.

---

## Этап 5

После стабилизации удалить:

```text
workspace_user.projects
workspace_project.allowed_users
```

---

# 49. Совместимость

`NULL org_unit_id` у ресурсов сохраняет текущую семантику:

> ресурс глобальный.

Это позволяет постепенно вводить организационное ограничение без обязательной миграции всех существующих ресурсов.

---

# 50. Производительность

Основные authorization-запросы должны быть рассчитаны на выполнение большого количества раз.

Необходимо предусмотреть индексы для:

* `org_unit.parent_id`;
* `org_unit.path`;
* `resource.org_unit_id`;
* `workspace_user.org_unit_id`;
* project/org-unit;
* project/member;
* execution identity/resource bindings.

Не следует выполнять полное построение дерева при каждом tool call.

При необходимости использовать:

* кэш effective access;
* предварительно вычисленные области;
* snapshot контекста Run.

Конкретный механизм оптимизации может быть выбран при реализации после измерений.

---

# 51. Тестирование

Обязательны интеграционные тесты следующих сценариев.

## Иерархия

```text
A
└── B
    └── C
```

Ресурс A:

* виден A;
* виден B;
* виден C.

Ресурс C:

* не виден A;
* не виден B;
* виден C.

---

## Глобальный ресурс

```text
org_unit_id = NULL
```

виден всем.

---

## Кросс-функциональный проект

Проект связан:

```text
Development
QA
Company B / Product
```

Проверить пользователей каждого подразделения.

---

## Explicit project membership

Пользователь вне организационной области проекта получает доступ только после explicit membership.

---

## Роли

Проверить:

* installation admin;
* org-unit admin;
* project writer;
* project reader;
* обычный пользователь.

---

## Trigger

Проверить:

* пользователь может создать разрешённый Trigger;
* пользователь не может создать Trigger с недоступным агентом;
* Trigger не получает права пользователя автоматически;
* execution identity имеет собственные права.

---

## Run

Проверить:

* агент может использовать разрешённые ресурсы;
* агент не может использовать недоступный MCP;
* агент не может использовать недоступный provider;
* Run нельзя создать с недоступным агентом.

---

## Human-in-the-loop

Проверить:

1. агент создаёт Human Request;
2. система определяет получателя;
3. выбирается preferred channel;
4. Run переходит в `waiting_for_human`;
5. ответ сохраняется;
6. Run возобновляется;
7. ответ становится результатом `ask_human`.

---

## Изменение Org Unit

Проверить:

1. пользователь имел доступ к ресурсу;
2. пользователя переместили;
3. effective visibility изменилась;
4. новый Run использует новые права;
5. существующий Run не исчезает;
6. критическое действие корректно проходит актуальную authorization check.

---

# 52. Фазы реализации

## Фаза 1 — Org Model

* `org_unit`;
* materialized path;
* CRUD;
* проверки циклов;
* перемещение;
* привязка пользователей;
* resource `org_unit_id`;
* базовые effective visibility tests.

---

## Фаза 2 — Project Model

* `workspace_project_org_unit`;
* `workspace_project_member`;
* миграция существующих проектов;
* effective project visibility;
* project roles.

---

## Фаза 3 — Authorization

* visibility;
* use;
* manage;
* administer;
* installation/org-unit/project scopes;
* authorization middleware/gates;
* проверки Execute/Bind/Unbind;
* execution identity.

---

## Фаза 4 — Triggers

* Trigger scope;
* execution identity;
* authorization Trigger;
* запуск Run;
* повторная проверка permissions;
* аудит.

---

## Фаза 5 — Human-in-the-loop

* Human Request;
* `ask_human`;
* recipient resolution;
* communication adapters;
* Matrix;
* Telegram;
* preferred channel;
* fallback;
* waiting/resume;
* timeout/escalation.

---

## Фаза 6 — Policies

* наследуемые policies;
* execution limits;
* MCP/model/provider policies;
* human approval policies;
* sandbox/network policies.

---

## Фаза 7 — UI

* Org Tree;
* resource availability;
* project memberships;
* user organization;
* Trigger configuration;
* Execution Identity;
* policies;
* effective access visualization.

---

## Фаза 8 — Migration & Cleanup

* сравнение старой и новой модели;
* миграционные проверки;
* отключение старых JSONB-механизмов;
* удаление legacy-полей;
* документация.

---

# 53. Критерии готовности

Система считается реализованной, если можно воспроизвести следующий сценарий:

```text
Company A
└── Development
    └── Platform
        └── User

Company B
└── QA
```

Создан ресурс:

```text
Reviewer Agent
scope = Company A / Development
```

Пользователь Platform его видит и может использовать.

Пользователь Company B его не видит и не может использовать через прямой API-вызов.

Создан проект:

```text
Omni
├── Company A / Development
└── Company B / QA
```

Пользователь из обеих областей получает доступ.

В проект добавлен внешний пользователь через explicit membership.

Создан Trigger:

```text
Azure DevOps Pull Request
        ↓
Reviewer
        ↓
Execution Identity = PR Reviewer
```

Webhook запускает Run.

Run использует только ресурсы, разрешённые execution identity.

Reviewer обнаруживает недостаток контекста и вызывает:

```text
ask_human
```

Система:

1. определяет допустимого получателя;
2. смотрит настройки пользователя;
3. отправляет запрос через Matrix или Telegram;
4. переводит Run в `waiting_for_human`;
5. получает ответ;
6. возобновляет Run;
7. передаёт ответ агенту;
8. завершает Run.

В Temporality при этом видна единая траектория:

```text
trigger
  ↓
run.created
  ↓
agent execution
  ↓
human.requested
  ↓
human.delivered
  ↓
waiting_for_human
  ↓
human.response_received
  ↓
run.resumed
  ↓
run.completed
```

Все изменения оргструктуры, membership, ролей, bindings, Trigger и execution identity аудируются.

# 54. Архитектурные ограничения первой версии

Чтобы не превратить первую реализацию в полноценную IAM-платформу, явно **не реализовывать**:

* deny rules;
* сложные ACL;
* произвольные пользовательские permission sets;
* полноценную матричную оргструктуру;
* автоматическую синхронизацию с HR;
* SCIM;
* федерацию организаций;
* сложную иерархию внешних компаний;
* автоматический выбор произвольного человека на основании LLM;
* сложные workflow согласований.

При этом архитектура должна оставлять возможность добавить эти возможности позднее без переделки базовой модели.

# 55. Ключевой принцип архитектуры

Все механизмы должны сходиться к одной модели:

```text
                    ┌──────────────┐
                    │  Org Units   │
                    └──────┬───────┘
                           │
             ┌─────────────┼─────────────┐
             ▼             ▼             ▼
         Resources      Projects       Policies
             │             │             │
             └─────────────┼─────────────┘
                           ▼
                         User
                           │
                           ▼
                     Authorization
                           │
             ┌─────────────┴─────────────┐
             ▼                           ▼
        Manual Run                  Trigger Run
                                         │
                                         ▼
                                Execution Identity
                                         │
                                         ▼
                                      Agent Run
                                         │
                              ┌──────────┴──────────┐
                              ▼                     ▼
                           Tools              Human Request
                                                    │
                                                    ▼
                                           Matrix / Telegram / ...
```

**Org Unit определяет область наследования.
Project определяет область совместной работы.
Role определяет полномочия.
Policy определяет ограничения.
Execution Identity определяет права автоматической операции.
Authorization принимает окончательное решение.
Temporality фиксирует контекст и историю этого решения.**
