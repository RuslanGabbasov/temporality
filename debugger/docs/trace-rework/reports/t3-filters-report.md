# T3 — Фильтры и поиск по трассе во вкладке «Запуски»: отчёт реализации

Статус: **реализовано и проверено**. Ветка `feature/trace-runs-tab-v2`, worktree `/workspace/worktrees/trace-runs-tab`.
DoR: выполнен (бизнес-контекст §8 v2-consolidated.md, DoD-4/NFR-9 подтверждены). Код не менялся в этой задаче —
закоммичен в виде, прошедшем полную верификацию (vitest 42/423, tsc, build).

---

## (а) Сводка изменений — Файл:Строка

### Новые файлы (зона T3)

**`debugger/src/traceFilters.ts`** (новый, ~370 строк) — чистый модуль фильтрации/поиска, без сети и без React:
- **3–21** — докблок: три композируемых измерения §8 (структурный фильтр + чипы + текстовый поиск), 0-fetch-на-символ (NFR-9), ограничение поиска по выводу команд загруженным буфером (§11.2), URL-состояние.
- **24–39** — `TraceFilter`: 5 структурных измерений §8 — `category`, `type`, `status` (ok/error/cancelled), `participant`, `minMs`; `EMPTY_FILTER` (стр. 39) и `isFilterEmpty` (стр. 41).
- **48–56** — `participantOf(event)`: `model.*` → model; `tool.*`/`mcp.*` → tool; `human.*`, `run.user_note`, `hitl.verdict.recorded`, `approval.*`, `hint.*` → human; остальное → system.
- **59–67** — `statusOfItem(item)`: type-guard по `kind` — `approvalPair` → свой статус, `group` → `'none'`, иначе статус строки *(минимальный фикс №2: type-guard вместо каста, tsc strict чистый)*.
- **69–83** — `categoryOfItem` / `participantOfItem` / `itemEventsOf`: проекции аспектов айтема (пара считает оба своих события).
- **86–94** — `durationMsOf(event)`: `latency_ms` / `duration_ms` / `wait_ms` из журнала ядра, 0 = неизвестно.
- **97–111** — `matchesFilter(filter, item)`: измерения AND-ятся; свернутые группы пропускаются, пока совпадает хотя бы участник.
- **113–133** — `hasActiveChips` + `matchesChipSet`: активные чипы OR внутри набора, AND с остальными измерениями.
- **136–153** — `filterTraceItems(...)`: применение фильтра и чипов к свернутому окну; группа не осиротевает — остается, пока совпадает хотя бы один участник.
- **156–181** — `PAYLOAD_PICKS` + `payloadTextOf(event)`: human-формы значений (заметка, вопрос, ответ, предложение, feedback); `secret.*`-строки никогда не отдают payload в haystack (инвариант secret.*, P-2).
- **184–216** — `haystackOf(event, row, locale, t)`: заголовок + факт + длительность + альтер-локальный заголовок каталога (поиск по обоим языкам) + payload + детали слоя; для `secret.*` — только имя/статус. `altTitleOf` — с мемоизацией.
- **219–226** — `snippetAround`: сниппет ±60 символов вокруг первого совпадения.
- **229–287** — `SearchEntry` + `searchItems(...)`: case-insensitive подстрока по haystack всех айтемов; группы — через участников и метку, saga — через события saga; `consoleBuffers` (event_id → текст stdout+stderr) подключается опционально *(минимальный фикс №3: guard `item.events.length === 0` в ветке saga, стр. 284 — пустая saga не падает и не матчится)*.
- **290–292** — `itemAnchorEvent`: якорное событие айтема для обратной ссылки «буфер → айтем».
- **295–311** — `groupLabelKey`, `countMatchingMembers` (маркер «N/M» на частично отфильтрованной группе), `buildFilterIndex` (уникальные типы окна), `filterCategories` (группы каталога по порядку).
- **314–368** — URL-состояние (§8): `TRACE_QUERY_KEYS` (`traceCat`, `traceType`, `traceStatus`, `traceWho`, `traceMinMs`, `traceQ`, `traceChips` — префикс `trace*` не пересекается с параметрами списка запусков), `parseTraceState` (неизвестные значения деградируют в «без фильтра», а не в пустой вид), `traceStateQuery` (пустое состояние → чистый URL), `isTraceStateEmpty`, `stripTraceParams`.

**`debugger/src/useDebouncedValue.ts`** (новый, 20 строк):
- **7** — `TRACE_DEBOUNCE_MS = 300` (§8/NFR-9, экспорт для пиннинга тестами).
- **9–14** — `useDebouncedValue<T>(value, delay)`: коммит значения через ≥300 мс после последнего нажатия; сеть не затрагивается, коммит feeds только чистую клиентскую фильтрацию.

**`debugger/src/TraceFiltersPanel.tsx`** (новый, ~255 строк):
- **30–48** — `ConsoleStateLike` + контрак: панель знает только загруженные состояния буферов консоли, ничего не fetch-ит.
- **50–71** — `consoleSearchIndex(consoleStates, items)`: map event_id → объединенный текст буфера, только для событий, реально присутствующих в окне.
- **72–97** — декларативные опции: статусы (ok/error/cancelled), участники (model/tool/human/system), 4 чипа (включая `myInput` 💬), пороги длительности (any/1s/5s/30s).
- **109–249** — `TraceFiltersPanel`: управляемый компонент (`state`/`onState`); 5 селекторов (категория/тип/статус/участник/длительность), ряд из 4 чипов, поле поиска с хинтом `runs.filters.search_hint` об ограничении серверного поиска, кнопка сброса; useMemo на индекс консольных буферов и опции типов.
- **251–253** — `panelSearch(...)`: поиск через `searchItems` с консольным индексом — тот же чистый путь, что и в TraceLayers.

**`debugger/src/traceFilters.test.ts`** (новый, 30 тестов) — participantOf по всем префиксам, matchesFilter по каждому измерению и их AND, поведение групп/пар/saga при фильтрации, чипы-OR/AND, haystack (оба локала, secret-инвариант), snippetAround, searchItems (включая буферы консоли и пустую saga), URL round-trip и деградация неизвестных значений, buildFilterIndex/countMatchingMembers.

**`debugger/src/useDebouncedValue.test.tsx`** (новый, 5 тестов) — debounce 300 мс (fake timers): задержка коммита, сброс таймера при новой печати, дефолтный delay, мгновенный первый рендер, произвольный delay.

**`debugger/src/traceFilters.perf.test.ts`** (новый, 3 теста) — perf-guard NFR-9: 0 сетевых вызовов на символ (spy на fetch/EventSource при печати запроса), фильтрация окна за бюджет на реалистичном окне, отсутствие повторной сборки haystack на некоммитнутый ввод.

### Измененные файлы

**`debugger/src/traceModel.ts`**:
- **177–183** — `TraceChip` расширен четвертым чипом `'myInput'`; добавлен чистый предикат `isMyInputType(type)` (типы собственного ввода пользователя: `run.user_note`, `hitl.verdict.recorded`, `human.answered`, `human.rejected`).
- **186–205** — `matchesChip`: ветка `myInput` для групп и одиночных событий; для `approvalPair` — нерешенный вопрос считается ожидающим вводом человека (совпадает с `myInput`).

**`debugger/src/TraceLayers.tsx`**:
- **5–6** — импорты `filterTraceItems`/`searchItems`/`TraceUrlState` и `TraceFiltersPanel`/`consoleSearchIndex`.
- **219–238** — props `filterState?`/`onFilterState?`/`consoleStates?`; без них — прежнее поведение (незагрязненная трасса + legacy-чипы), обратная совместимость остальных мест использования.
- **241–254** — `visibleItems` через `filterTraceItems` (три измерения AND, чипы OR), `searchEntries` через `searchItems` поверх отфильтрованного окна.
- **275–299** — рендер: панель фильтров вместо legacy-чипов, когда есть URL-backed state; ниже — блок результатов поиска («Найдено: N» + до 20 сниппетов + «… +остальные»).

**`debugger/src/AgentRuns.tsx`**:
- **21–22** — импорты `parseTraceState`/`stripTraceParams`/`traceStateQuery`/`TraceUrlState` и `useDebouncedValue`.
- **222–239** — URL-состояние (§8): `traceState` инициализируется из `window.location.search`, коммитится через `useDebouncedValue` (≥300 мс), effect пишет `trace*`-параметры через `history.replaceState` (ссылка восстанавливает отфильтрованный вид; пустое состояние — чистит URL; run/view-параметры нетронуты).
- **1335–1336** — передача `filterState={committedTraceState}` / `onFilterState={setTraceState}` в `TraceLayers` (владелец состояния — вкладка «Запуски»).

**`debugger/src/locales/ru.ts`** **2319–2339** и **`debugger/src/locales/en.ts`** **2319–2339** — по 21 ключу: `runs.filters.category|type|status|participant|duration`, `runs.filters.status.ok|error|cancelled`, `runs.filters.who.model|tool|human|system`, `runs.filters.duration.any|1s|5s|30s`, `runs.filters.search_placeholder`, `runs.filters.search_hint` (явно: серверный поиск по выводу недоступен, вывод не хранится), `runs.filters.results`, `runs.filters.reset`, `runs.chip.my_input`.

### Минимальные фиксы реализации, найденные при доводке (уже в коммите кода)

1. Импорт `eventInfo` из `eventCatalog` (traceFilters.ts:3) — альтер-локальные заголовки каталога берутся из каталога, а не дублируются.
2. Type-guard `statusOfItem` (traceFilters.ts:59–67) — сужение union `TraceItem` без кастов; tsc strict чистый.
3. Guard пустой saga в `searchItems` (traceFilters.ts:284) — `item.events.length === 0` → continue; пустая saga не дает ложных совпадений и не падает на `events[0]`.

---

## (б) Прогоны — точные итоговые строки (все выполнены на HEAD=49ae942 + эти правки, worktree trace-runs-tab)

Полный vitest-сьют (все 42 файла, включая 30+5+3 новых теста T3):

```
 Test Files  42 passed (42)
      Tests  423 passed (423)
```

Статический анализ типов:

```
$ npx tsc -b --force
TSC_CLEAN          # (echo после команды; сам tsc -b --force не вывел ни одной ошибки)
```

Продакшн-сборка:

```
$ npm run build        # = tsc -b && vite build
✓ 1272 modules transformed.
dist/assets/index-BrZoK5ac.js   1,803.61 kB │ gzip: 482.40 kB
✓ built in 1.15s
```

Предупреждение `(!) Some chunks are larger than 500 kB after minification` — **предсуществующее**, к зоне T3 не относится (размер основного бандла debugger, включая CSS 1 MB, вырос задолго до T3; предупреждение присутствует и на родителе 49ae942), на «built in …» и код выхода 0 не влияет.

---

## (в) Статический анализ

- **tsc**: `npx tsc -b --force` — чисто (см. (б)); строгая типизация, включая новые union-гарды (`statusOfItem`, `ConsoleStateLike`).
- **ESLint**: в проекте не настроен — `debugger/package.json` не содержит ни `lint`-скрипта (`{"dev","build","preview","test","test:watch"}`), ни ESLint-конфига/зависимости. Допустимо: статический анализ ограничен tsc, что соответствует конфигурации репозитория.

---

## (г) Таблица статусов

| # | Пункт | Статус |
|---|-------|--------|
| 1 | Реализация §8: 5 измерений структурного фильтра (категория/тип/статус/участник/длительность) — `traceFilters.ts` (`TraceFilter`, `matchesFilter`) + панель | ✅ сделано и проверено |
| 2 | Реализация §8: 4 быстрых чипа (`errors`, `tools`, `human`, `myInput`) — `traceModel.matchesChip` + `matchesChipSet` + панель | ✅ сделано и проверено |
| 3 | Реализация §8: текстовый поиск по трассе (haystack: заголовки обоих локалов, детали, факты, payload; сниппеты) | ✅ сделано и проверено |
| 4 | Реализация §8: состояние фильтров/поиска в URL (`trace*`-параметры, round-trip, деградация, чистка) — `AgentRuns.tsx` + `parseTraceState`/`traceStateQuery` | ✅ сделано и проверено |
| 5 | Реализация NFR-9: дебаунс 300 мс (`useDebouncedValue`, `TRACE_DEBOUNCE_MS`) | ✅ сделано и проверено |
| 6 | Perf-guard 0-fetch-на-символ: поиск/фильтрация только по загруженному окну, сеть не трогается | ✅ сделано и проверено |
| 7 | Тесты модуля фильтров/поиска — 30 (`traceFilters.test.ts`) | ✅ сделано и проверено |
| 8 | Тесты дебаунса — 5 (`useDebouncedValue.test.tsx`) | ✅ сделано и проверено |
| 9 | Perf-guard тесты — 3 (`traceFilters.perf.test.ts`) | ✅ сделано и проверено |
| 10 | Полный vitest-сьют: `Test Files 42 passed (42) / Tests 423 passed (423)` — регрессий нет | ✅ сделано и проверено |
| 11 | `npx tsc -b --force` — без ошибок типов | ✅ сделано и проверено |
| 12 | `npm run build` — успешно; предупреждение chunk>500 kB предсуществующее, вне зоны T3 | ✅ сделано и проверено |
| 13 | Поиск по выводу команд — только в пределах загруженного клиентского буфера (2000 строк / 256 KiB tail, §11.2); серверный поиск недоступен: вывод не хранится (§11.1); без постоянного хранения; лимит явно показан в UI (`runs.filters.search_hint`) | ✅ сделано и проверено |
| 14 | Секреты: `secret.*`-строки не отдают payload в haystack/поиск (только имя/статус) — инвариант secret.* | ✅ сделано и проверено |
| 15 | ESLint | ⛔ не сделано (и не требуется): в проекте ESLint не настроен, lint-скрипта в package.json нет — допустимо |
| 16 | Ручная проверка в браузере (визуальная прокрутка отфильтрованной трассы) | ⚠️ сделано, но не проверено — UI-компонент покрыт модульными тестами, интерактивный прогон в браузере не выполнялся в рамках этой задачи |
