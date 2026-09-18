# Pivot Temporality — Этап 3: investigation history

Дата: 2026-09-18. ТЗ §22 Этап 3 / §7: зафиксировать, какие объекты исследовались, какие гипотезы возникали/подтверждались/опровергались и как менялся focus — с причиной каждого перехода.

## Что было (пробел №3 из `01-archaeology.md`)

Смена фокуса не фиксировалась нигде: `frame.transitioned` несёт только parent/frame/emission; история восстанавливалась диффом frames без причин.

## Что сделано

### Событие `focus.changed` (`frp/runtime/step/step.go`)

Эмитируется step-пайплайном, когда attention-операция реально меняет фокус (сравнение `current.Focus` → `decision.Frame.Focus`; no-op attend не пишет ничего). Payload:

```json
{
  "from": "query:internal/reports",
  "to": "query:internal/util/format.go",
  "trigger": "hypothesis_retired",
  "evidence": ["claim:UUID", "event:UUID"],
  "frame_before": "...",
  "frame_after": "...",
  "emission_id": "..."
}
```

- `trigger`: `hypothesis_retired` (в этом же шаге refute/supersede — сильнейшая причина уйти), `hypothesis_confirmed` (confirm), `deliberate` (обычный выбор; причина.traceable через `emission_id` → reasoning эмиссии).
- `evidence`: claim-рефы переходов шага + наблюдаемые события шага (каждое — проверяемый факт «почему»), cap 8.
- `timestamp` — `valid_time` самого события (двухвременная модель уже даёт это).

### Куда встаёт событие

- Внутри «attention-диапазона» `Prepared.Events` (между claim-переходами и attention-событиями) — позиционная арифметика обоих stores не тронута, вставка через существующие циклы.
- Не входит в `ambientHiddenEvents` → попадает в `recent` RenderPacket и в attention-кандидаты: модель видит собственную историю переключений.
- Replay/timetravel/projections работают без изменений (новое событие — обычная строка лога).

### Прочее

- Обновлены счётчики событий в `scripts/smoke.py` (frame replay 9 → 10) и в тестах httpapi/step.
- Compose-executor во время smoke останавливался (крал executions из общей БД) — известная особенность локальных прогонов, возвращён обратно после.

## Тесты

- `TestStepRecordsFocusChangeWithTrigger` (`frp/runtime/step/step_test.go`): refute + attend → `focus.changed` с `hypothesis_retired`, from/to/frame_before/after, evidence содержит claim и event рефы; событие персистится; no-op attend не пишет событие.
- `TestMemoryStepSuccessAndReferenceRollback` / `TestAtomicStepAPI` обновлены на новый состав событий (focus.changed на месте [1]).
- Полный `go test ./...` + `scripts/smoke.py` — зелёные.

## Как этим отвечает на вопросы ТЗ §14

- «Почему агент пошёл в этот файл?» — `focus.changed` c from/to + evidence (claim, event) + emission reasoning.
- «Почему перестал рассматривать путь?» — `trigger: hypothesis_retired` + refuted/superseded claim в evidence.
- «Что уже проверял?» — цепочка `focus.changed` + executions + claims по ним.

## Осознанно не сделано

- Отдельная projection-таблица «investigated objects» не заводилась: trajectory строится из событий (`focus.changed` + `claim.*` + `execution.*`) — Этап 5 сделает из неё вью в debugger'е. Если запросы окажутся дорогими — проекция поверх тех же событий, без изменения пайплайна.
- `trigger` не извлекается из reasoning-текста (свободный текст ненадёжен как источник структуры); курсивная причина доступна через emission.
