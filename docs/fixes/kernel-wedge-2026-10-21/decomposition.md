# Декомпозиция: устойчивость ядра к зависанию (инцидент 2026-10-21)

Сага: saga-temporality-5636f9. Спецификация: docs/fixes/kernel-wedge-2026-10-21/spec.md
(R1–R5). Анализ: docs/fixes/kernel-wedge-2026-10-21/incident-analysis.md (F1–F8, D1–D4).

Рабочее дерево: /workspace/temporality. ВАЖНО: в дереве висит чужой незакоммиченный WIP
(sandbox-profiles/dockerfile-override, Dashboard/AgentRuns/Artifacts и др.) — его ханки
НЕ редактировать и НЕ откатывать; свои правки делать строго аддитивно в точках объёма.

## Подзадачи

| ID | Задача | Агент | Зависимости | Критерии готовности |
|----|--------|-------|-------------|---------------------|
| T1 | R1+R2: дедлайны Temporal-RPC в HTTP-хендлерах + WriteTimeout/IdleTimeout | Developer | — | go build/vet/test чисто; инъекционный тест 503 по таймауту; diff только в cmd/agent-kernel/main.go (+ новый _test.go) |
| T2 | R3: UI — ошибка загрузки проектов с ретраем + таймаут request() | Developer | — | npm run build + vitest чисто (база не сломана, новые тесты зелёные); diff только debugger/src/Layout.tsx, debugger/src/workspaceApi.ts (+ новые тесты) |
| T3 | Ревью диффов T1, T2 | Reviewer | T1, T2 | approve либо возврат с конкретными замечаниями |
| T4 | Верификация R4: полный прогон сборок/тестов | QA | T1, T2 | go build ./..., go vet ./..., go test ./cmd/agent-kernel/ ./kernel/agent/, npm run build, npx vitest run — всё чисто |

Follow-up (вне этой итерации, зафиксированы в spec «Объём»): аудит вотчдогов CallModel (D2),
персистентные логи ядра и pprof/дампы горутин (D4), персистентный taskId чата (D3).

## Промпты суб-агентов

### T1 — Developer (ядро)

Цель: реализовать R1 и R2 спецификации docs/fixes/kernel-wedge-2026-10-21/spec.md.

Контекст: инцидент 2026-10-21 — POST /v1/workspace/tasks/{taskID}/runs зависал навсегда,
когда Temporal-RPC (ExecuteWorkflow) не отвечал: таймаута нет ни у вызова (используется
r.Context()), ни у http.Server. Браузер получал транспортный NetworkError без
HTTP-ответа. Эталон фактов: docs/fixes/kernel-wedge-2026-10-21/incident-analysis.md (F3, D1).

Входные данные: /workspace/temporality/cmd/agent-kernel/main.go — хендлеры
POST /v1/workspace/tasks/{taskID}/runs (~строка 3082), POST /v1/agent/runs/{runID}/nudge
(~1203), POST /v1/agent/runs/{runID}/note (~1232), GET /v1/agent/runs (~97) и
resolveRunState (~3220); конфигурация http.Server (~7135).

Сделать:
1. Все Temporal-RPC из HTTP-хендлеров (ExecuteWorkflow, SignalWorkflow,
   DescribeWorkflowExecution, GetWorkflow) выполнять с context.WithTimeout; дефолт 15 с,
   override env KERNEL_TEMPORAL_RPC_TIMEOUT. По таймауту — HTTP 503, тело
   {"error":"temporal rpc timeout: …"}.
2. http.Server: добавить WriteTimeout и IdleTimeout (120 с, override
   KERNEL_HTTP_WRITE_TIMEOUT / KERNEL_HTTP_IDLE_TIMEOUT). SSE-хендлеры
   (/v1/workspace/runs/{id}/stream и /tokens) не должны падать от WriteTimeout — если
   WriteTimeout их задевает, исключить их явно (например, перенести их на отдельный
   http.Server-конвейер без WriteTimeout или периодически продлевать дедлайн записи),
   зафиксировав решение комментарием в коде.
3. Юнит-тесты: медленный Temporal-клиент (фейк с блокирующимся RPC) → хендлер возвращает
   503 не позже дедлайна; быстрый фейк → штатный 202/200.

Готовность: go build ./... и go vet ./... чисто; go test ./cmd/agent-kernel/ ./kernel/agent/
чисто (включая новые тесты). Diff — только main.go (+ новые *_test.go при необходимости);
чужой WIP не трогать.

### T2 — Developer (UI)

Цель: реализовать R3 спецификации docs/fixes/kernel-wedge-2026-10-21/spec.md.

Контекст: при недоступности API ядра селектор проектов молча пустел: loadProjects в
debugger/src/Layout.tsx (~строка 334) при любой ошибке делает catch { /* ignore */ } и
возвращает [] — пользователь видит «пропавший список проектов» без объяснения
(incident-analysis F5). request() в debugger/src/workspaceApi.ts (~1003) не имеет
таймаута — зависший запрос висит вечно.

Сделать:
1. Layout.tsx: состояние ошибки загрузки проектов (текст + кнопка «Повторить»,
   вызывающая loadProjects). Успешная загрузка очищает ошибку. Существующую логику
   выбора проекта не менять.
2. workspaceApi.ts: request() — общий таймаут 60 с (AbortSignal.timeout(60000) или
   AbortController+setTimeout), при срабатывании бросать ApiError с понятным сообщением.
   Правка строго аддитивная: файл содержит чужой WIP — существующие ханки не менять.
3. Юнит-тесты (vitest): loadProjects при ошибке показывает ошибку и ретрай
   восстанавливать список; request() при зависании бросает ошибку таймаута.

Готовность: npm run build и npx vitest run чисто (включая новые тесты), diff — только
Layout.tsx, workspaceApi.ts и новые тестовые файлы.

### T3 — Reviewer

Цель: качество-гейт диффов T1 и T2. Проверить: соответствие R1–R5, аддитивность
(чужой WIP не тронут), отсутствие регрессий в SSE-хендлерах и в контрактах API,
покрытие тестами. Вердикт approve либо возврат с конкретными замечаниями.

### T4 — QA

Цель: верификация R4 целиком на итоговом дереве: go build ./..., go vet ./...,
go test ./cmd/agent-kernel/ ./kernel/agent/, npm run build (в debugger), npx vitest run.
Любая красная проверка — возврат с точной локализацией.
