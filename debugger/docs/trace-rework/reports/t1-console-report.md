# T1 — backend консольного вывода run_command: отчёт

Ветка `feature/trace-runs-tab-v2`; реализация Т1 закоммичена поверх wip 8483ede.
Коммиты: `6ac254a` feat(console), + отдельный docs-коммит с этим отчётом.
Зона T4 (`debugger/src/*`) остаётся незакоммиченной — не входит в данный отчёт.

## (а) Сводка Файл:Строка и фиксы

**kernel/agent/console.go** (369 строк, ядро реестра)
- `Console` (L29), бюджеты стрима `consoleTailBytes`=256 KiB / `consoleTailLines`=2000
  (§11.2: defaults задокументированы, не финализированы), idle-свип 30 мин,
  буфер подписчика 512 кадров с неблокирующим drop.
- `Hook` L130 — OnOutput-коллбек для sandbox.Request; nil → классическое ожидание.
- `Write` L142 — append в tail + fan-out подписчикам (медленный потребитель
  отбрасывает кадры, выходной путь команды никогда не блокируется).
- `Done` L175 — идемпотентно, закрывает каналы подписчиков (done-фрейм в SSE).
- `Snapshot` L200 — REST-проекция: tails, bytes/lines, dropped_*/truncated, done+exit_code.
- `Subscribe` L237 — live-тап; подписка до первого чанка (lazy-UI §6) регистрирует
  запись, а не отвергается; tail-replay для reconnect.
- `op` L279, `sweep` L300 — ленивое создание, свип простаивающих записей.
- `consoleStream.append` L330 — tail = последние ≤256 KiB целых строк,
  вытесненное учитывается (droppedBytes/droppedLines), хвост — приватная копия.
- **Фикс №5 (data race, основной)**: `op.lastSeen` писался под разными
  мьютексами — в `op()` (~L285) и `Subscribe()` под `c.mu`, тогда как в
  `Write()` (L157→L160) и `Done()` (L194) под `op.mu`; `go test -race`
  ловил ровно пару console.go:285 vs :157. Выбран вариант A: все обращения
  к `lastSeen` (Write/Done/Subscribe/op/sweep) — только под `op.mu`;
  в `op()`/`Subscribe()` захват `op.mu` перенесён строго после освобождения
  `c.mu` — единый порядок `c.mu → op.mu` во всём файле, инверсии нет,
  свип читает `lastSeen` согласованно под `op.mu`. Контракт
  (Hook/Done/Snapshot/Subscribe) не менялся.
- Предыдущие фиксы (№1–4, вошли тем же коммитом): Subscribe регистрирует
  неизвестную операцию (lazy-поток §6); `?tail` парсится `strconv.ParseBool`
  (клиент шлёт `tail=1`); done-фрейм несёт реальный `exit_code`; роль в
  gate-токене исправлена на `reader:reader:*` (тестовая инфраструктура).

**kernel/agent/console_test.go** — юнит- и конкурентные тесты реестра,
включая `TestConsoleConcurrentWritesAreRaceFree` (тот, что ловил гонку).

**cmd/agent-kernel/consoleapi.go** — REST: GET-снимок (L43 Snapshot),
GET-стрим (L72 Subscribe; L81–83 кадры stdout/stderr; L92 done-фрейм),
`writeSSE` L111 — кадры `event: stdout|stderr|done`, JSON-payload.

**cmd/agent-kernel/consoleapi_test.go** — httptest-покрытие стрима:
replay-backlog, tail=true/1, done с exit_code, подписка до первого чанка,
чужой проект не видит стрим, ошибка как framed event (после SSE-заголовков).

**kernel/agent/activities.go** — L115–119 поле `Console` (nil ⇒ реестра нет,
история: journal + ExitCode); L826–837: при активном реестре `OnOutput =
Console.Hook(...)`, после завершения `Console.Done(..., ExitCode)`.

**cmd/agent-kernel/main.go** — L511 создание реестра; L1075–1078 регистрация
маршрутов `/v1/agent/console/{operationID}` (снимок + стрим).

**kernel/sandbox/docker.go** — L92–99 поле `OnOutput` в sandbox.Request;
L349 проброс чанков stdout/stderr в коллбек; L640: обрезка лога в runner
не конфликтует с консольным tail (у tail свой, больший бюджет).

## (б) Прогоны (точные итоговые строки)

До фикса (воспроизведение):
```
WARNING: DATA RACE  Write at ... console.go:285 (op)  vs  console.go:157 (Write)
--- FAIL: TestConsoleConcurrentWritesAreRaceFree (0.00s)
FAIL	github.com/temporality-project/temporality/kernel/agent	0.030s
```
После фикса:
```
go build ./...                                          → BUILD_OK
go vet ./kernel/agent/... ./cmd/agent-kernel/...        → VET_OK
go test -race ./kernel/agent/ -run TestConsole          → ok ... 1.025s
go test -race ./cmd/agent-kernel/ -run TestConsole      → ok ... 1.860s
go test ./cmd/agent-kernel/          (полный пакет)     → ok ... 14.213s
```

## (в) vet / gofmt

- `go vet ./kernel/agent/... ./cmd/agent-kernel/...` — чисто, замечаний нет.
- `gofmt -l` по зоне T1 — пусто; `go fmt ./kernel/agent/` — правок не внёс.

## (г) Итог

| Что | Статус |
|---|---|
| Реестр Console: write/tail/fan-out/sweep, бюджеты 256 KiB/2000 строк | сделано и проверено (юнит+race) |
| REST-снимок GET /v1/agent/console/{op} | сделано и проверено (пакетные тесты) |
| SSE-стрим: stdout/stderr/done кадры, replay, lazy-подписка, tail=1 | сделано и проверено (пакетные тесты) |
| Data race на op.lastSeen (вариант A: всё под op.mu, порядок c.mu→op.mu) | сделано и проверено (-race зелёный ×2) |
| Wiring: activities.Hook/Done, main-роуты, docker.OnOutput | сделано и проверено (build + полный пакет) |
| E2E через браузерный клиент consoleStream.ts (T4, debugger/src) | сделано, но не проверено — вне зоны T1, зона T4 не коммитилась |
| Поведение свипа в прод-таймингах (30 мин idle) | сделано, но не проверено — только юнит с инжекцией часов |
| Персистентность stdout/stderr | не сделано — отложено заказчиком до аудита приватности |
| Redaction-проекция стрима | не сделано — заказчик: не требуется (секретов в песочнице нет) |

Ограничения: single-process топология (activity worker и HTTP-сервер в одном
процессе, реестр в памяти — без брокера); гранулярность — два цельных
tail-буфера stdout/stderr, построчной фильтрации нет; при переполнении —
drop-oldest с сохранением счётчиков (dropped_bytes/dropped_lines/truncated),
медленным подписчикам кадры отбрасываются без ожидания.
