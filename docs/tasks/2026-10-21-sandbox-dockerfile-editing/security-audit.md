# Подтверждающий ре-аудит: редактирование полного Dockerfile профиля песочницы

Дата: 2026-10-21 (подтверждающий проход после доработки). Аудитор: Security Auditor (DevSecOps / principal audit).
Спецификация: `docs/tasks/2026-10-21-sandbox-dockerfile-editing/spec.md` (I-3, D2.2, D3, D7). Предыдущий отчёт: этот файл (проход до доработки: 3 High открыто).
Область: заявленные исправления **F-01** (нормализация BOM/CRLF + запрет любой директива-формы `# key=` до первой инструкции), **F-02** (склейка line-continuation до флаг-проверки RUN), **F-03** (запрет `--security=insecure|privileged`), **F-04** (`--mount` только `type=cache`), **F-05** (verifyRunArgs: `--entrypoint true` + изоляция runtime-профиля) в **текущем дереве**.
Метод: статический аудит (`kernel/sandbox/override.go`, `kernel/sandbox/build.go`, `kernel/sandbox/docker.go`, `workspace/sandboxprofiles.go`), **пробный корпус обходов (17 сценариев × 2 зеркала валидатора)**, запущенный через `go test -overlay` из `/scratch` (без единого файла в репозитории), сверка семантики парсера Dockerfile с upstream `moby/buildkit@master` `frontend/dockerfile/parser/parser.go` (web_fetch), сборка/тесты проекта. **PoC приведены только текстом и не исполнялись.**

> ⚠️ Дерево менялось параллельными работниками прямо во время прохода (снимки 13:26 → 13:40): на промежуточном снимке были зафиксированы два дефекта (мёртвый флаг-скан `runFlagViolations` — `break` на токене `RUN`; паника `match[1]` в ветке `directiveFormComment` без capture-группы). К финальному снимку оба исправлены (см. §1 и F-09); итоговые статусы вынесены по **финальному** состоянию, повторно верифированному.

---

## 🛡️ ВЕРДИКТ

# VULNERABLE

**Вердикт по I-3: ТРЕБУЕТ ИСПРАВЛЕНИЙ** (гейт AC-11 закрыть нельзя).
Три прежних High-обхода (F-01, F-02, F-05) устранены или сведены к принятым-с-обоснованием. Открытыми остаются: **F-04** (инвариант «только type=cache» обходится дублированием ключа `type=`), **F-06** (инъекция в логи сборки, Medium — не входила в заявленные исправления), **F-08** (тесты пакета `workspace` не компилируются → `go test ./...` красный — гейт верификации не пройден).

**Счётчики (F-01…F-08): закрыто 4 (F-01, F-02, F-03, F-07-как-вектор-обхода), открыто 3 (F-04, F-06, F-08), принято-с-обоснованием 1 (F-05).** Плюс ранее принятые-с-обоснованием P-01 (D2.2) и P-02 (класс writer+) — статусы не менялись.

---

## 1. Статус заявленных исправлений (финальный снимок)

| Заявлено | Что есть в коде | Вердикт |
|---|---|---|
| **F-01** нормализация BOM/CRLF, запрет любой `# key=` формы до первой инструкции | `kernel/sandbox/override.go:42` (`normalizeDockerfileText`: снятие `\xEF\xBB\xBF`, `\r\n`→`\n`), `override.go:27` (`parserDirective`), `override.go:34` (`directiveFormComment` с capture-группой), запрет `# syntax/escape=` — по всем top-level-комментариям; зеркало `workspace/sandboxprofiles.go:251` | **закрыто**: пробы BOM (`\ufeff# syntax=`), CRLF, `\t`/пробелы, регистр, `#\tkey=`, отступ перед `#` — отклоняются; двойной BOM и NBSP-варианты (`#\u00a0syntax=`, `# syntax\u00a0=`) валидатор пропускает, **но buildkit их тоже не исполняет** (см. §4) — не вектор обхода |
| **F-02** continuation-склейка до флаг-проверки | `override.go:98` (`parseDockerfileInstructions` — байт-конкатенация сегментов, пустые строки и комментарии внутри continuation пропускаются), `override.go:150` (`trimDockerfileContinuation`), `override.go:287` (`runFlagViolations` по склеенному тексту; `tokens[1:]` — после ключевого слова инструкции); зеркало `workspace/sandboxprofiles.go:455` | **закрыто**: `RUN --network=ho\<перенос>st`, `--net\<перенос>work=host`, пустая continuation-строка, `R\<перенос>UN --network=host` — все отклоняются (подтверждено пробами и юнит-тестами) |
| **F-03** запрет `--security=insecure\|privileged` | `override.go:326`; зеркало `workspace/sandboxprofiles.go:477` | **закрыто**: регистр/кавычки/continuation покрыты; `--security=unconfined` и форма `--security insecure` (без `=`) валидатор пропускает, но buildkit такие значения отвергает/игнорирует (не-эквивалент host-привилегий) — hardening-замечание в F-04 |
| **F-04** `--mount` только `type=cache` | `override.go:330`, `override.go:341` (`isCacheMount` — «есть опция type=cache»); зеркало `workspace/sandboxprofiles.go:500` | **частично закрыто → открыто**: дублирование ключа `type=` (`type=cache,type=bind`) проходит проверку (F-04a) |
| **F-05** verifyRunArgs: `--entrypoint true` + изоляция runtime-флагов | `kernel/sandbox/build.go:456-462`: `--network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges --pids-limit 512 --user 65534:65534 --entrypoint true --pull=never`; зеркало контура `kernel/sandbox/docker.go:463` | **принято-с-обоснованием** (см. §3): probe изолирован не слабее runtime-профиля; остаточные оговорки ниже |

Дополнительно (пробный корпус, поведение идентично в обоих зеркалах): `# key=`/`# syntax=` в heredoc-теле отклоняются (ложные срабатывания — F-07, не обход); `# syntax=` после первой инструкции отклоняется (buildkit директивы после инструкций не исполняет — over-block); `RUN echo --network=host` принимается корректно (это данные команды); директива через continuation-разрыв комментария (`# synt\` + `ax=evil`) валидатором **принимается, но и buildkit её не исполняет** (сверено с `parser.go`: директива матчится по физической строке до склейки, `possibleParserDirective` вызывается в `processLine`; склеенный текст уходит в `newNodeFromLine` → `parseIgnore`) — не вектор обхода.

---

## 2. Таблица находок

| ID | Критичность | Вектор | Где в коде | Рекомендация | Статус |
|----|-------------|--------|------------|--------------|--------|
| F-01 | High (был CVSS 8.0) | BOM/директива-форма прячут `# syntax=` от валидатора → pull+исполнение произвольного frontend-образа | `kernel/sandbox/override.go:27,34,42` | (сделано) нормализация + запрет `# key=` до первой инструкции | **закрыто** |
| F-02 | High (был CVSS 8.0) | `--network=ho\`+`st`: разрыв continuation внутри токена | `kernel/sandbox/override.go:98,150,287` | (сделано) склейка continuation по семантике парсера до флаг-проверки | **закрыто** |
| F-03 | Medium (был CVSS 6.0) | `RUN --security=insecure` | `kernel/sandbox/override.go:326` | (сделано) deny-list insecure/privileged; дополнительно: allowlist `default/sandbox` (hardening) | **закрыто** |
| F-04a | **Low** (CVSS 3.5) | `--mount` с **дублированным ключом `type=`**: `RUN --mount=type=cache,type=bind,from=base,target=/x cmd` (или `type=cache,type=secret`). `isCacheMount` (`override.go:341`) истинна при наличии *любой* опции `type=cache`; эффективный тип определяет разбор buildkit (duplicate-key семантика: присваивание в switch → вероятен last-wins) | `kernel/sandbox/override.go:330,341`; зеркало `workspace/sandboxprofiles.go:500` | Строгий разбор опций: ровно один ключ `type=`, значение `cache` (после снятия кавычек/фолда регистра); отвергать дубли любого ключа; allowlist опций cache-mount (`target,id,sharing,mode,uid,gid`) | **открыто** |
| F-04b | **Low** (CVSS 3.5) | Cache-отравление: `type=cache` с произвольным `id=` — общий cache id между сборками разных профилей | там же | Нормализовать/префиксовать `id` идентификатором профиля | **открыто (принято-с-обоснованием-кандидат)**: фактический риск ограничен контуром сборки профилей (writer+), но заявленный инвариант «только type=cache» этого не покрывает |
| F-05 | High (был CVSS 7.5) | `--entrypoint true` резолвится по PATH **образа** → код образа исполняется при верификации | `kernel/sandbox/build.go:456-462,476` | (сделано, частично) probe под полным контуром изоляции; оптимум — `docker image inspect` без запуска | **принято-с-обоснованием** (см. §3) |
| F-06 | **Medium** (CVSS 5.5) | Инъекция в логи/ошибки: сырой вывод `docker build` (mutable/внешние `FROM` = недоверенный pull-класс) вставляется в `fmt.Errorf` без санитизации control/ANSI/CRLF → подделка строк в логах, ANSI-эскейпы в терминале/`less`, разметка ошибок в UI | `kernel/sandbox/build.go:256` (`--- docker build log ---\n%s`) | strip C0/C1/DEL/ANSI, `\r`→`\n`-нормализация, лимит байт; зафиксировать в spec §2.4 | **открыто** (не входило в заявленные исправления) |
| F-07 | Low (CVSS 3.0) | Heredoc-тела трактуются валидатором как инструкции: `# syntax=`/`ADD` в теле `RUN <<EOF` дают ложный отказ (доступность/UX). Не обход: buildkit тело heredoc директивами/инструкциями не считает (цикл heredoc в `Parse` минует `processLine`) — валидатор строже | `kernel/sandbox/override.go:98-135` (нет учёта блоков `<<EOF … EOF`) | Пропускать тело heredoc-блоков при сканировании | **закрыто как вектор обхода** (подтверждено пробами `heredoc_directive`, `heredoc_add`); UX-замечание открыто |
| F-08 | **Medium** (процессное; не уязвимость рантайма) | Тест-файл не компилируется: **литеральный UTF-8 BOM внутри Go-строки** (`"M-oM-;M-?# key=value…"` = `\xEF\xBB\xBF# key=value`) → `illegal byte order mark` → `go vet`/`go test` пакета `workspace` падают на setup, кросс-тест зеркал не выполняется | `workspace/sandboxprofiles_override_test.go:143` | Заменить литеральный BOM на escape `"\ufeff"`; добавить `go vet ./...` в CI-гейт | **открыто** (блокирует гейт AC-11: «тесты зелёные» не подтверждено) |
| F-09 | — (устранено в ходе прохода) | На промежуточном снимке 13:26–13:3x: (а) `runFlagViolations` выходил по первому токену `RUN` → deny-list флагов был мёртвым кодом; (б) `match[1]` в ветке `directiveFormComment` без capture-группы → паника (DoS валидатора) на любой `# key=` форме | было `override.go:287-298,218-220` | Исправлено параллельным работником к 13:40 (`tokens[1:]`, capture-группа в `override.go:34`) | **устранено, верифицировано повторно** (пробы `generic_directive` → чистый отказ без паники; флаг-пробы отклоняются) |

### Принято-с-обоснованием

| ID | Что | Обоснование | Статус |
|----|-----|-------------|--------|
| F-05 | Код образа может исполняться на verify-probe (`true` резолвится внутри rootfs образа) | Probe запускается с контуром изоляции не слабее runtime-профиля песочницы (`docker.go:463`): `--network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges --pids-limit 512 --user 65534:65534`; образ исполняет свой код в тех же условиях, что и обычный запуск песочницы — это и есть принятый D7/P-02 класс «рецепт/внешний образ = произвольный код при сборке». Побочные эффекты исключены: `--rm`, `--pull=never`, таймаут 60s. **Оговорки:** (а) `--memory/--cpus` у probe отсутствуют (у runtime-профиля есть) — резидуальный DoS памяти хоста в окне 60s; (б) заявление «конфигурация образа не исполняется» остаётся неточным — точнее «исполняется под полной изоляцией». Hardening: `docker image inspect`/`docker create`+`rm` без запуска; либо добавить `--memory`/`--cpus` | принято-с-обоснованием |
| P-01 | D2.2: модель не может писать рецепт | Контракт `SandboxDraft` без поля рецепта (`kernel/agent/sandboxbuilder.go:21-34`), протащенное поле отбрасывается (`sandboxbuilder.go:85-93`); проверено ранее | принято-с-обоснованием / закрыто реализацией |
| P-02 | Класс доверия writer+ (рецепт = произвольный код при сборке через docker-сокет) | spec.md:65,118-122; мутации — `controlplane.RoleWriter` (`cmd/agent-kernel/sandboxprofiles.go:139,158,185,200,239,280`), чтение — RoleReader (`:122`), builtin override запрещён. **Ограничение приятия сохраняется**: оно опирается на работоспособность контролей D7 — F-01/F-02/F-05 закрыты ✓, F-04a/F-04b остаются | принято-с-обоснованием (с оговоркой на F-04) |

---

## 3. PoC (только текстом; не исполнялись)

- **F-04a**: `FROM alpine:3.20` + `\n` + `RUN --mount=type=cache,type=bind,from=base,target=/x cmd` → `ValidateDockerfileOverride`/`ValidSandboxDockerfile` возвращают nil (пробы `mount_dup_bind_cache`, `mount_dup_cache_bind` → ACCEPT в обоих зеркалах). Эффективный тип монтирования определяет разбор `--mount` в buildkit; при последовательном присваивании (`type` перезаписывается) фактическим становится `bind`/`secret` — т.е. заявленный инвариант «только type=cache» нарушается. Семантика duplicate-key в `parseMount` upstream **в этом проходе не верифицирована** (файл `instructions/commands_run.go` в master отсутствует, 404) — вектор классифицирован как вероятный, статус: открыто до строгого разбора опций.
- **F-04b**: `RUN --mount=type=cache,id=shared,target=/x cmd` в профилях A и B → общий кэш-слой письма/чтения между независимыми сборками.
- **F-06**: рецепт с mutable `FROM evil/base:latest`, в слое — вывод с `\r`/ANSI; ошибка сборки (`kernel/sandbox/build.go:256`) вставляет сырой лог в текст ошибки → подделка строк server-лога, ANSI-эскейпы в терминале/`less`.
- **F-08**: `go vet ./...` → `workspace/sandboxprofiles_override_test.go:143:4: illegal byte order mark` (литеральный U+FEFF внутри строки) → `go test ./workspace` = `setup failed`.
- **F-01 (исторический, закрыт)**: `\xEF\xBB\xBF# syntax=evil/frontend:latest\nFROM alpine\n` — теперь отклоняется (строка 1, `#syntax=`); двойной BOM `\xEF\xBB\xBF\xEF\xBB\xBF# syntax=…` валидатор принимает, но buildkit директиву не видит: `discardBOM` снимает один BOM (только `currentLine == 0`), строка не является комментарием (`isComment` требует `#`), `dispatch` не знает `\ufeff#` → `parseIgnore` — не эксплуатируется.
- **F-02 (исторический, закрыт)**: `RUN --network=ho\` + `st` (и `--net\`+`work=host`, пустая continuation-строка) → отклоняется (строка с флагом).

## 4. Сверка семантики с upstream `moby/buildkit@master` `frontend/dockerfile/parser/parser.go`

- `discardBOM` применяется только к первой строке и один раз → двойной/срединный BOM не снимается; `isComment` = «`#` после `TrimLeftFunc(unicode.IsSpace)`» (U+FEFF не пробел) → BOM-префикс скрывает комментарий и от валидатора, и от buildkit симметрично.
- Директивы матчит `possibleParserDirective` → `DirectiveParser.ParseLine` (regex `^#\s*([a-zA-Z][a-zA-Z0-9]*)\s*=(.*)`) по **физической строке до склейки continuation** → комментарий `# synt\` + `ax=evil` директивой не становится; NBSP между `#`/именем/`=` не совпадает с `\s` ни у одной из сторон.
- Склейка continuation: `trimContinuationCharacter` (`([^\esc])\esc[ \t]*$|^\esc[ \t]*$`) + `buf.Write` без разделителя, пустые continuation-строки и комментарии внутри пропускаются — семантика `parseDockerfileInstructions`/`trimDockerfileContinuation` идентична (включая случай `\\` на конце строки как литерального).
- Heredoc-тела потребляются отдельным циклом в `Parse` без `processLine` → директивами/инструкциями не считаются (валидатор строже — F-07, только UX).

## 5. Untrusted Boundaries (покрытие эшелонами)

| Точка входа | Эшелоны | Покрытие в этом проходе |
|---|---|---|
| `dockerfile_override` (PUT/POST preview/build, роль writer+) | AppSec, Supply chain, Защитное проектирование | F-01 ✓, F-02 ✓, F-03 ✓; F-04a/F-04b открыто; F-07 (UX); пробный корпус (BOM/`\r`/пробелы/табы/регистр/continuation/кавычки/heredoc/JSON-форма) |
| Модельный путь (SandboxDraft → форма) | AI/LLM (prompt-injection, утечки, Excessive Agency) | P-01: контракт без поля рецепта, игнор на сервере; base — allowlist (`BaseDefinition`), пакеты — `validPackageName` (`build.go:110`); утечек не выявлено; действие — роль writer+ от имени оператора |
| Внешние образы (`PullImage`, `FROM`, `COPY --from=`) | Supply chain, Защитное проектирование | P-02 (принято); F-05 принято-с-обоснованием (probe под изоляцией); F-06 открыто (лог сборки) |
| Вывод `docker build` → ошибки/логи/UI | AppSec (инъекция в логи) | F-06 открыто |
| Runtime запуск собранного образа | Защитное проектирование | Профиль жёсткий (`docker.go:463`), вне объёма I-3; замечаний нет |

Для LLM-части проверены эшелоны инъекций (промпт-инъекция → нет поля рецепта в контракте), утечки данных (не выявлено), Excessive Agency (модель не пишет рецепт; требуется роль writer+).

## 6. Цепочка поставок (повторная проверка манифестов)

- **Go (`go.mod`, 58 строк)**: плавающих версий нет — все require зафиксированы, `latest`/`replace`-на-ветки отсутствуют (в т.ч. pin-псевдоверсии `v0.0.0-YYYYMMDD-…`).
- **npm (`package.json`, `debugger/package.json`)**: диапазоны `^x.y.z` (плавающие minor/patch) — компенсированы lock-файлами (`package-lock.json`, `debugger/package-lock.json`); сборку выполнять только через `npm ci`; для критичных зависимостей рекомендуется точный pin.
- **Typosquatting**: признаков нет (`marked`, `react`, `@carbon/*`, `highlight.js`, `js-yaml`, `react-markdown`, `remark-*` — канонические upstream-имена). Прежнее пониженное внимание: `github.com/rostamlabs/rembed`, `github.com/gogs/chardet` — рекомендуется разовая проверка происхождения.

## 7. Верификация (что реально выполнено)

| Проверка | Результат |
|---|---|
| `go build ./...` | ✅ exit 0 |
| `go vet ./...` | ❌ **падает**: `workspace/sandboxprofiles_override_test.go:143:4: illegal byte order mark` (F-08) |
| `go test ./...` | ❌ **не зелёный**: `workspace [setup failed]` (F-08); пакеты `kernel/sandbox`, `cmd/agent-kernel`, `kernel/agent`, `controlplane`, `observation`, `skills` и др. — **ok** |
| Пробный корпус обходов (17 сценариев × 2 валидатора) | ✅ выполнен через `go test -overlay` из `/scratch` (probe-файлы вне репозитория; поведение зеркал идентично): REJECT — generic/uppercase/BOM/CRLF директивы, `# key=`, `R\`+`UN --network=host`, `# syntax=` в heredoc (F-07), `ADD` в heredoc (F-07); ACCEPT-с-обоснованием — двойной BOM, NBSP-формы, `# synt\`+`ax=` (buildkit не исполняет), `--net=host`/`--network host`/`--security=unconfined` (buildkit отвергает/игнорирует форму), `RUN echo --network=host` (данные команды); **ACCEPT как находка** — `--mount=type=cache,type=bind`, `--mount=type=cache,type=secret` (F-04a) |
| Семантика парсера Dockerfile | ✅ сверена с upstream `moby/buildkit@master` `frontend/dockerfile/parser/parser.go` (web_fetch): `discardBOM`, `isComment`, `possibleParserDirective` по физической строке, `trimContinuationCharacter`, heredoc-цикл, `parseIgnore`. Разбор `--mount` (duplicate-key) **не верифицирован**: целевой файл upstream не найден (404) — зафиксировано в F-04a |
| Статические анализаторы (semgrep/gosec/staticcheck/govulncheck) | ⚠️ не выполнялись: в песочнице отсутствуют, установка требует сети (запрещено ограничениями аудита). Эквивалент: `go vet` (падает только на F-08) + ручной аудит по эшелонам + пробный корпус |
| debugger (vitest, npm build) | ⚠️ не выполнялись: node/npm в песочнице отсутствуют (причина фиксируется по требованиям отчётности). Фронтенд-изменения в scope I-3 не входят |
| Изменения в кодовой базе | ✅ аудитором не изменён ни один файл кода; probe-файлы созданы только в `/scratch` (вне репозитория); в репозитории обновлён только этот отчёт |
| PoC | ✅ приведены только текстом; не исполнялись, не внедрялись |
| Секреты | ✅ не раскрываются (в scope не обнаружены) |

---

**Резюме для AC-11:** High-находки F-01/F-02 закрыты, F-05 сведена к принятой-с-обоснованием с изолированным probe. Гейт «заключение Security Auditor» закрыть нельзя: требуется короткая итерация — **F-04a** (строгий разбор опций `--mount` в обоих зеркалах + allowlist cache-опций, F-04b — нормализация `id` cache-mount), **F-06** (санитизация вывода сборки), **F-08** (escape BOM в тест-файле, зелёный `go vet`/`go test`), опционально hardening F-05 (`docker image inspect` либо `--memory/--cpus`) и allowlist `--security`. После — повторный короткий ре-аудит.
