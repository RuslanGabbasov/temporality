# QA-отчёт: полная верификация ветки `feature/trace-runs-tab-v2` (T5)

- **Дата:** 2026-10-22
- **Верификатор:** QA (подзадача T5, промпт `docs/tasks/2026-10-22-trassirovka-razrabotka/prompts/t5-qa.md`)
- **Объект:** worktree `/workspace/worktrees/trace-runs-tab`, ветка `feature/trace-runs-tab-v2`, диапазон `223eb0a..HEAD`
- **HEAD на момент верификации:** `697485a` — `wip(snapshot): state carried over from the previous development approach (pre-existing uncommitted work)`
- **Требования:** `docs/specs/trassirovka-vkladka-zapuski/v2-consolidated.md` (v2-rework-2), §9 NFR-1…9, §10 DoD-1…9
- **## ВЕРДИКТ: FAIL** (блокер: коммиты T1–T4 отсутствуют на ветке; целевой функционал саги не реализован)

---

## 1. Главный вывод (блокер)

Диапазон `223eb0a..HEAD` содержит **ровно один коммит — сам снапшот `697485a`** («перенесённое незакоммиченное состояние прошлого подхода»). Коммитов T1–T4 (backend консоли, трёхслойная модель, фильтры, UI консольного вывода) поверх снапшота **нет**:

```
$ git log --oneline 223eb0a..HEAD
697485a wip(snapshot): state carried over from the previous development approach (pre-existing uncommitted work)

$ git reflog | head -3
697485a HEAD@{0}: commit: wip(snapshot): state carried over ...
223eb0a HEAD@{1}: reset: moving to HEAD

$ git stash list
(пусто)
$ git log --all -S 'console://' --oneline
697485a wip(snapshot) ...   # единственный коммит, содержащий строку (в docs снапшота)
```

Прямые проверки целевого функционала в коде ветки (HEAD + рабочее дерево):

| Ожидание (задача) | Факт |
|---|---|
| Backend: `GET /v1/agent/console/{id}`, `.../stream` (SSE), in-memory registry | `grep -rn '/v1/agent/console' cmd/ kernel/` → **0 вхождений** |
| UI: канонический URI `console://<operation_id>` (DoD-5) | `grep -rn 'console://' debugger/src/` → **0 вхождений** |
| Фильтры трассы: категория/тип/статус/участник/длительность, дебаунс ≥300 мс (DoD-4) | дебаунс: `grep -rn 'debounce' src/AgentRuns.tsx src/runsLoad.ts src/ListFilter.tsx` → **0 вхождений**; есть только текстовый фильтр списка ранов (`ListFilter`, AgentRuns.tsx:1070) и чип «ops only» (AgentRuns.tsx:1064) |
| Состояние фильтров/поиска в URL (DoD-4) | в URL хранятся только `run` и `view` (AgentRuns.tsx:336, 342) |
| Свёрнутый сырой слой «Сырое событие (JSON)» (DoD-3) | `grep -rn 'Сырое\|rawEvent\|envelope' src/AgentRuns.tsx src/locales/ru.ts` → **0 вхождений**; сырой JSON аргументов при этом доступен напрямую (см. DoD-2) |
| Тесты redaction `secret.*` (DoD-2) | `grep -rn 'secret' src/*.test.ts src/**/*.test.ts` → **0 вхождений** |
| DoR: отчёты `t1-console-report.md`, `t2-layers-report.md`, `t3-filters-report.md`, `t4-console-ui-report.md` в `debugger/docs/trace-rework/reports/` | каталог содержит только **прошлую итерацию**: `r1-review.md`, `t1-report.md`, `t2-report.md` (от 2026-10-21) — требуемых файлов **нет** |

Единственный след T1 в рабочем дереве — **незакоммиченная** правка `kernel/sandbox/docker.go` (+20 строк: хук `OnOutput` в `Request` и фан-аут чанков из `limitedBuffer`, ссылки на §6 спеки). Это фрагмент T1 без registry, HTTP и SSE; файл не входит в заявленный для T1 периметр (`kernel/agent/activities.go`, `cmd/agent-kernel/main.go`). Правка не закоммичена и в верифицируемый диапазон не входит.

**Дополнительная проверка:** в основной рабочей копии `/workspace/temporality` целевой функционал также отсутствует (`console://` и `/v1/agent/console` — 0 вхождений; отчёты — те же три файла прошлой итерации), т.е. наработки T1–T4 не «висят» незакоммиченными и там. Работы T1–T4 в доступных артефактах нет вовсе.

Следствие: верифицировано фактическое состояние ветки (снапшот «прошлого подхода» + env-правка), а не результат T1–T4. Построчная сверка DoD ниже выполнялась по коду и прогонам, как требует промпт, и фиксирует отсутствие целевого функционала.

## 2. Прогоны (команды + полный итог)

### 2.1 vitest, полный прогон debugger

Команда: `cd debugger && npx vitest run` (без правок кода; см. ограничение окружения E-1 по флагу `NODE_OPTIONS`).

```
# Прогон 1 (без флага, документирование ограничения окружения):
 Test Files  11 failed | 23 passed (34)
      Tests  236 passed (236)
     Errors  1 error
# 11 файлов падают НА ИМПОРТЕ: ERR_REQUIRE_ESM (@carbon/utilities → temporal-polyfill;
# html-encoding-sniffer → @exodus/bytes). Ни одного упавшего ассерта.

# Прогон 2 и 3 (NODE_OPTIONS=--experimental-require-module, повтор для стабильности):
 Test Files  34 passed (34)
      Tests  326 passed (326)
     Errors  1 error   # unhandled в воркере, см. ограничение E-2; на тесты не влияет
```

Оба прогона с флагом идентичны: 34/34 файла, 326/326 тестов, flakiness не обнаружен.

Полная матрица (файл → тестов, все ✓):
eventCatalog 4, sandboxProfileForm 13, locales/locales 2, Markdown 7, observationApi 3, **runsLoad 17**, explorer/sessions 6, workspaceApi 11, explorer/agentExploreCalls 5, experience 35, RunDisputes 7, **eventSummary 52**, experience.fixtures 57, explorer/AreaMap 8, kernelApi 8, Dashboard.users 2, sandboxWidgets 6, Sandbox 2, **teamRuns 3**, agentEvolution 12, **timelineLoad 6**, tokenStream 11, workspaceApi.timeout 2, syntax 5, api 4, Org 5, journalStream 4, Artifacts.header 1, explorer/RelationGraph 8, explorer/HistoryTimeline 6, **agentRunsWindow 4**, explorer/ConflictPanel 2, Workspace 6, Artifacts 2. Итого 34 файла / 326 тестов.

Все требуемые промптом файлы присутствуют и зелёные: `runsLoad.test.ts`, `agentRunsWindow.test.ts`, `eventSummary.test.ts`, `eventCatalog.test.ts`, `locales.test.ts`, `teamRuns.test.ts`, `timelineLoad.test.ts`.

### 2.2 Go: build / vet / test

Окружение: `PATH=/cache/go-toolchain/go/bin` (go1.27.3, linux/arm64; `go` отсутствует в PATH песочницы — ограничение E-3).

```
$ go build ./...                                  → OK (rc=0)
$ go vet ./kernel/agent/... ./cmd/agent-kernel/…  → чисто (rc=0, без замечаний)
$ go test ./kernel/agent/... ./cmd/agent-kernel/...
ok  github.com/temporality-project/temporality/kernel/agent       77.911s
ok  github.com/temporality-project/temporality/cmd/agent-kernel   13.404s
```

Включая `runlist_test.go` из снапшота (322 строки, коммитится в его составе). Новых console-тестов в пакетах нет — их и не должно быть без T1.

### 2.3 Статический анализ (tsc)

ESLint в debugger не настроен (в `package.json` нет lint-скрипта) — **допустимо по условиям задачи**; статанализ засчитывается как `npm run build` (tsc):

```
$ npm run build   # tsc -b && vite build
✓ built in 1.25s
dist/assets/index-CsEAiJye.js 1 748.72 kB │ gzip: 469.02 kB
# Единственное замечание — предупреждение vite о чанке >500 kB (не ошибка, не tsc).
# Ошибок типов нет.
```

### 2.4 Бюджеты запросов (perf-guard)

Закреплено тестами (все зелёные, `debugger/src/runsLoad.test.ts:95–150`, константы `runsLoad.ts:27–33`):

| Бюджет | Константа | Тест | Статус |
|---|---|---|---|
| NFR-1: открытие вкладки ≤3 запросов | `OPEN_REQUEST_BUDGET = 3` | `openTabLoads performs at most OPEN_REQUEST_BUDGET requests (NFR-1)` + `pins the documented budgets` | ✅ зелёный |
| NFR-2: выбор рана = 1 запрос таймлайна | `SELECTION_EVENTS_REQUESTS = 1` | `selectRunEvents costs exactly one journal request for a fresh run (NFR-2)` + `a cached or in-flight run costs zero requests` | ✅ зелёный |
| NFR-9: 0 запросов на символ ввода; подписка консоли 1 на раскрытое событие | — | **тестов нет** (фильтры/поиск и консольный стрим не реализованы) | ❌ отсутствует (часть DoD-4/DoD-6) |
| NFR-7: ≤1.5 с до содержательного рендера | — | тестом не закреплён | ⚠️ по условию задачи допустимо при фиксации как ограничение (см. §5) |

Дополнительно из снапшота: `RUN_LIST_PAGE = 100`, `TIMELINE_FIRST_PAGE = 100` (спек: 50–100), курсорная пагинация, кэш/дедупликация/abort — покрыты тестами `runsLoad.test.ts` (152–350) и зелёные.

## 3. Таблица DoD-1…DoD-9 (проверено кодом и прогонами, не по отчётам разработчика)

| DoD | Вердикт | Основание (evidence) |
|---|---|---|
| **DoD-1** Полное покрытие 100% типов `eventCatalog.ts` + псевдо-инструменты + streaming-типы маппингом или generic-шаблоном | ❌ НЕ ПОДТВЕРЖДЁН | Теста на покрытие каталога нет: `grep -n 'EVENT_CATALOG' src/eventSummary.test.ts` → 0 вхождений. Generic-ветка существует (`eventSummary.ts:625–626`), но обязательный для DoD-1 тест покрытия (T2) отсутствует; маппинг не расширялся |
| **DoD-2** Сырые JSON недоступны из таймлайна; `secret.*` не отображаются; тесты redaction | ❌ НЕ ПОДТВЕРЖДЁН | Тестов на secret/redaction нет (grep по `*.test.ts` → 0). Сырой JSON аргументов рендерится из таймлайна: `AgentRuns.tsx:1252` `json(operation?.arguments ?? details)`, `:1403` `json(result.result)`. Сами кейсы `secret.issued/denied` выводят только имя/причину (`eventSummary.ts:527–530`) — по коду инвариант не нарушен, но без тестов DoD-2 не подтверждается |
| **DoD-3** Кнопка «Детализация» удалена: инлайн-раскрытие + свёрнутый сырой слой | ⚠️ ЧАСТИЧНО / НЕ ПОДТВЕРЖДЁН | Текста «Детализация» в src нет (grep → 0); инлайн-раскрытие есть (`AgentRuns.tsx:231–236`, `runs.show_details/hide_details`) — но это реализация «прошлого подхода» из снапшота, а не T2. Свёрнутый сырой слой «Сырое событие (JSON)» отсутствует (grep → 0); целевые тесты T2 отсутствуют |
| **DoD-4** Фильтры и поиск (§8) + тесты + состояние в URL | ❌ НЕ РЕАЛИЗОВАН | Нет фильтров по категории/типу/статусу/участнику/длительности, нет быстрых чипов §8, нет дебаунса (grep `debounce` → 0), нет текстового поиска по трассе; URL хранит только `run`/`view` (AgentRuns.tsx:336,342). Есть лишь фильтр списка ранов и чип ops-only (AgentRuns.tsx:1064,1070). Тестов фильтрации/поиска/URL нет |
| **DoD-5** Live-стрим + буфер хвоста, адресация `console://<operation_id>`, в UI нет `artifact://` для консольного вывода | ❌ НЕ РЕАЛИЗОВАН | `console://` в `debugger/src` — 0 вхождений; эндпоинтов `/v1/agent/console*` в backend — 0; SSE-стрима консоли нет; клиентского буфера хвоста нет. Запрещённых `artifact://`-ссылок на консольный вывод нет (вакуумно — функционала нет вообще). Незакоммиченный хук `OnOutput` в `kernel/sandbox/docker.go` — единственный фрагмент, registry/HTTP/SSE отсутствуют |
| **DoD-6** Бюджеты NFR-1/NFR-2/NFR-9 закреплены тестами | ⚠️ ЧАСТИЧНО | NFR-1 (=3) и NFR-2 (=1) закреплены и зелёные (`runsLoad.test.ts:95–150`). NFR-9 не закреплён — функционал отсутствует (дефект D-1). NFR-7 тестом не закреплён — зафиксировано как ограничение (допустимо) |
| **DoD-7** Нет регрессии; существующие тесты зелёные + отчёт | ✅ ПОДТВЕРЖДЁН (для текущего состояния ветки) | Полный прогон: 34/34 файла, 326/326 тестов ×2 (§2.1), включая `eventSummary.test.ts` (52), `eventCatalog.test.ts` (4), `teamRuns.test.ts` (3), `locales.test.ts` (2), `runsLoad.test.ts` (17), `agentRunsWindow.test.ts` (4), `timelineLoad.test.ts` (6). Отчёт — настоящий файл |
| **DoD-8** Статанализ без новых замечаний; локальность; новые unit-тесты (форматирование, стриминг, фильтры) | ⚠️ ЧАСТИЧНО | tsc чистый (§2.3); ESLint отсутствует — допустимо, зафиксировано. Новых unit-тестов T2–T4 (стриминг, фильтры/поиск) нет. Локальность диапазона: в `223eb0a..HEAD` только снапшот — посторонних WIP-коммитов сверх снапшота нет (по инструкции снапшот не считается дефектом саги), но и правок T1–T4 в диапазоне нет — «локальность целевых правок» неприменима. В рабочем дереве незакоммиченный `kernel/sandbox/docker.go` (D-3) |
| **DoD-9** Доступность: раскрытие с клавиатуры, aria-метки | ❌ НЕ ПОДТВЕРЖДЁН | Кнопка раскрытия — нативный `<button>` (кликабелен с клавиатуры), но без `aria-expanded`/`aria-label`/`aria-controls` (AgentRuns.tsx:231–236). По всему файлу только `aria-pressed` (1064) и `aria-label` (1483). Специальных a11y-тестов нет (целевые тесты T2 отсутствуют) |

## 4. Дефекты

| ID | Severity | Дефект | Файл/строка/вывод |
|---|---|---|---|
| **D-1** | **Blocker** | Коммиты T1–T4 отсутствуют на ветке; целевой функционал саги (backend консоли, трёхслойная модель, фильтры/поиск, UI консольного вывода) не реализован. DoR не выполнен: отчёты `t1-console-report.md`, `t2-layers-report.md`, `t3-filters-report.md`, `t4-console-ui-report.md` в `debugger/docs/trace-rework/reports/` отсутствуют (лежат только файлы прошлой итерации `r1-review.md`, `t1-report.md`, `t2-report.md` от 21.10) | `git log 223eb0a..HEAD` → только `697485a`; `grep -rn '/v1/agent/console' cmd/ kernel/` → 0; `grep -rn 'console://' debugger/src/` → 0; `grep -rn 'debounce' debugger/src/{AgentRuns.tsx,runsLoad.ts,ListFilter.tsx}` → 0; отсутствие 4 отчётов в `debugger/docs/trace-rework/reports/` |
| **D-2** | Major | Нет обязательных тестов: redaction `secret.*` (DoD-2), покрытие каталога generic-шаблоном (DoD-1), фильтры/поиск/URL (DoD-4), стриминг/буфер/отмена консоли (DoD-5), NFR-9 (0 запросов на символ), a11y (DoD-9) | `grep -rn 'secret' debugger/src/*.test.ts` → 0; `grep -n 'EVENT_CATALOG' debugger/src/eventSummary.test.ts` → 0; тестов фильтров/стриминга в дереве нет |
| **D-3** | Minor | Незакоммиченная правка в worktree вне верифицируемого диапазона: хук `OnOutput` (фрагмент T1) — не закрыт в коммит, не входит в заявленный периметр T1, нарушает чистоту состояния ветки | `git status`: `modified: kernel/sandbox/docker.go` (+20 строк; `Request.OnOutput` ~строка 92–100, `limitedBuffer.Write` ~строка 638–643) |
| **D-4** | Info | Сырой JSON аргументов доступен из UI (наследие, предмет отложенной P-4 по спеке; фиксируется как наблюдение к DoD-2) | `debugger/src/AgentRuns.tsx:1252`, `:1403` |

## 5. Ограничения окружения (не дефекты продукта)

- **E-1 (важно для CI):** на Node `v22.11.0` vitest не может загрузить 11 тест-файлов (`ERR_REQUIRE_ESM`: `@carbon/utilities`→`temporal-polyfill`, `html-encoding-sniffer`→`@exodus/bytes`, подтянуты jsdom@30 из снапшота): `require(esm)` без флага поддерживается с Node 22.12.0. Верификация выполнена с `NODE_OPTIONS=--experimental-require-module` (переменная окружения прогона, код не менялся). Рекомендация: закрепить Node ≥22.12 для debugger или привести зависимости.
- **E-2:** нестабильный unhandled error воркера vitest (3/3 прогонов, не влияет на результат тестов): `undici/lib/interceptor/decompress.js:78` «Cannot read properties of undefined (reading 'bind')» через jsdom@30.1.3. Окружение/jsdom, не тесты продукта.
- **E-3:** `go` отсутствует в PATH песочницы; использован тулчейн `/cache/go-toolchain/go/bin` (go1.27.3 linux/arm64). Прогоны честные, окружение зафиксировано.
- **E-4:** NFR-7 (≤1.5 с рендера) тестом не закреплён — по условию задачи допустимо; зафиксировано как ограничение. Сетевых живых проверок стенда не проводилось (сеть песочницы изолирована от стенда).

## 6. Что проверено и признано зелёным (фактическое состояние ветки)

- vitest debugger: **34 файла / 326 тестов, все зелёные**, 2 идентичных прогона (стабильно, без flakiness) — при учёте E-1.
- `go test ./kernel/agent/... ./cmd/agent-kernel/...` — оба пакета `ok`; `go build ./...` — OK; `go vet` — чисто.
- `npm run build` (tsc -b + vite) — успешно, ошибок типов нет.
- Perf-guard NFR-1/NFR-2 (`runsLoad.test.ts`) — зелёные, бюджеты = 3 и = 1.
- Локальность диапазона: посторонних WIP-коммитов сверх снапшота `697485a` нет (сам снапшот и его посторонний WIP — sandbox-profiles, nudge, migrations, Artifacts/Dashboard/ExperienceTimeline/Sandbox и пр. — по инструкции дефектом данной сагы не считаются).
- Основная рабочая копия `/workspace/temporality` QA не изменялась; продуктовый код QA не менял (к REPORT-коммиту пойдёт только файл отчёта).

## 7. Корроборация (обнаружена после составления отчёта)

Коммит `c53690a` (`docs(trace): T3 blocked at DoR`, Lead-агент, во время верификации T5) — отчёт `debugger/docs/trace-rework/reports/t3-filters-report.md` независимо подтверждает D-1: T2 (модуль `eventPresentation.ts`) отсутствует во **всех 6 ветках** репозитория и никогда не коммитился; «старый» `t2-report.md` (21.10) сам фиксирует блокировку T2 на DoR; по решению человека T3 прерван до попадания T2 в ветку. Незакоммиченный `kernel/sandbox/docker.go` в том отчёте охарактеризован как чужой WIP T1/T4 (совпадает с D-3). Вердикт FAIL этим подтверждается с двух независимых сторон (QA T5 и Lead/T3).

## 8. Итог

**FAIL.** Регрессионная база ветки здорова (vitest 326/326, go — ok, tsc — чисто, бюджеты NFR-1/2 закреплены), но сага не доставлена: T1–T4 отсутствуют на ветке (D-1, Blocker), обязательные тесты целевого функционала не существуют (D-2). Возвращать на доработку: завершение и коммит T1–T4 (включая отчёты в `debugger/docs/trace-rework/reports/`), после чего повторный прогон T5. Рекомендуется также решить судьбу незакоммиченного `kernel/sandbox/docker.go` (D-3): либо довести до полного T1 и закоммитить в рамках T1, либо убрать из рабочего дерева.
