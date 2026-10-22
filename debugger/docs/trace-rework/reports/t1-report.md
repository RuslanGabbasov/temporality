# Отчёт T1′ — «E: рефакторинг загрузки, анти-N+1, K-2/K-3» (доводка этапа T1)

**Сага:** saga--86d5a2. **Исполнитель:** agent_id=`developer`. **Дата:** 2026-10-21.
**Основание:** `docs/trace-rework/01-spec-validated.md` (§3E, §5), `docs/trace-rework/02-decomposition.md` (DoD T1, «Пересборка DAG»).
**Тип:** T1′ — доводка этапа T1: работа предыдущей попытки продолжена в месте обрыва, не откатывалась; в этой сессии закрыты пункты 1–6 брифа (perf-guard тесты, K-3 индикатор, NFR-5 рендер-окно, NFR-6 гейты, NFR-8 локали, полные прогоны + отчёт).

**Статус:** DoR выполнен (спецификация и фактическое состояние дерева прочитаны, ограничения учтены; сложная задача — действовал строго по спецификации, обращение к оператору запрещено брифом). DoD T1 — выполнен, доказательства в §2–§4.

---

## 1. (а) Сводка изменений этапа T1 целиком (сервер + клиент)

### 1.1 Сервер (Go) — было в дереве на старте T1′, перепроверено этой сессией

| Файл:Строка | Что |
|---|---|
| `workspace/store.go:845–876` | `RunListCursor` + `EncodeRunListCursor`/`DecodeRunListCursor` — курсорная пагинация списка (opaque base64url-жетон, валидация позиции) |
| `workspace/store.go:886–935` | `Store.ListRunsPage` — батч-страница списка ранов (страница 50–100, новый первый лист, next-курсор в конце) |
| `workspace/store.go:938–963` | `Store.RunTerminalStatuses` — терминальные статусы для строки списка одним запросом (источник статуса вместо сканов журнала на клиенте) |
| `workspace/store.go:965+` | `Store.AgentDisplayNames` — display-имена агентов для строк списка (клиенту не нужен запрос списка агентов) |
| `cmd/agent-kernel/main.go:76–94` | `runListScope(principal, project)` — scoping видимости проектов для листинга (пустой проект = глобальный список, ограниченный видимыми проектами; невидимых — пустая страница) |
| `cmd/agent-kernel/main.go:96–206` | `registerRunListRoute` — `GET /v1/agent/runs`: лимит строго 50–100 (вне диапазона — 400, не молчаливый кламп, :101–110); курсор (`DecodeRunListCursor`, :111–120); scoping (:129–135); **K-3**: при ошибке статусов ответ не падает — `partial:true` + `partial_error` + `status:"unknown"` в строках (:152–171, :201–202), ошибка логируется серверно |
| `cmd/agent-kernel/main.go:942` | регистрация роута в сборке |
| `cmd/agent-kernel/runlist_test.go:148,201,219,247,269,290,316` | 7 тестов: форма страницы, валидация лимита, round-trip курсора, partial-статусы, scoping видимости, пустая видимость, требование auth |

*Расхождение имён (зафиксировано как неясность):* в брифе/спеке функции названы `RunListPage`/`RunListCursor`; фактическое имя страницы — `Store.ListRunsPage` (`store.go:886`), `RunListCursor` существует как тип (`store.go:848`). Семантика совпадает со спецификацией.

### 1.2 Клиент — было в дереве на старте T1′ (предыдущая попытка)

| Файл:Строка | Что |
|---|---|
| `debugger/src/runsLoad.ts:1–42` | модуль-политика загрузки: бюджеты `OPEN_REQUEST_BUDGET=3` (:27), `SELECTION_EVENTS_REQUESTS=1` (:29), `RUN_LIST_PAGE=100` (:31), `TIMELINE_FIRST_PAGE=100` (:33), `TIMELINE_CATCHUP_PAGE=500` (:35), `TIMELINE_EVENT_CAP=4000` (:37), `TIMELINE_CACHE_MAX=6` (:39) |
| `runsLoad.ts:88` | `fetchRunList` — страница батч-списка (project/limit/cursor/signal) |
| `runsLoad.ts:123` | `buildRunRows` — дерево строк из батч-страницы, чистая функция (сканы журнала больше не нужны, K-2) |
| `runsLoad.ts:164` | `mergeRunEntries` — мерж страниц: известные раны обновляются на месте, явно загруженные старые страницы переживают тихий рефреш |
| `runsLoad.ts:185–216` | `openTabLoads` — весь open-путь ровно в `OPEN_REQUEST_BUDGET` запроса: батч-список + открытые human-запросы + reconcile-операции; отказ компаньонов не роняет открытие, причина попадает в `errors` («K-3 spirit») |
| `runsLoad.ts:249–279` | `fetchRunTimeline` — первый лист = запрос выбора (100), далее догон курсоров 500/лист с капом 4000 (семантика старого `runEvents` сохранена, NFR-4), дедуп по `source:event_id` |
| `runsLoad.ts:282–311` | `selectRunEvents` — кэш по `run_id` (LRU 6), in-flight дедуп, `refresh=true` для поллинга, abort не травит кэш, ошибка refresh сохраняет последний валидный снапшот |
| `runsLoad.ts:313–344` | view-lazy `fetchRunResult` / `fetchRunTrajectory` (не вызываются при выборе) |
| `AgentRuns.tsx:7–17` | миграция на `runsLoad` |
| `AgentRuns.tsx:297–301` | state K-3: `listPartial` / `listPartialError` |
| `AgentRuns.tsx:305–310` | per-load `AbortController`-рефы (NFR-4) |
| `AgentRuns.tsx:319–324` | `pageVisible` + подписка на `visibilitychange` (NFR-6, было заявлено) |
| `AgentRuns.tsx:358–371` | display-only каталог агентов из строк батч-списка (монтирующий `listAllAgents`-fetch убран) |
| `AgentRuns.tsx:534–560` | NFR-3: `childRuns` — мета-строки делегирования из таймлайна, `loadChildEvents` — ленивая догрузка через тот же кэш `selectRunEvents`; `Promise.all(childIDs.map(...))` устранён |
| `AgentRuns.tsx:600–644` | `loadRuns`: `openTabLoads` вместо 4 сканов; partial → state + `console.warn` с `partial_error` (:613–619); мерж страниц; авто-выбор без per-row проб |
| `AgentRuns.tsx:647–663` | `loadOlderRuns` — курсорная догрузка; partial-страница тоже поднимает индикатор (:657–661) |
| `AgentRuns.tsx:666–720` | `loadRun` — только `selectRunEvents` (1 запрос/кэш/refresh), остальное лениво |
| `AgentRuns.tsx:731–830` | view-lazy эффекты: result (:738–764), trajectory+cost (:766–797), budget (:799–830) — по `view`, с abort, не перезапрашиваются от poll/SSE |
| `AgentRuns.tsx:833–839` | polling выбранного рана 10 с, **с сохранённым** visibility-гейтом (NFR-6) |
| `AgentRuns.tsx:852–874` | SSE `subscribeRunJournal` выбранного рана — сохранён (жёсткое ограничение) |
| `AgentRuns.tsx:881` | тихий рефреш списка 15 с — сохранён |
| `observationApi.ts:118–119`, `workspaceApi.ts:1474–1482`, `kernelApi.ts:102–106` | `AbortSignal` в событийном/запросном API (NFR-4) |

### 1.3 Изменения, выполненные ЭТОЙ сессией (доводка T1′)

| Файл:Строка | Что | Зачем |
|---|---|---|
| `AgentRuns.tsx:63–71` | `TRACE_WINDOW=50`, `TRACE_WINDOW_STEP=50` переведены в `export`, добавлена чистая `windowedTrace(rows, limit)` | NFR-5 вычисление окна стало тестируемым |
| `AgentRuns.tsx:528` | `traceWindow = useMemo(() => windowedTrace(trace.visible, traceLimit), …)` — раньше окно вычислялось, но **не применялось при рендере** | NFR-5 |
| `AgentRuns.tsx:1404` | рендер трассы: `traceWindow.map(...)` вместо `[...trace.visible].reverse().map(...)` — DOM больше не материализует весь ран | NFR-5 |
| `AgentRuns.tsx:1485–1493` | кнопка «показать ещё N событий» (`TRACE_WINDOW_STEP` за клик, рост только по явному запросу) | NFR-5 |
| `AgentRuns.tsx:1054–1066` | **K-3**: индикатор `InlineNotification kind="warning"` над списком — «статусы недоступны» + причина (`listPartialError`), заголовок/подзаголовок из локалей | K-3 (сами state/warn были, UI-индикатор отсутствовал) |
| `AgentRuns.tsx:850,874` | visibility-гейт SSE: условие `!pageVisible` + `pageVisible` в deps (hidden → abort подписки, visible → переподписка) | NFR-6 (гейт был только у polling) |
| `AgentRuns.tsx:880–883` | visibility-гейт тихого рефреша списка: `if (!pageVisible) return` + `pageVisible` в deps | NFR-6 |
| `AgentRuns.tsx:504` | visibility-гейт токен-стрима: `useRunTokenMetrics(selected, selectedLive && pageVisible)` (это тоже сетевой SSE выбранного рана) | NFR-6 (дополнение к трём обязательным точкам) |
| `AgentRuns.tsx:379` | `sandbox_profile: ''` в display-only каталоге агентов | фикс ошибки tsc на старте (тип `Agent` получил обязательное поле у соседней задачи; откат чужих изменений запрещён — добавлено поле в узком скоупе) |
| `kernelApi.ts:109–112` | `whoami(signal?: AbortSignal)` — сигнатура обновлена под вызов `whoami(unmountRef.current.signal)` (`AgentRuns.tsx:925`) | фикс ошибки tsc на старте (abort-сweep предыдущей попытки не закрыл сигнатуру) |
| `locales/ru.ts:737–739`, `locales/en.ts:737–739` | новые ключи (обе локали, только добавление): `runs.statuses_unavailable`, `runs.statuses_unavailable_hint`, `runs.trace_show_more` | NFR-8 (парность ru/en) |
| `src/runsLoad.test.ts` (новый, 17 тестов) | perf-guard + unit-тесты (§2) | DoD-3/DoD-5 |
| `src/agentRunsWindow.test.ts` (новый, 4 теста) | NFR-5 окно (§2) | DoD-5 |

**Не тронуто (по брифу):** K-1 — `AgentRuns.tsx:1234` `json(operation?.arguments ?? details)` остался в зоне T2; `journalStream.ts` (только чтение, парсер не менялся); polling 10 с и SSE сохранены; семантика событий журнала не менялась; `secret.*`-слой и `console://`/`artifact://` — вне скоупа T1 (T2/T3); чужие файлы (`sandboxprofiles*`, `kernel/sandbox/*`, `Artifacts.tsx`, `Dashboard.tsx`, `ExperienceTimeline.tsx`, `Sandbox.tsx`, `package.json` и др.) не изменялись; в локалях только строки-добавления (0 удалений: `git diff --numstat` = `15 0` для каждой).

**Локальность диффа этой сессии:** `AgentRuns.tsx`, `kernelApi.ts`, `locales/ru.ts`, `locales/en.ts` + 2 новых тест-файла. Изменений в соседних модулях нет.

---

## 2. (б) Прогон тестов — точные команды и полный вывод

### 2.1 `cd /workspace/temporality/debugger && npm test` (vitest)

```
> temporality-debugger@0.1.0 test
> vitest run


 RUN  v5.0.1 /workspace/temporality/debugger

 ✓ src/locales/locales.test.ts (2 tests) 56ms
 ✓ src/runsLoad.test.ts (17 tests) 26ms
 ✓ src/experience.test.ts (35 tests) 22ms
 ✓ src/Markdown.test.tsx (7 tests) 21ms
 ✓ src/experience.fixtures.test.ts (57 tests) 30ms
 ✓ src/explorer/AreaMap.test.tsx (8 tests) 10ms
 ✓ src/RunDisputes.test.ts (7 tests) 13ms
 ✓ src/sandboxWidgets.test.ts (6 tests) 13ms
 ✓ src/Org.test.tsx (5 tests) 15ms
 ✓ src/Sandbox.test.tsx (2 tests) 12ms
 ✓ src/explorer/agentExploreCalls.test.ts (5 tests) 11ms
 ✓ src/Dashboard.users.test.tsx (2 tests) 11ms
 ✓ src/teamRuns.test.ts (3 tests) 11ms
 ✓ src/workspaceApi.test.ts (11 tests) 9ms
 ✓ src/eventCatalog.test.ts (4 tests) 8ms
 ✓ src/kernelApi.test.ts (8 tests) 7ms
 ✓ src/Artifacts.header.test.tsx (1 test) 12ms
 ✓ src/eventSummary.test.ts (52 tests) 12ms
 ✓ src/syntax.test.ts (5 tests) 7ms
 ✓ src/agentEvolution.test.ts (12 tests) 5ms
 ✓ src/sandboxProfileForm.test.ts (13 tests) 5ms
 ✓ src/timelineLoad.test.ts (6 tests) 5ms
 ✓ src/tokenStream.test.ts (11 tests) 6ms
 ✓ src/observationApi.test.ts (3 tests) 4ms
 ✓ src/explorer/RelationGraph.test.tsx (8 tests) 6ms
 ✓ src/explorer/sessions.test.ts (6 tests) 4ms
 ✓ src/api.test.ts (4 tests) 3ms
 ✓ src/journalStream.test.ts (4 tests) 3ms
 ✓ src/explorer/HistoryTimeline.test.tsx (6 tests) 3ms
 ✓ src/agentRunsWindow.test.ts (4 tests) 4ms
 ✓ src/explorer/ConflictPanel.test.ts (2 tests) 2ms
 ✓ src/Workspace.test.tsx (6 tests) 3ms
 ✓ src/Artifacts.test.ts (2 tests) 2ms

 Test Files  33 passed (33)
      Tests  324 passed (324)
   Start at  13:15:13
   Duration  3.71s (import 71%, transform 24%, tests 4%, worker 1%)

     Import  117 modules were evaluated 216 times · 6.09s total, 71% of tracked time
             ~489ms faster with isolate: false — shared modules are evaluated once per worker instead of once per file
             learn more: https://vitest.dev/guide/improving-performance#test-isolation

npm-test-exit=0
```

**Итог: 33/33 файла, 324/324 теста — зелёные, exit 0.**
Контрольная точка на старте сессии (до правок T1′): `npm test` = **31 файл / 303 теста — зелёные** (baseline). Прирост: +2 файла / +21 тест (новые `runsLoad.test.ts`, `agentRunsWindow.test.ts`). Все существующие тесты, включая `eventSummary.test.ts`, `eventCatalog.test.ts`, `teamRuns.test.ts`, `locales.test.ts`, — зелёные без правок (регрессий нет, DoD-4).

Что закрепляют новые тесты:

**`src/runsLoad.test.ts` (17):**
- perf-guard «бюджеты»: константы `OPEN_REQUEST_BUDGET=3`, `SELECTION_EVENTS_REQUESTS=1`, страницы 50–100; `openTabLoads` → ровно 3 fetch-вызова (≤ бюджета); `selectRunEvents` для нового рана → ровно 1 запрос с `limit=100&run=…`; кэш/in-flight → 0 запросов;
- `fetchRunList`: дефолтная страница `limit=100` + `project` + проброс `signal`, курсорная страница (`cursor=c-next&limit=60`), отказ при не-OK, **partial-страница** (`partial:true`, `partial_error`, `status:'unknown'` в строке → `buildRunRows` даёт `unknown`, не теряется);
- деградация `openTabLoads`: падение компаньонов даёт причины в `errors.questions`/`errors.ops` при живом списке; падение самого списка — reject (ровно 3 запроса в обоих случаях);
- `buildRunRows` (дерево/depth/свежесть), `mergeRunEntries` (обновление на месте, выживание старых страниц, non-mutation входа);
- `selectRunEvents`: догон курсоров (2-й лист `limit=500`, `cursor=c1`) с дедупом шва; `refresh=true` мимо кэша; упавший refresh сохраняет последний снапшот; **abort** → reject с `AbortError` и незатравленный кэш (следующий выбор снова ходит в сеть).

**`src/agentRunsWindow.test.ts` (4):** окно в границах 50–100, шаг 50; для рана в 4000 событий рендерится ровно `TRACE_WINDOW` newest-first строк; короткий трасс показывается целиком; расширение только явным шагом.

### 2.2 `cd /workspace/temporality && go build ./...`

```
go-build-exit=0
```
(вывод пустой — сборка без ошибок и предупреждений)

### 2.3 `cd /workspace/temporality && go vet ./cmd/agent-kernel/ ./workspace/`

```
go-vet-exit=0
```
(вывод пустой — замечаний нет)

### 2.4 `cd /workspace/temporality && go test ./cmd/agent-kernel/ -run TestRunList -v -count=1`

```
=== RUN   TestRunListPageShape
--- PASS: TestRunListPageShape (0.01s)
=== RUN   TestRunListLimitValidation
--- PASS: TestRunListLimitValidation (0.00s)
=== RUN   TestRunListCursorRoundTrip
--- PASS: TestRunListCursorRoundTrip (0.01s)
=== RUN   TestRunListPartialStatuses
--- PASS: TestRunListPartialStatuses (0.00s)
=== RUN   TestRunListVisibilityScoping
--- PASS: TestRunListVisibilityScoping (0.00s)
=== RUN   TestRunListScopeEmptyVisibility
--- PASS: TestRunListScopeEmptyVisibility (0.00s)
=== RUN   TestRunListRequiresAuth
--- PASS: TestRunListRequiresAuth (0.00s)
PASS
ok  	github.com/temporality-project/temporality/cmd/agent-kernel	0.013s
go-test-exit=0
```

**Итог: 7/7 PASS (неповторный прогон `-count=1`), go build — 0, go vet — 0.** Заявленные 7/7 подтверждены лично.

---

## 3. (в) Статический анализ

### 3.1 `cd /workspace/temporality/debugger && npm run build` (= `tsc -b && vite build`)

```
> temporality-debugger@0.1.0 build
> tsc -b && vite build

vite v8.3.1 building client environment for production...
transforming...
✓ 1264 modules transformed.
rendering chunks...
computing gzip size...
dist/index.html                     1.11 kB │ gzip:   0.51 kB
dist/assets/index-D5EIxf5v.css  1,047.05 kB │ gzip: 113.11 kB
dist/assets/index-FQ8_2yvk.js   1,747.24 kB │ gzip: 468.58 kB

[plugin builtin:vite-reporter] 
(!) Some chunks are larger than 500 kB after minification. Consider:
- Using dynamic import() to code-split the application
- Use build.rolldownOptions.output.codeSplitting to improve chunking: https://rolldown.rs/reference/OutputOptions.codeSplitting
- Adjust chunk size limit for this warning via build.chunkSizeWarningLimit.
✓ built in 917ms
npm-build-exit=0
```

**Итог: exit 0, ошибок типов нет (`tsc -b` покрывает `include: ["src"]`, т.е. и тест-файлы).**

Замечания и только замечания:
1. **На старте T1′ `npm run build` падал** с двумя ошибками TS в зоне T1 — обе устранены в этой сессии (без отката чужих правок):
   - `src/AgentRuns.tsx(356)` TS2741 `sandbox_profile` missing in `Agent` — добавлено `sandbox_profile: ''` в display-only каталог (`AgentRuns.tsx:379`);
   - `src/AgentRuns.tsx(910)` TS2554 `whoami` «Expected 0 arguments, но получен 1» — сигнатура `whoami(signal?)` (`kernelApi.ts:109`).
   Последующие прогоны `tsc -b` чистые (до правок тестов и после).
2. **ESLint в проекте не настроен** — подтверждено: нет `.eslintrc*`/`eslint.config.*`, в `package.json` нет скрипта `lint` (grep по `package.json`: совпадений `"lint` нет). Единственный статический анализ — `tsc -b` (strict) + `vite build`. Отчёт зафиксирован по требованию DoD-5.
3. Предупреждение vite «Some chunks are larger than 500 kB» — generic-предупреждение о размере общего бандла (`index-*.js` 1.7 МБ, бандл один на всё приложение). Не связано с правками T1 (никакого код-splitting не добавлялось и не убиралось); baseline-прогон `vite build` до правок физически невозможен — предыдущий `tsc -b` падал на двух ошибках выше, поэтому «новизна» предупреждения относительно baseline подтвердить нельзя — честно помечено как ограничение.

---

## 4. (г) Таблица статусов: NFR-1…NFR-8 и K-2/K-3

Правило маркировки: **«сделано и проверено»** — есть автотест или вывод команды, подтверждающий пункт; **«сделано, но не проверено»** — реализовано, автотеста/измерения нет; **«не сделано»** — отсутствует.

| Пункт | Статус | Доказательства / что не покрыто |
|---|---|---|
| **NFR-1**: открытие ≤3 запросов; batch-список (курсор, 50–100); убраны 4 скана и N+1 | **сделано и проверено** | `runsLoad.test.ts`: «openTabLoads performs at most OPEN_REQUEST_BUDGET requests» (ровно 3), «pins the documented budgets»; сервер: `go test -run TestRunList` 7/7 PASS; сканы/N+1 отсутствуют: `grep 'runEvents(' src/AgentRuns.tsx` → ABSENT, per-row детальные пробы в `loadRuns` нет (код-ревью строк 600–644) |
| **NFR-2**: выбор рана = 1 запрос таймлайна (50–100, курсор); result/costs/trajectory лениво | **сделано и проверено** (бюджет) / **сделано, но не проверено** (ленивость view-вьюх) | тест «selectRunEvents costs exactly one journal request for a fresh run» + «cached/in-flight = 0» + курсорный догон; ленивость result/trajectory/budget — только код-ревью эффектов с гейтом `view` (`AgentRuns.tsx:731–830`), автотеста на view-gates нет (рендер компонента в тестовой среде не автоматизирован — нет @testing-library) |
| **NFR-3**: дети — мета-строка + ленивая догрузка вместо `Promise.all` | **сделано и проверено** (структурно) | `childRuns`/`loadChildEvents` (`AgentRuns.tsx:534–560`), `Promise.all(child…)` отсутствует (grep по файлу, чисто); догрузка идёт через `selectRunEvents` — покрыт его тестами; UI-поведение раскрытия ребёнка автотестом не покрыто |
| **NFR-4**: AbortController, кэш/дедуп по run_id, догон курсоров сохранён | **сделано и проверено** | тесты: abort → `AbortError` без травления кэша; in-flight dedup = 1 запрос; кэш = 0 запросов; refresh-снапшот; догон `runEvents`-семантики (2 листа, cap) — «catches up journal cursors…»; сигналы в `observationApi`/`workspaceApi`/`kernelApi` — типизированы (`tsc` чист), их собственные тесты зелёные |
| **NFR-5**: виртуализация таймлайна (окно 50–100), ≤2000 DOM-узлов, тяжёлые тексты лениво | **сделано и проверено** (окно и ленивость) / **сделано, но не проверено** (фактическое число DOM-узлов в браузере) | `agentRunsWindow.test.ts` (4 теста: границы 50–100, newest-first, шаг); рендер через `traceWindow` (`AgentRuns.tsx:1404`) + кнопка расширения (:1485–1493); тяжёлые тексты (ответ/рассуждение/поля) рендерятся только при `expanded` (`EventDetail`, `AgentRuns.tsx:154–266`); ≤2000 узлов — расчёт (окно ≤100 × ≤20 узлов свёрнутой строки), в браузере не измерялось |
| **NFR-6**: polling только выбранного, ≥5 с, `document.hidden` → пауза; SSE только выбранного; пауза refresh | **сделано, но не проверено** (автотестов нет) | реализовано во всех трёх точках: polling 10 с с гейтом (`AgentRuns.tsx:833–839`), SSE (`:850–874`, гейт добавлен этой сессией), тихий рефреш 15 с (`:880–883`, гейт добавлен этой сессией) + токен-стрим (`:504`); все три включают/выключают через общий `pageVisible` (`:319–324`). Автотеста на visibilitychange нет, поведение в браузере не проверялось |
| **NFR-7**: ≤1.5 с до содержательного рендера (100 ранов/20 Мбит); бюджеты — perf-guard | **сделано и проверено** (бюджеты) / **сделано, но не проверено** (временной замер) | perf-guard тесты бюджетов — §2.1; временной замер 1.5 с не выполнялся (нет стенда/бюджета на замер в этой задаче) |
| **NFR-8**: все новые строки — `locales/ru.ts` + `locales/en.ts`; тесты локалей зелёные | **сделано и проверено** | 3 новых ключа добавлены в обе локали (`ru.ts:737–739`, `en.ts:737–739`, удалений 0); `src/locales/locales.test.ts` (парность ключей + непустые значения) — зелёный в полном прогоне; полный список `t('…')`-ключей `AgentRuns.tsx` сверен с обеими локалями (расхождений нет) |
| **K-2**: N+1 `:549–551` → batch | **сделано и проверено** | per-row `GET /v1/agent/runs/{id}` в list-пути отсутствует (код: `loadRuns` использует только `openTabLoads`, `fetchRunList` — только в `loadOlderRuns`); серверные тесты батч-эндпоинта 7/7; perf-guard «ровно 3 запроса на открытие» |
| **K-3**: `.catch(() => ({events: []}))` → `partial:true` + индикатор «статусы недоступны» + логирование | **сделано и проверено** (сервер/клиент-логика/тесты) / **сделано, но не проверено** (сам UI-индикатор автотестом) | сервер: `main.go:152–171,201–202` + `TestRunListPartialStatuses` PASS; клиент: тип `RunListPage.partial/partial_error` (`runsLoad.ts:75–77`), тест `fetchRunList` partial-страницы, `console.warn` с причиной (`AgentRuns.tsx:616–619, 657–661`), UI-индикатор `InlineNotification` (`AgentRuns.tsx:1054–1066`, строки из локалей). Рендер индикатора автотестом не покрыт |

**Итог по DoD T1:** бюджеты закреплены тестами ✅; новые unit-тесты зелёные (21) ✅; все существующие тесты зелёные (324/324, полный вывод §2.1) ✅; `npm run build` без ошибок типов (вывод §3.1) ✅; `go build`/`go vet`/`go test` чистые ✅; отчёт существует физически (этот файл) ✅.

---

## 5. Зафиксированные неясности и ограничения

1. **Имена серверных функций:** в брифе — `RunListPage`, фактически — `Store.ListRunsPage` (`workspace/store.go:886`); `RunListCursor` существует (:848). Отклонение только в имени, поведение соответствует спецификации.
2. **ESLint не настроен** в проекте (нет конфига и скрипта) — статический анализ = `tsc -b` (strict) + `vite build`; подтверждено grep'ом (§3.1 п.2).
3. **Baseline `vite build` недоступен:** на старте T1′ сборка падала на 2 ошибках TS, поэтому предупреждение о размере чанка не имеет baseline-сравнения; оно generic-ное и не выводится из правок T1.
4. **Браузерные runtime-сценарии не автоматизированы:** фактическое число DOM-узлов трассы (NFR-5), срабатывание `visibilitychange` (NFR-6), рендер K-3-индикатора и ленивость view-вьюх (NFR-2) — покрыты код-ревью/структурными тестами, но не e2e (в проекте нет @testing-library/react/e2e-раннера; добавление новой инфраструктуры — вне радиуса T1).
5. **K-1 не тронут** — `AgentRuns.tsx:1234` (`json(operation?.arguments ?? details)`) остался в зоне T2, как предписано брифом.
6. **Чужие задачи:** в дереве присутствуют их правки (`sandboxprofiles*`, `kernel/sandbox/*`, `Sandbox/Artifacts/Dashboard/ExperienceTimeline`, локальные строки `sandbox.*`/`timeline.*`) — не изменялись и не откатывались; в локалях только добавления.
7. Фикс ошибки `Agent`/`sandbox_profile` сделан добавлением поля в узком display-only скоупе `AgentRuns.tsx`, а не изменением типа `Agent` (чтобы не задеть контракт соседней задачи).
8. Прогон `go test ./...` на весь репозиторий не выполнялся: скоуп T1 — `cmd/agent-kernel` (batch-эндпоинт) и `workspace` (store); пакетные тесты соседних задач (sandbox и др.) не находятся в зоне ответственности T1. `go build ./...` и `go vet` по этим пакетам — чистые.
