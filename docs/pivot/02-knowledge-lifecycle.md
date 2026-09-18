# Pivot Temporality — Этап 2: knowledge lifecycle

Дата: 2026-09-18. ТЗ §22 Этап 2: агент должен уметь отличать «мы предполагаем X» от «мы проверили X и получили Y».

## Что было (пробелы №1–№2 из `01-archaeology.md`)

- `supported` (CONFIRMED) был недостижим в автоматическом пайплайне: шаг всегда создавал claims как `candidate`; переход — только ручным API.
- Claims из эмиссий модели рождались без evidence: путь «гипотеза → на каком наблюдении основана» отсутствовал.

## Что сделано

### 1. Схема эмиссии (`frp/cognition/emission.go`)

- `EmittedClaim.Evidence []string` — канонические рефы `"event:UUID"`; принимаются строки и ref-объекты (нормализация как у `supersedes`), пустые/дубликаты вычищаются в `ApplyDefaults`.
- `EmittedClaim.Status` теперь `candidate | supported`:
  - `supported` разрешён **только** с непустым `evidence` — «факт, извлечённый из цитируемых событий»;
  - `candidate` — гипотеза; evidence опционален (мотивирующие наблюдения);
  - терминальные статусы при рождении по-прежнему запрещены.
- `claim_ops[].op` — `refute | confirm`.

### 2. Step-пайплайн (`frp/runtime/step/step.go`)

- Claim рождается со статусом из эмиссии. Born-supported создаёт событие `claim.supported` (payload включает `evidence`), hypothesis — `claim.candidate`. Лог событий сам читается как lifecycle.
- `resolveTarget` параметризован целевым статусом; `confirm` → переход `claim.supported` (`CanTransition(candidate→supported)` уже существовал), `refute` → `claim.refuted` как раньше.
- Повторный confirm supported-claim — видимый Rejection «claim is already supported» (не ошибка), как у остальных guard'ов.
- `stepRuntime.Claim.Evidence` (bare event ids) едет в stores.

### 3. Stores

- **postgres** (`frp/substrate/postgres/step.go`): evidence-рефы проверяются в `validateStepRefsPostgres` (несуществующее событие = ошибка шага, тот же контракт, что у observation refs); строки `claim_evidence` пишутся атомарно с claim; **фикс**: UPDATE переходов больше не закрывает `valid_to` для confirm (раньше любой переход ставил `valid_to=now` — confirm делал бы supported-claim «истёкшим»).
- **memory** (`frp/substrate/memory/store.go`): та же проверка evidence в `validateStepRefsLocked`; `claimEvidence` заполняется в write-phase (работают `ListClaimEvidence`/`ListClaimsByWorld`).

### 4. Промпт (`frp/model/model.go`)

Обновлены инструкции: `status:"supported"` только для факта, извлечённого из цитируемых событий (с `evidence`); `confirm` — повысить проверенную гипотезу (например, execution result это доказал); `refute` — как раньше. Evidence добавлен в список «ids копируются точно из пакета».

## Маппинг на статусы ТЗ §5

| ТЗ | Реализация |
|---|---|
| OBSERVED | не вводился пятый статус; его роль играет born-`supported` с evidence (факт мира, извлечённый из наблюдения) — решение зафиксировано в `01-archaeology.md` |
| HYPOTHESIS | `candidate` |
| CONFIRMED | `supported`: born (с evidence) или через `claim_ops.confirm` после проверки |
| REFUTED | `refute` (без изменений) |
| SUPERSEDED | `supersedes` (без изменений) |

## Иварианты

- Claim не может цитировать несуществующее событие (M13) — теперь и для модельных claims.
- Переходы идут через канонический `CanTransition` + триггер БД; confirm не закрывает `valid_to`.
- Всё атомарно в `CommitStep`; проваленный шаг не оставляет следов (тесты проверяют rollback).
- Replay не затронут: новые события — обычные события лога.

## Тесты

- `frp/cognition/emission_claim_ops_test.go` — `TestClaimLifecycleDecodeAndValidate`: decode/нормализация evidence, supported-без-evidence отклонён, confirm валиден, терминальные статусы рождения запрещены.
- `frp/runtime/step/step_test.go` — `TestStepAppliesClaimLifecycle`: born-supported + evidence в `claim_evidence`, confirm открывает гипотезу (valid_to nil), повторный confirm = rejection, галлюцинированный evidence = ошибка + полный rollback.
- `frp/substrate/postgres/claim_evidence_integration_test.go` — `TestStepClaimLifecyclePersists`: то же в PostgreSQL, включая valid_to IS NULL после confirm.
- Полный `go test ./...` (unit + integration, warm test DB) — зелёный.

## Осознанно не сделано (следующие этапы)

- Render ещё не сообщает статус/происхождение delivered-claims (identity/world_memory) — Этап 4 (подсекции CONFIRMED/REFUTED/PREVIOUSLY INVESTIGATED).
- `focus.changed` событие с trigger — Этап 3.
- Evidence у самих `claim_ops.confirm` не заводился: provenance подтверждения восстанавливается цепочкой `claim.supported` event → emission_id → emission.observation (шаг, где модель видела проверку). При необходимости добавим на Этапе 4/5 по данным бенчмарка.
