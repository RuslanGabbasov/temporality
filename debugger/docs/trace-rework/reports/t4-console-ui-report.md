# T4 — UI консольного вывода: отчёт реализации (доводка)

Статус: **реализовано, все известные дефекты исправлены, проверено**. Ветка `feature/trace-runs-tab-v2`,
worktree `/workspace/worktrees/trace-runs-tab`, база — HEAD `7660cd0` + незакоммиченные правки зоны T4
(ConsoleOutput.tsx, AgentRuns.tsx, TraceLayers.tsx, новый ConsoleOutput.test.tsx), поданные двумя коммитами
(`feat(console)` — код+тесты, `docs(console)` — этот отчёт). `/workspace/temporality` не тронут, реbase не выполнялся.

DoR: выполнен (бизнес-контекст §6/§8 v2-consolidated.md, DoD-5/NFR-9; URI-схема `console://<operation_id>`
утверждена, `artifact://` зарезервирован реестром артефактов). Доводка велась строго в зоне T4:
исправлены 3 предписанных дефекта + 2 дефекта, вскрытых прогонами (оба — в том же новом тестовом файле;
код продукта сверх предсуществующих правок зоны не менялся).

---

## Исправленные дефекты (суть, 1 строка каждый)

| # | Где | Суть |
|---|-----|------|
| 1 | `TraceLayers.tsx:7` | Добавлен недостающий `import type { ConsoleState } from './consoleStream'` (тип использовался в props до фикса). |
| 2 | `ConsoleOutput.test.tsx:31` | Объявлены `let root: Root; let container: HTMLElement;` — тесты падали на `ReferenceError` до первого присваивания в `beforeEach`. |
| 3 | `ConsoleOutput.test.tsx:163–192` | Неработоспособный visibility-тест через `vi.stubGlobal('document', …)` заменён на рабочий паттерн: `Object.defineProperty(document, 'hidden', { value: true/false, configurable: true })` + `document.dispatchEvent(new Event('visibilitychange'))` — слушатель компонента висит на реальном `document`, поэтому стаб глобала до него не доставал; исходный дескриптор `hidden` сохраняется и восстанавливается после теста. |
| 4 | `ConsoleOutput.test.tsx:76–79` *(вскрыт прогоном, фикс минимальный)* | Стаб `vi.stubGlobal('navigator', { ...navigator, clipboard })` затирал `navigator.language` (не own-enumerable, а с прототипа) → `I18nProvider` падал в `localeFromTag(undefined).split` при монтировании. В стаб добавлен явный `language: navigator.language`. |
| 5 | `ConsoleOutput.test.tsx:57–67` *(вскрыт `npm run build`, фикс минимальный)* | `renderConsole` спредил `Partial<ConsoleOutputProps>` поверх обязательного `live: boolean` → TS2322 (`boolean | undefined`), который vitest не ловит (не тайпчекает). `live` теперь деструктурируется с дефолтом `false` и передаётся явно, остаток — спредом. |

---

## (а) Сводка изменений — Файл:Строка

### Файлы зоны T4 (предсуществующие правки доводки, поданы в `feat(console)`)

**`debugger/src/ConsoleOutput.tsx`**:
- **5** — импорт из `consoleStream` свёрнут в одну строку (без изменения множества имён).
- **46** — `ConsoleOutputProps` экспортирован (нужен тесту для `Parameters<typeof ConsoleOutput>[0]`).
- **49–58** — докблоки контракта: `live` = «выбранный ран ещё live» (гейт SSE-подписки, NFR-9), `onStateLoaded?` — публикация каждого загруженного состояния буфера владельцу (§8/NFR-9: поиск не fetch-ит).
- **61** — сигнатура компонента принимает `onStateLoaded`.
- **84–90** — publish-эффект: после загрузки tail и на каждом стрим-кадре состояние уходит владельцу; identity-стабильно (лишних ре-рендеров владельца нет).
- **97–127** — live-гейт подписки: в `.then` тейла `if (!live || tail.done) return` перед `subscribeConsoleStream`; `live` добавлен в deps эффекта (снятие выделения/завершение рана перезапускает эффект — свежий tail без подписки).

**`debugger/src/AgentRuns.tsx`**:
- **29** — `import type { ConsoleState } from './consoleStream'`.
- **386–399** — `consoleStates: ReadonlyMap<operationId, ConsoleState>` + `collectConsoleState` (identity-stable: то же состояние не создаёт новый map → без лишних ре-рендеров).
- **597–598** — очистка `consoleStates` при смене выбранного рана: буферы чужого рана не переживают переключение.
- **1352–1353** — проброс `consoleStates`/`onConsoleState` в `TraceLayers`.

**`debugger/src/TraceLayers.tsx`**:
- **7** — импорт `type ConsoleState` (дефект №1).
- **39–42** — `onConsoleState?` в `EventRowVisualProps` (докблок: владелец индексирует буфер для текстового поиска, без fetch).
- **66, 110–121** — `EventLayeredDetails` пробрасывает `onStateLoaded={onConsoleState}` в слот `ConsoleOutput` (монтируется только при открытых деталях, включая строки внутри раскрытых групп).
- **163, 193** — `TraceEventRow` пробрасывает колбэк дальше.
- **229, 247–251** — props `TraceLayers`: `consoleStates?` (уже был) + `onConsoleState?`.
- **338, 383** — проброс в обеих ветвях рендера: строки раскрытой группы (338) и одиночные события (383). Свёрнутая группа строки не монтирует — `ConsoleOutput` не создан, буфер не публикуется, что корректно: поиск покрывает только реально загруженные буферы (NFR-9).

**`debugger/src/ConsoleOutput.test.tsx`** (новый, ~255 строк, 11 тестов; jsdom-окружение файла, реальный `consoleStream.subscribeConsoleStream` поверх замоканного `fetch`):
- компонентные (8): монтирование из tail + `console://`/не-`artifact://` (86), stdout/stderr-кадры в раздельные табы + done/exit_code (96), один AbortController на tail+stream с абортом на unmount (123), нет подписки для не-live события (146), `document.hidden`-пауза/возобновление (163), публикация состояния для §8-индекса (193), поверхность ошибки недоступного эндпоинта (205), копирование буфера и URI (212);
- чистые границы (3): `consoleLines` — подсветка ошибок, без фантомной пустой строки (234), `shouldAutoScroll` — порог 24px (242), `CONSOLE_LINE_LIMIT === 2000` запинен (249).

---

## Live-гейт: одна подписка на раскрытое live-событие — как гейтится

Подписка целиком живёт внутри одного эффекта с deps `[operationId, project, live, applyFrame, stop, visibleTick]`:
1. Раскрытие события → эффект глушит прежний контроллер (`stop()`), создаёт **один** `AbortController` и тянет `fetchConsoleTail`; в `.then` — `setState(tail)` и гейт **`if (!live || tail.done) return`** перед `subscribeConsoleStream`: SSE открывается только когда событие принадлежит выбранному рану, который ещё live, и операция ещё не завершена (tail — уже ответ).
2. `live` — это `selectedLive` владельца (`AgentRuns`: `run.started` без `run.completed/failed/cancelled` для выбранного рана). Снятие выделения или завершение рана меняет `live` → эффект перезапускается: свежий tail, подписки нет.
3. Схлопывание/unmount/скрытие таба → cleanup-`stop()` → abort того же контроллера: и tail, и stream гасятся одним движением; повторное раскрытие live-события пересоздаёт подписку, свежий tail покрывает пропуск.
4. Скрытая вкладка дополнительно гейтится ранним `if (document.hidden) return` в том же эффекте (см. ниже).

Итог NFR-9: ровно одна подписка на раскрытое live-событие; settled/невыбранные события держат только tail. Тесты: 96 (кадры+done), 123 (abort одного контроллера), 146 (`live:false` → `streamOpened === 0`).

## document.hidden-пауза

Слушатель `visibilitychange` вешается на **реальный** `document` (эффект с deps `[stop]`). На скрытие — `stop()`: контроллер подписки абортится (SSE-ридер выходит по `isAbortError` без поверхности ошибки). На возврат видимости — `setVisibleTick(tick + 1)`: `visibleTick` — deps эффекта подписки, поэтому эффект молча пересоздаёт подписку **свежим tail-запросом**, который покрывает всё, что прошло за время паузы (SSE `/stream` при этом переоткрывается с `tail=1` только для live-несовершённой операции — гейт из предыдущего раздела). Тест 163 теперь проверяет это через реальный `document`: `Object.defineProperty(document, 'hidden', …)` + `dispatchEvent(new Event('visibilitychange'))`, ассерт на `signal.aborted` и повторный `TAIL_URL`; исходный дескриптор восстанавливается.

## Буфер 2000 строк / 256 КиБ

Клиентский инвариант держит `appendTail` (`consoleStream.ts`): дроп всегда с **старейшего** конца (консоль показывает хвост); строковый лимит (2000) режет целыми строками, байтовый (256 КиБ) — по границам символов (мультибайтовый символ не разрезается); счётчики `droppedLines`/`droppedBytes` — точные, клиентские, для маркера «хвост укорочен»; серверный флаг `truncated` помечает «объём неизвестен точно» без выдуманных чисел. Клиентский лимит применяется и к серверному tail (`consoleStateFromTail`), поэтому инвариант 2000/256 КиБ держится даже если серверный бюджет больше. `CONSOLE_LINE_LIMIT = 2000` экспортирован и запинен тестами (ConsoleOutput.test 249, consoleStream.test 31/42).

## Адресация `console://` и запрет `artifact://`

`consoleURI(operationId)` — единственная точка конструирования адреса (`console://<operation_id>`); UI показывает его и отдаёт кнопке копирования. `consoleStateFromTail` принимает `console_uri` из payload **только** с префиксом `console://` — любая другая схема (включая `artifact://`) отбрасывается и заменяется каноническим конструктором, так что чужая схема не проходит даже от пира. `artifact://` зарезервирован реестром артефактов и для консольного вывода не используется. Тесты: consoleStream.test 19/23, ConsoleOutput.test 86 (текст содержит `console://op-42`, не содержит `artifact://`) и 212 (копирование URI).

---

## (б) Прогоны — точные итоговые строки (HEAD `7660cd0` + эти правки, worktree trace-runs-tab)

Целевой прогон (новые/затронутые файлы T4):

```
$ NODE_OPTIONS=--experimental-require-module npx vitest run src/ConsoleOutput.test.tsx src/consoleStream.test.ts

 Test Files  2 passed (2)
      Tests  25 passed (25)
```

Полный vitest-сьют (все 43 файла = база 42 + ConsoleOutput.test.tsx; 434 = 423 + 11 новых тестов):

```
$ NODE_OPTIONS=--experimental-require-module npx vitest run

 Test Files  43 passed (43)
      Tests  434 passed (434)
```

Продакшн-сборка и типы:

```
$ npm run build        # = tsc -b && vite build
✓ built in 1.00s

$ npx tsc -b --force
TSC_CLEAN              # (echo после команды; сам tsc -b --force не вывел ни одной ошибки)
```

Промежуточные падения в ходе доводки (оба устранены, финальные прогоны выше — чистые): фикс №4 вскрылся целевым vitest-прогоном (1 failed: `localeFromTag` на затёртом `navigator.language`), фикс №5 — сборкой (`TS2322` в тестовом файле).

---

## (в) Статический анализ

- **tsc**: `npx tsc -b --force` — чисто (см. (б)).
- **ESLint**: в проекте не настроен — `debugger/package.json` не содержит `lint`-скрипта и ESLint-конфига/зависимости (та же конфигурация, что зафиксирована в отчёте T3). Статический анализ ограничен tsc — соответствует репозиторию.

---

## (г) Таблица статусов

| # | Пункт | Статус |
|---|-------|--------|
| 1 | Фикс №1: `import type { ConsoleState }` в TraceLayers.tsx | ✅ сделано и проверено |
| 2 | Фикс №2: объявления `root`/`container` в ConsoleOutput.test.tsx | ✅ сделано и проверено |
| 3 | Фикс №3: visibility-тест на реальном document (`defineProperty` + `visibilitychange`) с восстановлением дескриптора | ✅ сделано и проверено |
| 4 | Фикс №4 (вскрыт прогоном): `navigator.language` сохранён в стабе | ✅ сделано и проверено |
| 5 | Фикс №5 (вскрыт build): TS2322 в `renderConsole` — `live` с дефолтом, без спреда поверх обязательного пропса | ✅ сделано и проверено |
| 6 | Live-гейт подписки: `if (!live \|\| tail.done) return` в `.then` тейла, `live` в deps; одна подписка на раскрытое live-событие (NFR-9) | ✅ сделано и проверено |
| 7 | Публикация `onStateLoaded` (tail + каждый кадр, identity-stable) + сборка `consoleStates` в AgentRuns + очистка при смене рана | ✅ сделано и проверено |
| 8 | `document.hidden`-пауза: abort на скрытие, свежий tail при возврате | ✅ сделано и проверено |
| 9 | Буфер 2000 строк / 256 КиБ: клиентский лимитер, счётчики дропов, лимит применён и к серверному tail | ✅ сделано и проверено |
| 10 | Адресация `console://<operation_id>`; `artifact://` не проходит ни конструктором, ни из payload | ✅ сделано и проверено |
| 11 | Новый ConsoleOutput.test.tsx: 11 тестов (8 компонентных + 3 чистые границы) | ✅ сделано и проверено |
| 12 | Целевой прогон: `Test Files 2 passed (2) / Tests 25 passed (25)` | ✅ сделано и проверено |
| 13 | Полный vitest: `Test Files 43 passed (43) / Tests 434 passed (434)` — регрессий нет | ✅ сделано и проверено |
| 14 | `npm run build` — успешно | ✅ сделано и проверено |
| 15 | `npx tsc -b --force` — без ошибок типов | ✅ сделано и проверено |
| 16 | Дифф локален: изменены только 4 файла зоны T4; `/workspace/temporality` не тронут, реbase не выполнялся | ✅ сделано и проверено |
| 17 | ESLint | ⛔ не сделано (и не требуется): в проекте ESLint не настроен — допустимо |
| 18 | Ручная интерактивная проверка в браузере (живой стрим реального рана, пауза по скрытию таба) | ⚠️ не выполнялась в этой задаче — поведение покрыто модульными тестами (11) на jsdom с реальным `subscribeConsoleStream` |

## Отклонения от предписания

Единственное: помимо трёх предписанных дефектов вскрыты и минимально исправлены два дополнительных (№4, №5) —
оба в том же новом тестовом файле `ConsoleOutput.test.tsx`, что прямо разрешено предписанием («если при прогонах
всплывут ещё ошибки — чини минимально, фиксируй каждую в отчёте»). Код продукта сверх предсуществующих правок
зоны T4 не менялся; скоуп не расширялся; диагностика заново не проводилась.
