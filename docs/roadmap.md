# Temporality — план предстоящих работ

Дата фиксации: 2026-09-26. Документ — единый план всех открытых фронтов.
Статус-контекст: [`product-architecture.md`](product-architecture.md)
(инвентаризация и целевая архитектура), [`failure-reconciliation.md`](failure-reconciliation.md)
(реализованная модель отказов), [`phase2-status-report.md`](phase2-status-report.md)
(источник пунктов Phase 2).

## 0. Принцип приоритизации

1. **Используемый продукт важнее новых экспериментов**: система разворачивается
   и начинает применяться командой в реальной работе; эксперименты ставятся
   только как acceptance-корпуса для продукта.
2. **Event stream — единственный source of truth**: любые проекции
   (operations listing, timeline, state-at-T) строятся из событий, а не
   хранятся отдельно.
3. **Kernel развивается только там, где мешает достоверной наблюдаемости**
   опыта (решение из product-architecture §6).

## 1. Текущее состояние (кратко)

| Фронт | Статус |
|---|---|
| Experience Timeline + lens + forensic | работает, acceptance-корпуса exp4–8 |
| Auth/RBAC + project-scoping (journal, kernel) | готово, live-smoked |
| Квоты запусков (`KERNEL_RUN_QUOTAS`) | готово, live-smoked (429/Retry-After/usage) |
| Секреты `<VAR>_FILE` (kernel, journal) | готово, live-smoked (401/200/403) |
| Effect semantics + operation reconciler + fault injection | готово, live-drill пройден |
| Model-call observability (`model.completed` metadata) | готово |
| MCP provenance (`server`, `result_ref`, idempotency keys) | готово |
| Exact-duplicate tool calls (`duplicate_of`) | готово |
| Deployment | docker compose (полный стек) |

## 2. P0 — используемый продукт (текущий фронт)

### 2.1. Uncertain operations в debugger UI

Операции с неурегулированным эффектом сейчас видны только через API
(`GET /v1/agent/operations`). Для команды это означает, что реконсайл
происходит вслепую через curl — на практике не будет происходить.

Объём:
- экран/панель «Operations» в debugger (рядом с Agent Runs): список
  uncertain-операций (run, tool, state, reason, started_at, arguments_hash);
- клик → детали + ссылка на run в timeline;
- действие «Reconcile»: выбор эффекта `none|occurred|unknown`, note, actor_id,
  `POST /v1/agent/operations/reconcile`; контрол доступен только токенам с
  ролью operator (reader — read-only);
- после успешного реконсайла запись исчезает из списка (сеттлится).

Acceptance:
- [ ] seeded uncertain-операция видна в UI без curl;
- [ ] реконсайл из UI пишет `operation.reconciled` с `parent_event_id`;
- [ ] reader-токен не видит кнопку реконсайла, operator — видит;
- [ ] список обновляется после реконсайла.

### 2.2. Duplicate-call mitigation (остатки из phase2)

Exact-duplicate dedup есть; остались три вектора из
phase2-status-report (§ «Observed model behavior pathologies»):

- `parallel_tool_calls: false` в запросах chat/completions — просим
  провайдера не эмитить параллельные вызовы вовсе;
- near-duplicate throttling: вариации флагов/echo, отличающиеся от
  exact-match, сейчас проходят (run `-04`: 152 выполнения);
- malformed-argv salvage: одна битая JSON-строка `arguments` сейчас валит
  весь completion; спасти корректные вызовы того же ответа.

Acceptance:
- [ ] повторный прогон calculator-сценария не порождает десятков
  near-duplicate выполнений;
- [ ] битый arguments одного вызова не теряет соседние корректные;
- [ ] `parallel_tool_calls` виден в wire-трафике (unit-тест на тело запроса).

### 2.3. Ops-готовность (runbook)

Командное использование = кто-то администрирует стек. Нужен
`docs/runbook.md`: backup/restore postgres (journal + kernel outbox),
процедура обновления (миграции 0000xx, порядок сервисов), ротация токенов
(`KERNEL_AUTH_TOKENS`/`JOURNAL_AUTH_TOKENS` без простоя — пока только
restart), мониторинг (`/healthz`, `GET /v1/agent/outbox`,
`GET /v1/agent/quotas`), типовые аварии (outbox pending растёт, quota
429-шторм, workflow завис в running).

Acceptance:
- [ ] runbook покрывает backup/restore, upgrade, token rotation, мониторинг;
- [ ] backup/restore прогнан на dev-стеке фактически.

## 3. P1 — валидационные инварианты (остатки Phase 2)

### 3.1. Live MCP E2E через AgentRun

Локальные интеграционные тесты mcpclient есть; live-контур
AgentRun → approval → MCP-tool → persisted `mcp.call.*` (с `server`,
`result_ref`) — не прогнан как сценарий с доказательствами в journal.

Объём: прогон против `examples/test-mcp` (compose уже умеет
`KERNEL_MCP_COMMAND`): задача агенту, требующая `create_issue` (approval
gated), ручной approval через UI, проверка цепочки событий
`approval.requested → granted → mcp.call.started → completed` с
`result_ref` и idempotency key в `_meta`.

Acceptance:
- [ ] живой run с MCP-инструментом через approval завершён;
- [ ] события содержат server identity и result_ref;
- [ ] повторный вызов с тем же operation id не дублирует эффект
  (idempotency по ключу).

### 3.2. Adversarial sandbox matrix

Сейчас — positive smoke. Нужна формализованная батарея (автотесты +
таблица expected-vs-actual в доке): workspace escape (`../`), сетевая
эгрессия (запрещена), resource limits (memory/cpu/pids), доступ к
credentials (docker socket, env хоста), lifecycle (timeout kill, bounded
output, `/scratch` isolation), read-only режимы ролей reviewer/qa.

Acceptance:
- [ ] автоматизированная батарея в `kernel/sandbox` тестах;
- [ ] документ с матрицей случай/ожидание/факт;
- [ ] найденные гэпы закрыты или явно приняты как риски.

### 3.3. Semantic replay / state-at-T / diff

Проекции поверх immutable history: состояние knowledge на момент T,
реконструкция trajectory, diff между T1/T2 (см. [`replay-model.md`](replay-model.md)).
Это прямой фундамент для forensic-исследований в UI.

Объём: journal API вида `GET /v1/knowledge/state?project=&at=` (projection
без нового хранения), diff-эндпоинт; потребляется debugger'ом.

Acceptance:
- [ ] state-at-T воспроизводит lifecycle exp4 (K77/K88 → invalidated, K86)
      на произвольном T;
- [ ] diff показывает, что изменилось между двумя runs;
- [ ] нет нового persistent-хранения — только проекция над событиями.

## 4. P1 — Experience Timeline (ядро продукта)

### 4.1. Activation chain первым классом

Forensic-режим «одним взглядом»: memory recalled → injected → decision →
action → outcome. Сейчас цепочка собирается вручную из событий.

Acceptance:
- [ ] по клику на knowledge видна полная activation chain с переходами
      к соответствующим местам таймлайна;
- [ ] chain различает reused/validated vs failed (кейс транзиентного
      отказа Run 02 из exp4).

### 4.2. Semantic zoom: cluster → episode → event

Глубже текущего уровня: episode-уровень (turn, tool call, observation)
при zoom-in.

### 4.3. URL state / шаринг вью

Текущее состояние вью (lens, фильтры, zoom, selected) — в URL, чтобы
сценарий можно было скинуть ссылкой.

## 5. P2 — глубже в продукт

### 5.1. Автоматические read-after-write пробы

Сейчас вердикт `operation.reconciled` пишет только оператор. Пробы:
автоматическая проверка наблюдаемого следа эффекта (файл в workspace,
issue существует) как **подсказка** оператору, не автовардикт.

### 5.2. Experiment 9 — конкурирующие опыты на длинном горизонте

Следующий acceptance-корпус: несколько гипотез, повторные подтверждения,
contradictions, resurrection, stale knowledge, supersession, разные
scopes. Ставится после п. 4.1–4.2, чтобы новый корпус сразу читался на
таймлайне. Формат как у exp4–8: fixture + тесты + дока.

### 5.3. Cost accounting

`model.completed` несёт token split, но не стоимость. Цено-таблица
(env-конфиг по model id), cost в событиях, агрегат по run/project;
возможно — квоты по токенам в дополнение к квотам запусков.

## 6. Отложено (с условием возврата)

| Пункт | Условие возврата |
|---|---|
| Tenants как отдельная сущность | несколько org-структур; сейчас project-scoping + RBAC + квоты закрывают кейс одной команды |
| Helm chart / не-compose deployment | выход за пределы одного хоста |
| Behavioral SLO / policy enforcement | появятся реальные abuse-сценарии из использования |
| Evidence package как экспорт | запрос от внешних потребителей (аудит) |
| Model gateway vs adapter (pivot §13) | второй провайдер или потребность в fallback/routing |
| Автокластеризация / embeddings / graph DB | подтверждённая невозможность жить без них (см. §7) |

## 7. Не делать

Решения зафиксированы фазовыми планами и кодом (product-architecture §6):
универсальный knowledge graph, embeddings-инфраструктура, graph DB,
автоматическая кластеризация как источник визуальных групп, новый runtime,
«ещё один debugger»/generic event viewer. Scheduling отдельным блоком
harness — не нужен, cron/schedules остаются на стороне Temporal.

## 8. Порядок исполнения

```text
P0: 2.1 UI operations → 2.2 duplicate mitigation → 2.3 runbook
P1: 3.1 live MCP E2E → 3.2 sandbox matrix → 3.3 state-at-T
    параллельно с 4.x (UI-фронт, отдельные ветки работы)
P2: 5.1 пробы → 5.2 exp9 (после 4.1–4.2) → 5.3 cost
```

Правило готовности этапа: каждый пункт закрывается acceptance-чекбоксами,
живые прогоны фиксируются в docs, все тесты (`go test ./...`, tsc,
vitest) зелёные, изменения запушены.
