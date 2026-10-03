# План работ: Оргструктура и модель доступа

Source spec: `docs/org-structure.md` (расширенная версия, 55 разделов / 8 фаз).
Статус: **Фаза 1 завершена** (коммит `0e1ec70`). Остальное — по волнам ниже; порядок согласован с фазами спеки, но UI организации вынесен вперёд (волна A), потому что бэкенд фаз 1–2 уже существует и без UI неюзабелен.

Размеры задач: S / M / L — относительно одной волны работы.

## 0. Текущее состояние (baseline после 0e1ec70)

Готово (Фаза 1 спеки + часть Фазы 2):

- миграция 000034: `org_unit` (дерево, materialized path), `org_unit_id` на agent/skill/mcp-server/provider/user, `workspace_project_org_unit` (со стороны проекта — CASCADE, со стороны узла — RESTRICT за проверкой «пустого узла»);
- стор `workspace/org.go`: CRUD дерева, move с пересчётом путей поддерева, запрет циклов, удаление только пустых узлов, привязка ресурсов, инспекция узла;
- `Principal.VisibleUnits` (цепочка предков) + фильтрация списков и одиночных GET: агенты, навыки, MCP, провайдеры, проекты; admin и kernel-internal токены — без фильтра;
- переходное правило проектов: `org-видимость AND legacy allowed_users`;
- API: `/v1/org/units` CRUD (мутации — admin), `/v1/org/units/{id}/resources`, `PUT /v1/org/resources/{kind}/{id}/binding`;
- обычные PUT ресурсов не сбрасывают привязку (сменить скоуп может только admin через binding); whoami отдаёт `org_unit_id`.

Уже существовало в системе до оргструктуры (релевантно фазам 4–5):

- каналы пользователей + preferred channel (`workspace_user.channels`, `PUT /users/{id}/channels`), notify-адаптеры; web-канал — inbox в UI;
- `ask_human` / `request_approval`: приостановка Run (`waiting`), структурированные опции, timeout, события траектории (`human.requested` и т.д.), возобновление Run;
- триггеры `workspace_trigger` (project-scoped: schedule/webhook/event), инструменты агента `create/update/delete_trigger`;
- Run фиксирует версию агента (миграция 000031).

Известные пробелы против расширенной спеки:

- видимость ≠ право использования: `POST /v1/agent/runs` проверяет роль+проект, но не доступ к агенту/скиллам/MCP/провайдеру (§22);
- нет `workspace_project_member` (explicit membership, §16), нет ролей на узлах (§13), нет execution identity (§20), нет аудита (§36), у триггеров нет `org_unit_id` (§18);
- получатель `ask_human` — конкретный `user:xxx`, нет резолюции role/org_unit/project_owner (§26).

## 1. Карта фаз спеки → волны плана

| Фаза спеки | Содержание | Состояние | Волна |
|---|---|---|---|
| 1 Org Model | дерево, path, CRUD, привязки, visibility-тесты | ✅ готово | — |
| 2 Project Model | project↔units, members, project roles, миграция | частично (units есть) | B |
| 3 Authorization | visibility/use/manage/administer, проверки на операциях, execution identity | минимально | C |
| 4 Triggers | scope триггеров, авторизация, запуск под identity, аудит | не начато | D |
| 5 HITL | human_request, recipient resolution, каналы, timeout-политики | ядро есть | E |
| 6 Policies | наследуемые политики выполнения | не начато | F |
| 7 UI | Org Tree, доступность, membership, identity, политики | не начато | A (частично) + по фазам |
| 8 Migration & Cleanup | расхождение моделей, удаление legacy | переходное правило | G |

## 2. Волны работ

### Волна A. UI организации и доступности — M — фаза 7 (частично)

- [ ] `Org.tsx`: дерево со сворачиванием; инспекция узла (ресурсы по видам, пользователи, проекты); создание/переименование; перемещение выбором родителя с подтверждением («меняет видимость»); удаление пустого узла через наш confirm-диалог.
- [ ] `Layout.tsx`: раздел «Организация» (admin; остальным по ролям на узлах — после волны C).
- [ ] Поле «Доступность» (селект узла, дефолт «Вся организация»; для admin — эффективная область «Development └── 14 дочерних») в формах Agents/Skills/Mcp/Providers.
- [ ] Проект: мультивыбор узлов («Организационные области»), отдельно будущие «Участники»; legacy `allowed_users` скрыть.
- [ ] Пользователь: выбор основного подразделения вместо списка проектов.
- [ ] Карточки списков: бейдж узла / «глобально».
- [ ] Мастер корня: если `org_unit` пуст — создание организации (онбординг/страница «Организация»).
- [ ] `workspaceApi.ts`: `orgApi`; i18n ru/en; обе темы; `ListFilter`.

### Волна B. Project Model — M — фаза 2

- [ ] `workspace_project_member (project_id, user_id, role, created_at, created_by, PK(project_id,user_id))` — миграция 000035.
- [ ] Доступ к проекту = `org-unit access OR explicit membership` (заменяет AND-правило для legacy-части; `allowed_users` замораживается на чтение).
- [ ] API: `GET/POST/DELETE /v1/projects/{id}/members`; `GET/PUT/DELETE /v1/projects/{id}/org-units/{unit_id}`; `DELETE /v1/org/resources/{kind}/{id}/binding` (алиас PUT с "").
- [ ] Аудит-минимум: таблица `access_audit_log` + события `project.member_added/removed`, `resource.bound/unbound`, `org_unit.*` (§36).
- [ ] Внешние пользователи MVP: explicit membership; `user_type` и `expires_at` — опционально здесь или в волне C.
- [ ] UI: участники проекта в диалоге проекта.

### Волна C. Authorization — L — фаза 3

- [ ] Уровни права: `visibility / use / manage / administer` (§10) — модель в controlplane, а не только фильтры списков.
- [ ] Проверки на операциях (§22): `POST /runs` (агент, проект, скиллы, MCP, провайдер), привязка MCP/скилла к агенту, использование провайдера, EXECUTE/BIND/UNBIND.
- [ ] Роли на узлах: `org_unit_role (user_id, org_unit_id, role, granted_by, granted_at)`; эффективная роль = максимум (установка ∪ путь узла); API `PUT/DELETE /v1/org/units/{id}/roles/{userID}`; аудит `role.granted/revoked`.
- [ ] Execution Identity — сущность для автоматических запусков (§20): доступные агенты/MCP/провайдеры/проекты/human-получатели; CRUD + аудит; правило: Run триггера не наследует права создателя.
- [ ] Snapshot execution context (§34–35): в Run фиксируются agent_id/skill_ids/mcp_ids/provider_id/project_id/execution_identity_id/policy context (сейчас только версия агента); изменение оргструктуры не убивает живой Run, критические действия перепроверяют права.
- [ ] Provider credentials (§23): секреты не возвращаются ресурсными API; использование — только в execution context.

### Волна D. Triggers — M — фаза 4

- [ ] `org_unit_id` у триггеров (или `org_unit_trigger` при.departments-сценариях) + видимость в списках.
- [ ] Авторизация создания триггера (§21): право на агента, identity, проект.
- [ ] Запуск Run под execution identity; повторная проверка критических разрешений при каждом запуске.
- [ ] Аудит `trigger.created/updated/deleted`.
- [ ] UI триггера (§46): источник → событие → агент → область → identity → human-правила, без YAML.

### Волна E. HITL — M — фаза 5

- [ ] `human_request` как отдельная сущность (§28: статусы pending/delivered/waiting/answered/expired/cancelled) — поверх существующих событий траектории; UI-список запросов.
- [ ] Recipient resolution (§26): `project_owner / role / org_unit / specific_user`; проверка права обращения (§30).
- [ ] Адаптеры Matrix/Telegram + fallback по preferred channel.
- [ ] Timeout-политики (§33): fail/retry/fallback/escalate/cancel — в модели выполнения, не в транспорте.
- [ ] Аудит HITL (§37).

### Волна F. Policies — M/L — фаза 6

- [ ] Сущность Policy, привязка к узлам, наследование сверху вниз.
- [ ] Минимум: разрешённые модели/MCP, бюджет/токены, timeout, network/sandbox, human approval.
- [ ] UI политик + визуализация эффективного доступа.

### Волна G. Migration & Cleanup — S — фаза 8

- [ ] Отчёт расхождения old/new модели на стенде (этап 4 спеки §48).
- [ ] Отключение чтения `allowed_users` / `workspace_user.projects`; удаление колонок отдельной миграцией.
- [ ] Документация модели доступа (runbook/architecture).

## 3. Тестирование и приёмка (по §51 спеки)

- Иерархия: ресурс A виден A/B/C; ресурс C виден только C.
- Глобальный ресурс (`NULL`) виден всем.
- Кросс-функциональный проект: пользователи всех связанных узлов видят; несвязанные — нет.
- Explicit membership: доступ вне орг-области после добавления.
- Роли: installation admin / org-unit admin / project writer / project reader / обычный пользователь.
- Trigger: разрешённый триггер создаётся; с недоступным агентом — нет; права создателя не наследуются; identity имеет собственные права.
- Run: недоступные агент/MCP/провайдер отклоняются на POST /runs и внутри выполнения.
- HITL: полный цикл ask_human → delivery → waiting → answer → resume.
- Изменение узла пользователя: новый Run по новым правам; живой Run не исчезает; критическое действие перепроверяется.

## 4. Принятые решения

1. **Переходная видимость = org AND legacy** (до волны B): обе модели должны пропустить; без настройки структуры поведение не меняется. В волне B заменяется на `org OR explicit membership`, legacy замораживается.
2. **Пересчёт `path` — в приложении**, одной транзакцией `MoveOrgUnit`, без PL/pgSQL-триггеров.
3. **Мутации дерева и привязок — только admin** (изменение видимости для всех); writer создаёт ресурсы с доступностью «вся организация».
4. **`VisibleUnits = nil` у служебных токенов** — рантайм не зависит от прав пользователя; privileged API не используется для обхода авторизаций пользовательского API (§41).
5. **Только аддитивная модель** (§7): без deny/override/исключений поддеревьев — зафиксировано как ограничение первой версии.
6. **Видимость ≠ полномочия** (§10): фильтрация списков — это visibility; use/manage/administer появляются в волне C на уровне операций.
7. **Trigger ≠ пользователь** (§19): автоматические Run — под execution identity с собственными правами.
8. **Org Unit у пользователя один** (primary, §11–12): таблица дополнительных memberships — будущее расширение без переделки модели.

## 5. Не-цели (первой версии, §54 спеки)

- Deny rules, сложные ACL, произвольные permission sets.
- Матричная оргструктура (несколько узлов пользователя).
- HR sync / SCIM / федерация организаций.
- Выбор получателя LLM-ом, workflow согласований.
- ltree вместо materialized path.
