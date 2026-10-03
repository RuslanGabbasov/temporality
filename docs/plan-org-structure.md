# План работ: Оргструктура и модель доступа

Source spec: `docs/org-structure.md` (расширенная версия, 55 разделов / 8 фаз).
Статус: **Фаза 1 завершена** (коммит `0e1ec70`). **Волна A завершена** (UI организации и доступности, коммиты `fa49dde`, `42202ca`). **Волна B завершена** (явное членство в проектах + аудит доступа). **Волна C завершена** (роли на узлах, авторизация ранов, execution identity, snapshot контекста). Остальное — по волнам ниже; порядок согласован с фазами спеки, но UI организации вынесен вперёд (волна A), потому что бэкенд фаз 1–2 уже существует и без UI неюзабелен.

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
| 2 Project Model | project↔units, members, project roles, миграция | ✅ готово (units + members + аудит) | B |
| 3 Authorization | visibility/use/manage/administer, проверки на операциях, execution identity | ✅ готово (provider credentials отложены до secret store) | C |
| 4 Triggers | scope триггеров, авторизация, запуск под identity, аудит | не начато | D |
| 5 HITL | human_request, recipient resolution, каналы, timeout-политики | ядро есть | E |
| 6 Policies | наследуемые политики выполнения | не начато | F |
| 7 UI | Org Tree, доступность, membership, identity, политики | Org Tree/доступность/membership готово | A + B (готово); identity/политики — по фазам |
| 8 Migration & Cleanup | расхождение моделей, удаление legacy | переходное правило | G |

## 2. Волны работ

### Волна A. UI организации и доступности — M — фаза 7 (частично)

- [x] `Org.tsx`: дерево со сворачиванием; инспекция узла (ресурсы по видам, пользователи, проекты); создание/переименование; перемещение выбором родителя с подтверждением («меняет видимость»); удаление пустого узла через наш confirm-диалог.
- [x] `Layout.tsx`: раздел «Организация» (admin; остальным по ролям на узлах — после волны C).
- [x] Поле «Доступность» (селект узла, дефолт «Вся организация»; для admin — эффективная область «Development └── 14 дочерних») в формах Agents/Skills/Mcp/Providers.
- [x] Проект: мультивыбор узлов («Организационные области»); участники — в волне B; legacy `allowed_users` скрыт и заморожен на чтение.
- [x] Пользователь: выбор основного подразделения вместо списка проектов.
- [x] Карточки списков: бейдж узла / «глобально».
- [x] Мастер корня: если `org_unit` пуст — создание организации (онбординг/страница «Организация»).
- [x] `workspaceApi.ts`: `orgApi`; i18n ru/en; обе темы; `ListFilter`.

### Волна B. Project Model — M — фаза 2

- [x] `workspace_project_member (project_id, user_id, role, created_at, created_by, PK(project_id,user_id))` — миграция 000035 (в ней же `access_audit_log`).
- [x] Доступ к проекту = `org-unit access OR explicit membership` (заменяет переходное AND-правило; `allowed_users` заморожен на чтение и больше не учитывается; PUT без поля сохраняет колонку как есть).
- [x] API: `GET/POST /v1/workspace/projects/{id}/members`, `DELETE /v1/workspace/projects/{id}/members/{userID}`; `DELETE /v1/org/resources/{kind}/{id}/binding` (алиас PUT с ""); `GET /v1/org/audit?limit=`. Отдельные `…/org-units/{unit_id}` эндпоинты не делались — replace-семантика через `PUT /v1/workspace/projects/{id}` с `org_units` покрывает сценарий.
- [x] Аудит-минимум: события `project.member_added/removed`, `resource.bound/unbound`, `org_unit.created/updated/moved/deleted` (§36).
- [x] UI: участники проекта в диалоге проекта (мультивыбор пользователей с ролью, синхронизация diff-ом при сохранении).
- [ ] Внешние пользователи MVP: `user_type` и `expires_at` — опционально, отложено до потребности (сама модель explicit membership готова).

### Волна C. Authorization — L — фаза 3

- [x] Модель эффективной роли в controlplane: `Principal.OrgRoles` (гранты с self-path узла) + `MaxRoleAt(unitID, unitPath)` — максимум из роли установки, membership-роли и грантов на сам узел и его предков; глобальные ресурсы — только роль установки. Unit-тесты на дерево (грант на предка/сам узел/сиблинга/потомка/глобальный ресурс).
- [x] Роли на узлах: миграция 000036 `org_unit_role`; стор `ListOrgUnitRoles/SetOrgUnitRole/RemoveOrgUnitRole/ListAllOrgUnitRoleGrants`; API `GET/PUT/DELETE /v1/org/units/{id}/roles/{userID}` (мутации — admin, ParseRole-валидация); аудит `role.granted/revoked`; `refreshUserTokens` подхватывает гранты без рестарта; роли в инспекции узла (join имён); узел с грантами не удаляется.
- [x] Project scope из орг-модели: `VisibleProjectIDs` — для назначенных в узел не-админов токен получает конкретный список проектов (org-пересечение ∪ membership) вместо legacy `user.projects`; admin и непривязанные — `*` (переходный режим). `refreshUserTokens` вызывается после мутаций проектов/участников/грантов.
- [x] Проверки на операциях (§22): `authorizeRunUse` в `POST /v1/agent/runs` и `POST /v1/workspace/tasks/{id}/runs` (гейт ослаблен до reader, авторизация — 403 с точной причиной): видимость проекта (org ∪ membership) + эффективная роль ≥ writer; агент — OrgVisible + writer на его узле; скиллы/MCP рана — OrgVisible; «висячие» ссылки пропускаются молча, как в рантайме. PUT агента проверяет OrgVisible скиллов/MCP (иначе 403).
- [x] Snapshot execution context (§34–35): миграция 000037 `workspace_run.exec_context`; заполняется при старте рана (tasks/trigger/schedule): agent_id/agent_version/skill_ids/mcp_ids/model/project_id/actor/execution_identity_id/authorized_by{user_id, role(эффективная), org_unit_id}; изменение оргструктуры не затрагивает живые раны; читается всеми рановыми запросами.
- [x] Execution Identity — сущность для автоматических запусков (§20): миграция 000038; CRUD `GET/POST/PUT/DELETE /v1/workspace/execution-identities` + аудит `execution_identity.created/updated/deleted`; отсутствующие allowed-списки дефолтятся к `["*"]` (не «ничего»); wiring в триггеры — волна D, UI — фаза 7.
- [x] Аудит `user.org_unit_changed` (from/to) при смене узла пользователя.
- [ ] Provider credentials (§23) — отложено: `api_key_ref` — ссылка на env/secret, не значение; ужесточение после появления реального хранилища секретов. CRUD ресурсов остаётся на installation-role, org-гранты пока дают запуск ранов.
- [ ] UI ролей на узлах и execution identities — фаза 7 (спека §42, §47).

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
