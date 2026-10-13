# Temporality — quality-loop: финальный отчёт (Rework round 3)

Дерево: `a7d4282` (ветка `test/agent-pr-pipeline`), `git status` → чистое (`## ... [ahead 5]`, porcelain пуст).
Спека `/workspace/specs/quality-loop.md` не изменялась.

## 1. Коммиты

| Хеш | Что |
|---|---|
| `81b1499` | `feat(quality-loop): M3 eval-gated proposal apply with override and 202 idempotency` — позиции **1, 2, 3** |
| `bd053ad` | `feat(quality-loop): stale_after on golden cases and suite_stale apply refusal` — позиция **4** |
| `a7d4282` | `feat(quality-loop): M4 LLM-judge execution, calibration endpoints and M5 verdict/judge agreement` — позиции **5, 6** |

## 2. Закрытые позиции 1–7 (file:line по дереву a7d4282)

### 1 [C1] M3 apply-flow end-to-end (81b1499)
- Старый handler `main.go:1884-1939` удалён, подключён `RegisterM3Routes` — `cmd/agent-kernel/main.go:1927` (единственная регистрация `POST .../proposals/{id}/apply`, двойной регистрации/паники mux нет — подтверждено проходящими тестами пакета `cmd/agent-kernel`).
- Маршрут и RBAC: `cmd/agent-kernel/m3evalgate.go:113` (route), `:114-125` (writer = полный админ, reader → 403).
- Парсинг тела запроса: `m3evalgate.go:94-107` (`parseEvalGateRequest`), `gate` — дефолт `true`.
- Свежесть: `workspace/evalgate.go:76-92` (`EVAL_GATE_FRESHNESS_WORKDAYS`, дефолт 3), обрезка свежести `evalgate.go:97-117`, Сб/Вс не расходуют свежесть — `evalgate.go:119-123` (`isEvalWorkingDay`).
- Нет прогона → 202 + `evaluation_run_id`, сервер сам стартует прогон `trigger=gate`: `m3evalgate.go:243-258`, `CreateGateRun` — `m3evalgate.go:249` → `workspace/evalgate.go:285` (идемпотентен: повторный apply возвращает тот же 202, не второй прогон — `TestEvalGateRepeatApplyIsIdempotent202`).
- Красный → 409 + `failed_cases` — `m3evalgate.go:218-241` (`gate_blocked`, `evalFailedCaseNames:313`).
- Устаревшая база → 409 `base_version_stale` — `m3evalgate.go:162`, `workspace/agentproposals.go:149` (`ErrBaseVersionStale`), поверхность `:206`.
- Apply-колбэк — `Store.ApplyAgentProposal` (передаётся в `RegisterM3Routes` как `apply`, `m3evalgate.go:69,109`).

### 2 [C1] Override гейта (81b1499)
- Тело парсится (`gate:false` + override с обязательным `justification`) — `m3evalgate.go:94-107`, ветка `:168`, требование justification `:363` (`evalGateOverrideRequired`), тест `TestEvalGateOverrideRequiresJustification`.
- reader → 403 — `m3evalgate.go:116`, тест `TestEvalGateReaderForbidden`.
- Промот красного → audit: `recordEvalGateOverride` `m3evalgate.go:270-281` → `RecordAccessAudit` с `workspace.AuditEvalGateOverridden` (`workspace/audit.go:60`), тест `TestEvalGateRedOverrideByWriter`.
- События: `agent.evaluation.gate.overridden` — `m3evalgate.go:192,227`; `gate.blocked` — `:197,232`; `gate.passed` — `:214` (эмиттер `:326-360`).

### 3 [C2] Миграции 000054/000056 (81b1499)
- Цепочка старта `main.go:58-206` продолжена: 000054 — `cmd/agent-kernel/main.go:214`, 000056 — `main.go:218`; 000052 — `evalcasecandidates.go:44` (пример M2). Без этого в БД нет `proposal_id/verdict/cause/threshold/judge_*` и `workspace_judge_calibration`.

### 4 [M2 partial] stale_after + suite_stale (bd053ad)
- Поле `stale_after` у golden-кейса (по умолчанию 90 дней) — `workspace/skillevals.go:42-48`.
- Проверка протухших — `workspace/evalgate.go:63` (`EvalSuiteStaleCases`).
- В apply-гейте → 409 `suite_stale` со списком протухших (вариант B §13) — `m3evalgate.go:175-207` (отказ до гейта; `gate:false` не обходит — `TestEvalGateSuiteStaleSkippedByGateFalse`, override писателем — `TestEvalGateSuiteStaleOverride`).

### 5 [M4] LLM-judge (a7d4282)
- (а) judge-исполнение в kernel-раннере: `kernel/agent/agenteval.go:174-247` — резолв конфига `:178-186`, вызов `judgeEvalCase` после passed-паттернов `:211-224` через `kernel/agent/agentevaljudge.go`; ошибки judge → `judge_error`/`judge_invalid_json`/`judge_not_configured` (`:217-224`).
- (б) endpoints §8.6: `GET /v1/workspace/evaluation-judge` — `cmd/agent-kernel/m4judge.go:48`; `POST /v1/workspace/agents/{agentID}/evaluation-judge/calibrate` — `m4judge.go:63` (writer-only, reader → 403 — `TestJudgeCalibrateReaderForbidden`).
- (в) PUT evaluation-suite валидирует judge-кейсы: маршрут `cmd/agent-kernel/main.go:1645`, без `EVAL_JUDGE_MODEL` → 422 `judge_not_configured` — `main.go:1681-1689`.
- (г) `recordPayload` несёт `tested_model` — `kernel/agent/agenteval.go:248`; сервер выставляет `judge_same_model` — `workspace/agentjudge.go:74-78,265` (`JudgeSameModel`), колонка `workspace/agentevals.go:53,147-151`.
- (д) событие `agent.evaluation.judge.calibrated` — `cmd/agent-kernel/m4judge.go:139,151-166`.
- (е) тесты `workspace/agentjudge_test.go` (обязательные по спеке, все прошли): `TestComputeRunVerdictJudgeNotConfigured:13` (judge_not_configured), `TestJudgeSameModel:44`, `TestJudgeCalibrationDegraded:54` + `TestCalibrationAgreementBelowFloorIsDegraded:63` (agreement < 0.7 → judge-degraded), `TestComputeRunVerdictJudgeErrors:92` (ошибка judge → verdict=error), `TestComputeRunVerdictPatternsPlusJudge:121`. Плюс endpoint-тесты `cmd/agent-kernel/m4judge_test.go` (calibrate happy/degraded/reader-forbidden, config).

### 6 [M5] QualityEvaluation (a7d4282)
- Поля `verdict` и `judge_agreement` — `workspace/quality.go:39-47` (§8.4); чтение `qualityEvaluationRuns` — `quality.go:270-291`; резолв судейского согласия в `AgentQualityReport` — `quality.go:127-145`; заполнение `LastEvaluation` — `quality.go:393-409`.

### 7. Финальная верификация
- `go build ./...` → exit 0 (лог ниже).
- `go vet ./...` → exit 0, пустой вывод (лог ниже).
- `go test ./... -count=1` → exit 0: **15 ok, 5 «no test files», 0 FAIL** (дословный лог ниже).
- Обязательные сценарии (гейт / идемпотентный 202 / suite_stale / override писателем) — 26 targeted `--- PASS`, `0 FAIL` (дословный лог ниже).
- Рабочее дерево чистое: `git status --porcelain=v1` → 0 строк.
- Линтер: `golangci-lint`/`staticcheck` в окружении отсутствуют; прогнан `go vet` + `gofmt -l`. `gofmt -l` указывает 3 файла: `kernel/llm/client_test.go` (formatted-дрейф **до** quality-loop, последний коммит `b365c1f`), `workspace/audit.go` и `workspace/quality_test.go` (только выравнивание колонки комментариев/пробелов, семантика нулевая; в `Makefile` цель `fmt` покрывает только `cmd kernel observation examples`, не `workspace`). По инструкции ревьюера «код не менять» — эти whitespace-файлы не трогал; `go vet` и все тесты при этом зелёные.

## 3. Дословный лог статической анализа

```
$ go build ./...
BUILD_EXIT=0        (пустой вывод)

$ go vet ./...
VET_EXIT=0          (пустой вывод)
```

## 4. Дословный лог `go test ./...` по дереву a7d4282 (принудительно, -count=1, без кэша)

```
$ go test -count=1 ./...
ok  	github.com/temporality-project/temporality/cmd/agent-kernel	0.017s
ok  	github.com/temporality-project/temporality/cmd/temporality	0.005s
?   	github.com/temporality-project/temporality/cmd/temporality-journal	[no test files]
ok  	github.com/temporality-project/temporality/controlplane	0.001s
ok  	github.com/temporality-project/temporality/examples/lead_coder_reviewer_qa	0.062s
?   	github.com/temporality-project/temporality/examples/test-mcp	[no test files]
ok  	github.com/temporality-project/temporality/kernel/agent	0.450s
ok  	github.com/temporality-project/temporality/kernel/cost	0.001s
ok  	github.com/temporality-project/temporality/kernel/llm	12.752s
ok  	github.com/temporality-project/temporality/kernel/mcpclient	0.470s
?   	github.com/temporality-project/temporality/kernel/outbox	[no test files]
ok  	github.com/temporality-project/temporality/kernel/probe	0.001s
ok  	github.com/temporality-project/temporality/kernel/quota	0.001s
ok  	github.com/temporality-project/temporality/kernel/sandbox	1.007s
ok  	github.com/temporality-project/temporality/observation	0.005s
ok  	github.com/temporality-project/temporality/observation/httpapi	0.004s
?   	github.com/temporality-project/temporality/observation/memory	[no test files]
?   	github.com/temporality-project/temporality/observation/postgres	[no test files]
ok  	github.com/temporality-project/temporality/skills	0.001s
ok  	github.com/temporality-project/temporality/workspace	0.294s
TEST_EXIT=0
```

Итог по строкам: `ok` = 15, `? [no test files]` = 5, `FAIL` = 0.

## 5. Дословный лог трёх ключевых пакетов

```
$ go test -count=1 ./cmd/agent-kernel ./workspace ./kernel/agent
ok  	github.com/temporality-project/temporality/cmd/agent-kernel	0.015s
ok  	github.com/temporality-project/temporality/workspace	0.372s
ok  	github.com/temporality-project/temporality/kernel/agent	0.460s
EXIT=0
```

## 6. Дословный лог обязательных сценариев (гейт / 202 / suite_stale / override / judge)

```
$ go test -count=1 -v -run 'TestEvalGate|TestEvalSuiteStaleCases|TestJudge|TestCalibration|TestComputeRunVerdict' ./cmd/agent-kernel ./workspace ./kernel/agent
--- PASS: TestEvalGateGreenAppliesAndEmitsPassed (0.00s)
--- PASS: TestEvalGateStartsRunAndAnswers202 (0.00s)
--- PASS: TestEvalGateRepeatApplyIsIdempotent202 (0.00s)
--- PASS: TestEvalGateRedBlocksWithFailedCases (0.00s)
--- PASS: TestEvalGateRedOverrideByWriter (0.00s)
--- PASS: TestEvalGateOverrideRequiresJustification (0.00s)
--- PASS: TestEvalGateReaderForbidden (0.00s)
--- PASS: TestEvalGateFalseSkipsGate (0.00s)
--- PASS: TestEvalGateBaseVersionStale (0.00s)
--- PASS: TestEvalGateNonPendingProposalConflicts (0.00s)
--- PASS: TestEvalGateMalformedBody (0.00s)
--- PASS: TestEvalGateSuiteStaleRefusesBeforeGate (0.00s)
--- PASS: TestEvalGateSuiteStaleSkipsHeldOut (0.00s)
--- PASS: TestEvalGateSuiteStaleOverride (0.00s)
--- PASS: TestEvalGateSuiteStaleSkippedByGateFalse (0.01s)
--- PASS: TestJudgeCalibrateHappyPathStoresHistoryAndEmits (0.00s)
--- PASS: TestJudgeCalibrateAgreementBelowFloorIsDegraded (0.00s)
--- PASS: TestJudgeCalibrateReaderForbidden (0.00s)
--- PASS: TestJudgeConfigEndpointReportsUnconfigured (0.00s)
--- PASS: TestComputeRunVerdictJudgeNotConfigured (0.00s)
--- PASS: TestJudgeSameModel (0.00s)
--- PASS: TestJudgeCalibrationDegraded (0.00s)
--- PASS: TestCalibrationAgreementBelowFloorIsDegraded (0.00s)
--- PASS: TestComputeRunVerdictJudgeErrors (0.00s)
--- PASS: TestComputeRunVerdictPatternsPlusJudge (0.00s)
--- PASS: TestEvalSuiteStaleCases (0.00s)
PASS
--- PASS count: 26, --- FAIL count: 0
ok  	github.com/temporality-project/temporality/cmd/agent-kernel	0.016s
ok  	github.com/temporality-project/temporality/workspace	0.002s
ok  	github.com/temporality-project/temporality/kernel/agent	0.007s [no tests to run]
TARGET_EXIT=0
```

Пояснение к `kernel/agent [no tests to run]`: обязательные judge-тесты по спеке §5.2 живут в `workspace/agentjudge_test.go` (позиция 5е), сам пакет `kernel/agent` полностью зелёный в общем прогоне (`ok ... kernel/agent 0.450s`).
