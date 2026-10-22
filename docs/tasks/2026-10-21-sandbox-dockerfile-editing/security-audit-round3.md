# Подтверждающий ре-аудит, раунд 3: редактирование полного Dockerfile профиля песочницы

Дата: 2026-10-21 (короткий подтверждающий проход). Аудитор: Security Auditor (DevSecOps / principal audit).
Дерево: `223eb0a` + незакоммиченная работа (снимок во время прохода). Предыдущий вердикт: `security-audit.md` — VULNERABLE (открыты F-04a Low, F-06 Medium, F-08 Medium).
Метод: статический аудит изменённых файлов (`kernel/sandbox/override.go`, `kernel/sandbox/build.go`, `workspace/sandboxprofiles.go` + тесты), сверка зеркал валидаторов, пробный корпус обходов (40 сценариев × 2 зеркала + 11 сценариев санитизации) через `go test -overlay` из `/scratch` (ни одного файла в репозитории), сборка/тесты/vet. **PoC — только текстом, не исполнялись.**

---

## 🛡️ ВЕРДИКТ

# SECURE

**Вердикт по AC-11: HARDENED.** Все открытые находки раунда 2 закрыты: F-04a (строгий разбор `--mount` в обоих зеркалах), F-06 (санитизация вывода docker при захвате + контракт D8/I-4 в spec.md), F-08 (escape `"\ufeff"` вместо литерального BOM, vet/тесты зелёные). Регрессий F-01/F-02/F-03/F-05 нет (подтверждено пробами). Критических рисков не осталось; остатки — только принятые-с-обоснованием (F-04b, F-05, P-01, P-02, daemon-класс F-06 в `docker.go`) — см. §2.

⚠️ Одна оговорка к тестируемости: `go vet ./...` / `go test ./cmd/agent-kernel/` в этом снимке **красные из-за чужого незакоммиченного диффа** (`cmd/agent-kernel/temporalrpc_test.go` не компилируется; дерево меняется параллельными работниками). К I-3/F-08 это отношения не имеет: F-08 касался пакета `workspace`, и его тесты теперь компилируются и зелёные. Гейт «тесты зелёные» по scope аудита пройден; полная зелень `./...` вернётся с починкой чужого диффа (не в зоне аудита).

---

## 1. Статусы находок (F-01…F-09, P-01/P-02)

| ID | Риск (CVSS) | CWE / OWASP | Локация (проверена) | Вектор | Решение | Статус раунда 3 |
|----|-------------|-------------|---------------------|--------|---------|-----------------|
| F-01 | High (8.0) | CWE-20 / CWE-116 · OWASP A05 | `kernel/sandbox/override.go:42,27,34`; зеркало `workspace/sandboxprofiles.go:257` | BOM/директива-форма прячут `# syntax=` → pull+исполнение произвольного frontend-образа | (сделано) нормализация BOM/CRLF + запрет `# key=` до первой инструкции | **закрыто** (регресс-пробы P01–P06, P11 → REJECT; P12–P15 → ACCEPT с обоснованием «buildkit не исполняет») |
| F-02 | High (8.0) | CWE-182 / CWE-20 · OWASP A05 | `kernel/sandbox/override.go:287,316`; зеркало `workspace/sandboxprofiles.go:455,477` | Разрыв continuation внутри токена (`--net\`+`work=host`) | (сделано) склейка continuation до флаг-проверки | **закрыто** (пробы P07, P08 → REJECT) |
| F-03 | Medium (6.0) | CWE-269 · OWASP A05 | `kernel/sandbox/override.go:316`; зеркало `workspace/sandboxprofiles.go:477` | `RUN --security=insecure\|privileged` снимает seccomp/AppArmor сборки | (сделано) deny-list | **закрыто** (пробы P09, P10 → REJECT; P17 `unconfined` → ACCEPT с обоснованием: buildkit отвергает форму) |
| F-04a | Low (3.5) | CWE-184 · OWASP A05 | `kernel/sandbox/override.go:361` (`isCacheMount`), `:333`; зеркало `workspace/sandboxprofiles.go:518` (`sandboxIsCacheMount`), `:494` | `--mount=type=cache,type=bind[,from=…]` — duplicate-key семантика buildkit делает эффективным `bind`/`secret` при «любой type=cache» скане | (сделано) строгий разбор: отказ при дубле любого ключа (в т.ч. case-fold `type`/`TYPE`), ровно один `type=cache` после снятия кавычек/фолда, allowlist `type,target,id,sharing,mode,uid,gid`, `sharing ∈ {shared,private,locked}`, `id ∈ ^[A-Za-z0-9_.-]{1,64}$`, `mode/uid/gid` — цифры; пустое значение и `--mount` без `=` → отказ | **закрыто** (пробы N01–N03, N05–N09, N15–N19, N22 → REJECT в обоих зеркалах; N04/N12–N14/N20/N21/N23 → ACCEPT) |
| F-04b | Low (3.5) | CWE-284 · OWASP A01 | `kernel/sandbox/override.go:346`; зеркало `workspace/sandboxprofiles.go:506` | Общий cache-`id=` между сборками разных профилей (cache poisoning) | id ограничен charset `^[A-Za-z0-9_.-]{1,64}$` (path-вектор закрыт); префиксация id профилем — hardening | **принято-с-обоснованием** (см. §2) |
| F-05 | High (7.5) | CWE-829 · OWASP A08 | `kernel/sandbox/build.go:572` (`verifyRunArgs`), `:599` | `--entrypoint true` резолвится по PATH образа → код образа исполняется на verify-probe | probe под полным контуром изоляции; оптимум — `docker image inspect` без запуска | **принято-с-обоснованием** (статус не менялся; `verifyRunArgs` не изменился — `--network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges --pids-limit 512 --user 65534:65534 --entrypoint true --pull=never`) |
| F-06 | Medium (5.5) | CWE-117 · OWASP A09 | `kernel/sandbox/build.go:246` (`sanitizeDockerOutput`), `:279` (`stripEscapeSequences`), точки применения `:420,474,521,599`; контракт `spec.md:66` (I-4), `spec.md:133` (D8) | Сырой вывод `docker build` (mutable/внешний `FROM` = недоверенный pull-класс) в `fmt.Errorf` → подделка строк лога (`\r`), ANSI в терминале/`less`, разметка UI | (сделано) санитизация ровно один раз при захвате: CSI/OSC/DCS/SOS/PM/APC/иные ESC, C0 кроме `\n/\t`, DEL, C1, `\r\n/\r`→`\n`, валидный UTF-8, лимит 16384 байт с хвостом и маркером обрезки | **закрыто** (пробы S01–S11 зелёные; D4-классификация работает поверх санитизированного текста) |
| F-06-остаток | Low | CWE-117 · OWASP A09 | `kernel/sandbox/docker.go:418,575`; `kernel/sandbox/build.go:553` (`pinnedRef`, `%q`) | Daemon-класс: `docker network create` (≤300 байт) и `docker rm` вывод вставляются в ошибки без санитизации; `pinnedRef` — через `%q` | hardening: обернуть `sanitizeDockerOutput`/`%q` | **принято-с-обоснованием** (см. §2) |
| F-07 | Low (3.0) | — (UX, не уязвимость) | `kernel/sandbox/override.go:287` | Heredoc-тела → ложные отказы (валидатор строже buildkit) | пропуск тела heredoc | **остаточное (UX)**: не вектор обхода; статус не менялся |
| F-08 | Medium (процессное) | — (не уязвимость рантайма) | `workspace/sandboxprofiles_override_test.go:65,189` (escape `"\ufeff"`); скан BOM по `*.go` — 0 совпадений | Литеральный U+FEFF в Go-строке → `illegal byte order mark` → vet/test падали на setup | (сделано) замена на `"\ufeff"` | **закрыто** (`go vet ./workspace/` и `go test ./workspace/` зелёные). Примечание: заявленная строка «:143» в текущем файле уже не существует (файл переписан; escape-ы стоят на `:65` и `:189`) — суть подтверждена, дрифт строки зафиксирован |
| F-09 | — (устранено в раунде 2) | — | `kernel/sandbox/override.go:287,34` | Мёртвый флаг-скан + паника `match[1]` (промежуточный снимок) | исправлено параллельным работником | **устранено** (пробы флагов и `# key=` форм чистые, без паник) |
| P-01 | — (принято) | CWE-20 · OWASP LLM01 | `kernel/agent/sandboxbuilder.go` (контракт без поля рецепта; prompt `:59`) | Модель не может писать рецепт (prompt-injection → рецепт) | контракт + игнор на сервере | **принято-с-обоснованием**, статус не менялся (поверхностно перепроверено) |
| P-02 | — (принято) | CWE-284 · OWASP A01 | `cmd/agent-kernel/sandboxprofiles.go` (role-гейты writer+/reader) | Класс доверия writer+ (рецепт = произвольный код при сборке) | роль writer+ на мутации, D7-контроли | **принято-с-обоснованием**; оговорка раунда 2 («приятие опирается на работоспособность D7-контролей») теперь снята: F-01/F-02/F-04a/F-05 подтверждены пробами |

---

## 2. Решения по пунктам запроса

**(а) F-04a — ЗАКРЫТО.** Оба зеркала теперь отвергают дубли любого ключа опции (включая case-fold `type=cache,TYPE=bind` → REJECT, N15), требуют ровно один `type=cache` после снятия кавычек/фолда, держат allowlist `type,target,id,sharing,mode,uid,gid`, `sharing ∈ {shared,private,locked}`, `id ∈ ^[A-Za-z0-9_.-]{1,64}$`, цифровые `mode/uid/gid`; `type="cache"` принимается (кавычки снимаются), но кавычки не могут превратить эффективный тип в `bind/secret/ssh`: любой принимаемый набор несёт единственный ключ `type` со значением `cache` (± кавычки) — buildkit либо видит `cache`, либо отвергает литеральное `"cache"` (fail-closed). Пустое значение и `--mount` без `=` → отказ. Корпус N01–N23 — все 23 сценария соответствуют ожиданиям в обоих зеркалах. Семантика duplicate-key buildkit (last-wins) для закрытия находки больше не load-bearing: дубли отвергаются до того, как она вступает в силу.

**(б) Регрессий F-01/F-02/F-03/F-05 — НЕТ.** Пробы P01–P11 (директивы BOM/uppercase/CRLF/tab/indented, continuation-разрывы ключа и флага, heredoc-формы, директива после инструкций) → REJECT; P16 (`RUN echo --network=host`, данные команды) → ACCEPT корректно. F-05 не менялся (`verifyRunArgs` в прежнем контуре), пакет `kernel/sandbox` зелёный.

**(в) F-04b — ПРИНЯТО-С-ОБОСНОВАНИЕМ.** Charset id ограничен (`:346`/`:506`), path-вектор («вывести кэш-монтирование за пределы cache-каталога») закрыт: `id=../x` → REJECT (N08), 65 символов → REJECT (N19), 64 → ACCEPT (N20). Остаток — общий хорошо сформированный `id` между профилями: это тот же класс, что P-02 (writer+ и так пишет произвольный рецепт и исполняет свой код при сборке), дополнительных привилегий не даёт. Hardening (не блокирует): префиксовать `id` идентификатором профиля при передаче в buildkit.

**(г) F-06-остаток по `docker.go` (daemon-класс) — ПРИНЯТО-С-ОБОСНОВАНИЕМ.** Точки применения D8 — ровно 4 (`build.go:420,474,521,599`), все «один раз при захвате»; `output.String()` в `build.go` встречается только в этих 4 местах (grep verbatim — ниже). `pinnedRef` встраивает через `%q` (`build.go:553`) — управляющие символы экранируются. `docker.go` несёт только вывод демона/CLI без pull-контента: имя сети — константа, имена контейнеров — сгенерированный hex, `rm` — свой контейнер; `Execute` ошибки — `%w` без вывода. Обоснование приятия: демон — доверенный компонент деплоя (docker-сокет = root-эквивалент), инъекция pull-класса через эти пути невозможна. Hardening (не блокирует): обернуть `docker.go:418` (≤300 байт) и `docker.go:575` в `sanitizeDockerOutput`/`%q`.

**Побочное наблюдение (не находка):** фильтр санитизации пропускает символы ≥ U+00A0, включая bidi-контроли (U+202A–U+202E, U+2066–U+2069) — теоретическая косметическая путаница в рендере лога; вне заявленного контракта D8 (C0/DEL/C1/ESC), принято как есть.

**(д) AC-11: HARDENED** (значение вердикта — SECURE, см. выше; открытых находок нет).

---

## 3. Пробный корпус (через `go test -overlay`, probe-файлы только в `/scratch`)

### 3.1 Валидаторы Dockerfile — 40 сценариев × 2 зеркала (mirror = `workspace.ValidSandboxDockerfile`, authority = `kernelsandbox.OverrideDefinition`)

| # | Сценарий (сокр. текст) | mirror | authority | Ожидание |
|---|---|---|---|---|
| P01 | `# key=value` до FROM | REJECT | REJECT | REJECT |
| P02 | `# SYNTAX=evil/frontend:latest` | REJECT | REJECT | REJECT |
| P03 | `\ufeff# syntax=…` (BOM) | REJECT | REJECT | REJECT |
| P04 | `# syntax=…` с CRLF | REJECT | REJECT | REJECT |
| P05 | `#\tkey=hello` | REJECT | REJECT | REJECT |
| P06 | `\ufeff  # escape=`` | REJECT | REJECT | REJECT |
| P07 | `R\`+перенос+`UN --network=host` | REJECT | REJECT | REJECT |
| P08 | `--net\`+перенос+`work=host` | REJECT | REJECT | REJECT |
| P09 | `# syntax=evil` в heredoc-теле | REJECT | REJECT | REJECT (over-block F-07) |
| P10 | `ADD x y` в heredoc-теле | REJECT | REJECT | REJECT (over-block F-07) |
| P11 | `# syntax=evil` после инструкций | REJECT | REJECT | REJECT (over-block) |
| P12 | двойной BOM | ACCEPT | ACCEPT | ACCEPT (buildkit не исполняет) |
| P13 | `#\u00a0syntax=evil` (NBSP) | ACCEPT | ACCEPT | ACCEPT (buildkit не исполняет) |
| P14 | `# syntax\u00a0=evil` (NBSP) | ACCEPT | ACCEPT | ACCEPT (buildkit не исполняет) |
| P15 | `# synt\`+перенос+`ax=evil` | ACCEPT | ACCEPT | ACCEPT (buildkit не исполняет) |
| P16 | `RUN echo --network=host` (данные) | ACCEPT | ACCEPT | ACCEPT |
| P17 | `--security=unconfined` | ACCEPT | ACCEPT | ACCEPT (buildkit отвергает форму) |
| N01 | `--mount=type=cache,type=bind,from=base,target=/x` | REJECT | REJECT | REJECT |
| N02 | `--mount=type=bind,type=cache` | REJECT | REJECT | REJECT |
| N03 | `--mount=type=cache,type=secret` | REJECT | REJECT | REJECT |
| N04 | `--mount=type="cache"` | ACCEPT | ACCEPT | ACCEPT |
| N05 | `--mount=type=cache,type="bind"` | REJECT | REJECT | REJECT |
| N06 | `--mount=type=cache,target=/x,target=/y` (дубль target) | REJECT | REJECT | REJECT |
| N07 | `--mount=type=cache,sharing=evil` | REJECT | REJECT | REJECT |
| N08 | `--mount=type=cache,id=../x` | REJECT | REJECT | REJECT |
| N09 | `--mount=type=cache,from=base` (неизвестная опция) | REJECT | REJECT | REJECT |
| N10 | `--mount=` (пустое значение) | REJECT | REJECT | REJECT |
| N11 | `--mount` (без `=`) | REJECT | REJECT | REJECT |
| N12 | `--mount=type=cache` | ACCEPT | ACCEPT | ACCEPT |
| N13 | `--mount=type=cache,target=/x` | ACCEPT | ACCEPT | ACCEPT |
| N14 | `type=Cache,ID=ok-1.2_3,sharing=private,mode=0755,uid=65534,gid=65534` | ACCEPT | ACCEPT | ACCEPT |
| N15 | `--mount=type=cache,TYPE=bind` (дубль с case-fold) | REJECT | REJECT | REJECT |
| N16 | `--mount=type=cache,,target=/x` (двойная запятая) | REJECT | REJECT | REJECT |
| N17 | `--mount=type=cache-allowlist,target=/x` | REJECT | REJECT | REJECT |
| N18 | `--mount=type=cache,mode=0x10` | REJECT | REJECT | REJECT |
| N19 | `id=` из 65 символов | REJECT | REJECT | REJECT |
| N20 | `id=` из 64 символов | ACCEPT | ACCEPT | ACCEPT |
| N21 | `--mount=type=cache,sharing=locked` | ACCEPT | ACCEPT | ACCEPT |
| N22 | `--mount=type=cache,target=""` | REJECT | REJECT | REJECT |
| N23 | `--mount="type=cache",target=/x` | ACCEPT | ACCEPT | ACCEPT |

**Расхождений зеркал нет: 40/40 совпадают (assert в пробе: `mirrors disagree` — 0 срабатываний).** Семантика зеркал идентична и механически: тела `isCacheMount` (`override.go:361`) и `sandboxIsCacheMount` (`sandboxprofiles.go:518`) после нормировки имён — `diff` пуст (`MIRROR_BODIES_IDENTICAL`); то же для паттернов id (`:346` / `:506`) и allowlist/сообщений об отказе (включая каноническую фразу «допустим только один type=cache без дублей ключей»).

### 3.2 Санитизация вывода docker (F-06) — 11 сценариев, kernel-зеркало

| # | Вход | Выход | Итог |
|---|---|---|---|
| S01 | `ok` + CSI `ESC[2J` `ESC[1;31m` `ESC[0m` + `red` | `okred` | ✅ |
| S02 | OSC-титул `ESC]0;evil title BEL` + `after` | `after` | ✅ |
| S03 | OSC-титул `ESC]0;evil ST` + `after` | `after` | ✅ |
| S04 | `\r`-перезапись: `safe\rFAKE line\nreal\n` | `safe\nFAKE line\nreal\n` | ✅ |
| S05 | `a\r\nb` | `a\nb` | ✅ |
| S06 | NUL + DEL + C1-NEL: `a\0b\177c\205d` | `abcd` | ✅ |
| S07 | невалидный UTF-8: `ok\xff\xfe bad` | `ok bad` | ✅ |
| S08 | DCS `ESC P secret ST` + `tail` | `tail` | ✅ |
| S09 | VT + FF: `x\13y\14z` | `xyz` | ✅ |
| S10 | 20000×`x` + `TAIL-END` | len=16384, маркер обрезки в начале, `TAIL-END` в хвосте | ✅ |
| S11 | D4 поверх санитизированного: `\r\n` + ANSI вокруг `sqlmap (no such package)` | классификация срабатывает, имя пакета в ошибке | ✅ |

---

## 4. Верификация командами (verbatim, снапшот 2026-10-21; `PATH=/usr/local/go/bin:$PATH`, go1.27.2 linux/arm64)

```
go build ./...
BUILD_EXIT=0
```
```
go vet ./kernel/sandbox/ ./workspace/ ./kernel/agent/
VET_TARGET_EXIT=0
```
```
go test -count=1 ./kernel/sandbox/ ./workspace/ ./kernel/agent/
ok  	github.com/temporality-project/temporality/kernel/sandbox	1.010s
ok  	github.com/temporality-project/temporality/workspace	0.300s
ok  	github.com/temporality-project/temporality/kernel/agent	77.611s
TEST_TARGET_EXIT=0
```
```
go vet ./...
# github.com/temporality-project/temporality/cmd/agent-kernel
vet: cmd/agent-kernel/temporalrpc_test.go:49:9: cannot use fakeWorkflowRun{…} as client.WorkflowRun value in return statement: fakeWorkflowRun does not implement "go.temporal.io/sdk/internal".WorkflowRun (missing method Get)
VET_ALL_EXIT=1
```
```
go test -count=1 ./cmd/agent-kernel/
cmd/agent-kernel/temporalrpc_test.go:49:9: cannot use fakeWorkflowRun{…} …
cmd/agent-kernel/temporalrpc_test.go:224:19: assignment mismatch: 2 variables but 1 value
FAIL	github.com/temporality-project/temporality/cmd/agent-kernel [build failed]
TEST_AGENT_EXIT=1
```
(Чужой незакоммиченный дифф `cmd/agent-kernel/temporalrpc_test.go`/`nudge_test.go`; к I-3 отношения не имеет. Import `workflowservice` в `cmd/agent-kernel/main.go:43` **используется** на `:1082` — неиспользуемым не является, нулевое изменение от upstream-задачи корректно: удаление импорта сломало бы сборку.)

Пробный корпус (overlay: probe-файлы `/scratch/probes/*` замаплены на несуществующие `zz_audit_probe_*_test.go`, репозиторий не тронут):
```
go test -count=1 -overlay=/scratch/probes/overlay.json -run '^TestAuditProbeCorpus$' -v ./workspace/
… 40 строк mirror/authority …
--- PASS: TestAuditProbeCorpus (0.00s)
CORPUS_EXIT=0
```
```
go test -count=1 -overlay=/scratch/probes/overlay.json -run '^TestAuditSanitize|^TestAuditD4' -v ./kernel/sandbox/
… S01–S11 …
--- PASS: TestAuditSanitizeProbes / TestAuditSanitizeOversizeTail / TestAuditD4OnSanitizedText
SANITIZE_EXIT=0
```
Скан литерального BOM в Go-исходниках (F-08):
```
find . -name '*.go' -not -path './debugger/*' | xargs grep -ln $'\xef\xbb\xbf'
(пусто; файлов с литеральным BOM нет)
```
Механическая сверка зеркал:
```
sed -n '/^func isCacheMount/,/^}/p' kernel/sandbox/override.go | sed 's/isCacheMount/F/; s/cacheMountIDPattern/IDP/g' > /scratch/a.txt
sed -n '/^func sandboxIsCacheMount/,/^}/p' workspace/sandboxprofiles.go | sed 's/sandboxIsCacheMount/F/; s/sandboxCacheMountIDPattern/IDP/g' > /scratch/b.txt
diff /scratch/a.txt /scratch/b.txt
MIRROR_BODIES_IDENTICAL
```
Точки применения F-06 (ровно 4, «один раз при захвате»):
```
grep -n 'sanitizeDockerOutput(output.String())\\|log := sanitizeDockerOutput' kernel/sandbox/build.go
420: socketError(…sanitizeDockerOutput(output.String())…)   # PreflightSocket
474: log := sanitizeDockerOutput(output.String())           # BuildDefinition (+ D4 по log)
521: image pull: …sanitizeDockerOutput(output.String())     # PullImage
599: verify built sandbox image …sanitizeDockerOutput(…)    # verifyPinned
```

---

## 5. PoC (только текстом; не исполнялись)

- **F-04a (закрыто; прежний обход теперь отвергается).** Было: `FROM alpine:3.20` + `RUN --mount=type=cache,type=bind,from=base,target=/x cmd` → ACCEPT в обоих зеркалах (эффективный `bind` по duplicate-key семантике buildkit). Стало: REJECT в обоих зеркалах («дубль ключа опции "type"»), как и обратный порядок `type=bind,type=cache`, `type=cache,type=secret`, `type=cache,type="bind"`, case-fold дубль `TYPE=` (пробы N01–N03, N05, N15). Концептуальный патч (уже в коде): `isCacheMount`/`sandboxIsCacheMount` — `seen[key]`-карта на любой дубль, allowlist ключей, `strings.EqualFold(val,"cache")` после `strings.Trim(..., "\"'")`.
- **F-06 (закрыто).** Было: рецепт с mutable `FROM evil/base:latest`, слой печатает `\r`+ANSI → сырой лог в `fmt.Errorf` (подделка строк server-лога, ANSI в `less`). Стало: `sanitizeDockerOutput` при захвате — `\r`→`\n`, ESC-последовательности вырезаются, C0/DEL/C1 — нет (S01–S09). Концептуальный патч (уже в коде): `stripEscapeSequences` + фильтр рун + лимит 16384 с хвостом (`build.go:246–277`).
- **F-08 (закрыто).** Было: литеральный `\xEF\xBB\xBF` внутри Go-строки → `illegal byte order mark` → `go vet`/`go test` падали. Стало: `"\ufeff"`-escape (`workspace/sandboxprofiles_override_test.go:65,189`), vet/тесты зелёные.
- **F-04b (остаток, принят).** `RUN --mount=type=cache,id=shared,target=/x cmd` в профилях A и B → общий кэш между сборками. Path-форма (`id=../x`) отвергается; общий корректный id — принятый класс P-02.
- **F-06-остаток (принят).** Вывод `docker network create` (до 300 байт, `docker.go:418`) попадает в текст ошибки без санитизации — возможны ANSI/`\r` от недоверенного демона; демон доверенный деплой-компонент, pull-контент в этот путь не попадает.

---

## 6. Подтверждено пробами / не подтверждено

**Подтверждено пробами (go test -overlay, оба зеркала):**
- F-04a закрыт: все 12 негативных duplicate-key/allowlist сценариев (N01–N03, N05–N09, N15–N19, N22) → REJECT; позитивные (N04, N12–N14, N20, N21, N23) → ACCEPT; зеркала совпадают 40/40.
- F-06 закрыт: S01–S11 (ANSI CSI, OSC-титул BEL/ST, DCS, `\r`-перезапись, `\r\n`, NUL/DEL/C1, VT/FF, невалидный UTF-8, >16 КиБ с хвостом и маркером, D4 поверх санитизированного текста).
- F-08 закрыт: `go vet`/`go test ./workspace/` зелёные, скан `*.go` на литеральный BOM — пуст.
- Регрессий F-01/F-02/F-03: P01–P11 → REJECT, P16/P17 → корректный ACCEPT.
- Семантика зеркал идентична (diff тел функций пуст + 40/40 в корпусе).
- Сборка (`go build ./...`), vet и тесты scope-пакетов (`kernel/sandbox`, `workspace`, `kernel/agent`) — зелёные.

**Не подтверждено / вне проверки:**
- Duplicate-key семантика buildkit (last-wins) — не верифицирована против upstream-исходников (как и в раунде 2); закрытие F-04a от неё не зависит (дубли отвергаются априори).
- Поведение кавычек внутри `--mount` у реального buildkit (N04/N23): наша сторона строже-или-эквивалентна; худший случай — fail-closed (сборка отвергнет форму), обход не выстроен.
- `go vet ./...` / `go test ./cmd/agent-kernel/` — красные по чужому диффу (см. §4); полная зелень `./...` не подтверждена и от аудита не зависит.
- debugger (vitest/npm): node/npm в песочнице отсутствуют — не выполнялись (фронтенд вне scope I-3).
- Статические анализаторы semgrep/gosec/staticcheck/govulncheck: в песочнице отсутствуют, установка требует сети (запрет ограничений аудита). Эквивалент: `go vet` по scope-пакетам + пробный корпус + ручной аудит по четырём эшелонам.
- F-05 hardening (`docker image inspect` вместо probe-запуска, `--memory/--cpus`) — не сделан, статус принятого-с-обоснованием сохранён.
- Полнота динамики buildkit (реальные сборки docker-демоном) не прогонялась: демона в песочнице нет.

## 7. Эшелоны и цепочка поставок (кратко)

- **Untrusted Boundaries**: `dockerfile_override` (writer+) — F-01/F-02/F-03/F-04a закрыты пробами ✓; модельный путь (SandboxDraft) — P-01 ✓ (нет поля рецепта); внешние образы — P-02/F-05 приняты, F-06 закрыт ✓; вывод docker → ошибки/логи/UI — F-06 закрыт ✓, остаток daemon-класс принят; runtime запуск — профиль жёсткий (вне I-3).
- **AI/LLM**: инъекции (нет поля рецепта), утечки (не выявлены), Excessive Agency (действия — роль writer+ от имени оператора) — проверены, отклонений нет.
- **Цепочка поставок**: `go.mod` — 0 плавающих версий/`replace`/диапазонов (grep verbatim: 0 совпадений); npm — диапазоны `^x.y.z` компенсированы lock-файлами (сборка только `npm ci`); typosquatting-признаков нет (канонические upstream-имена).
- **Защитное проектирование**: verify-probe под изоляцией не слабее runtime-профиля; сокет-префлайт классифицирует ошибки деплоя без pull-контента.

---

**Резюме для AC-11:** закрыто — F-04a, F-06 (основной), F-08; без регрессий F-01/F-02/F-03/F-05; принято-с-обоснованием — F-04b (id charset ограничен, остаток = общий cache id в классе writer+), F-05, P-01, P-02, F-06-остаток (daemon-класс `docker.go`, hardening предложен). Открытых находок нет → **SECURE / AC-11 HARDENED**.
