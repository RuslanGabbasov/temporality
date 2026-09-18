# Pivot Temporality — Этап 1: археология существующей реализации

Дата: 2026-09-18. Цель по ТЗ (`temporality-pivot-tz.md` §22 Этап 1): до изменения кода зафиксировать существующие реализации Claim/ClaimRelation/Observation/Evidence/Frame/Event/Attention/RenderPacket/Entity/Relation/Procedure/CognitiveEmission/replay/Debugger и фактический путь данных. Ничего не переписывается.

Формат: для каждого concept — где живёт, что уже умеет, чего не хватает относительно ТЗ пивота. В конце — сводная таблица пробелов и вывод, какие этапы 2–5 какие именно места трогают.

---

## 1. Фактический путь данных (проверено по коду)

```
world (executor)                     frp/world/effect.go, world/events.go
  ↓ world.observation / world.effect events
event log (postgres/memory)          frp/substrate/postgres/*.go, frp/substrate/memory/store.go
  ↓ (bootstrap)                       frp/ingest/{ingest,walk,extract}.go — observation → candidate claim + evidence
projections                          frp/projection/{regions,edges,entity}.go
  ↓
render                               frp/render/render.go — Packet по frame+objective+world
  ↓
model adapter                        frp/model/* (openai-chat-completions / OpenRouter)
  ↓ CognitiveEmission (frp/cognition/emission.go)
step pipeline                        frp/runtime/step/step.go + frp/cognition/reducer.go
  ↓ атомарный CommitStep: events + claims + claim transitions + frame + executions
cognitive_steps (persisted)          render_packet + emission + hashes — «что видел агент»
  ↓ следующий frame
loop
```

## 2. Concept map

### 2.1 Event — `frp/protocol/event.go`

Двухвременная модель (`tx_time`, `valid_time`), `provenance` map, метрики доверия (`source_trust`, `evidence_strength`, ...). Типы событий — открытые строки; фактический реестр: `claim.candidate|supported|refuted|superseded`, `attention.selected|suggested`, `frame.created|transitioned`, `episode.started`, `world.registered|observation|effect`, `execution.created|started|completed|failed`, `affordance.requested`.

**К ТЗ:** достаточно; смена фокуса и verification — новые типы событий поверх существующей модели (менять Event не нужно).

### 2.2 Claim — `frp/cognition/claim.go`, таблица `claims`

Поля: `claim_id`, `proposition`, `confidence`, `status`, `created_event`, `valid_from/to`, триплет `subject/predicate/object` (M14). Статусы: `candidate | supported | refuted | superseded`.

Маппинг на статусы ТЗ §5:

| ТЗ | Сейчас | Комментарий |
|---|---|---|
| OBSERVED | — | нет; все claims — утверждения. Прямых «наблюдений» как claims нет; наблюдения живут в `world.observation` events |
| HYPOTHESIS | `candidate` | создаются каждым шагом модели (`step.go` L153 — **всегда** candidate) |
| CONFIRMED | `supported` | **достижим только ручным API** `POST /v1/claims/{id}/transitions` (`frp/runtime/httpapi/server.go`); в автоматном пайплайне НИКОГДА не проставляется |
| REFUTED | `refuted` | есть: `emission.claim_ops[].op="refute"` → событие `claim.refuted` (step.go L223–237) |
| SUPERSEDED | `superseded` | есть: `claims[].supersedes` → `claim.superseded` с lineage (step.go L238–256) |

**Пробел №1 (главный):** lifecycle разорван — модель может опровергать и заменять, но **не подтверждать**. В реальных прогонах (`frp_mem_bench_*`) нет ни одного `supported` — вся память = candidates. Поэтому warm-память неотличима от гипотез (AC2/AC5 нарушены de facto).

### 2.3 Evidence — `cognition.Commit.Evidence` (`store.go` L15–24), таблица `claim_evidence`

`Commit.Evidence []string` — ссылки на events; `ListClaimEvidence` читает. Store проверяет существование events (M13).

**Пробел №2:** в step-пайплайне claims модели создаются **без evidence** (step.go L144–160: только proposition/confidence). Evidence есть только у bootstrap-claims из ingest. Т.е. путь «гипотеза → на каком наблюдении основана» для модельных claims отсутствует (AC1 частично).

### 2.4 ClaimRelation — `frp/cognition/relation.go`

`supports | contradicts | derived_from | supersedes`, weight, evidence_event. Коммитится только через ручной claims API; step-пайплайн их не создаёт. Достаточно для lineage ТЗ, не используется автоматически.

### 2.5 Observation (эмиссии) — `cognition/emission.go` L14–69

`Observation{ref, interpretation}` — интерпретация моделью уже существующих events. Ref-нормализация устойчива к ошибкам модели. Это не «OBSERVED-статус», а аннотация. `reasoning[]` — произвольный текст (kind/text), сохраняется в `cognitive_steps.emission`.

**К ТЗ:** reasoning не структурирован; причина смены фокуса из него не извлекается.

### 2.6 Frame — `frp/frame/frame.go`, `reducer.go`; таблица `frames`

`Focus{type,id|query}`, `WorkingSet []Ref` (cap 32), `Mode(explore|exploit|reflect|verify)`, `Revision`. Переход — `Transition{AsOf, Operations}`: `attend` (focus), `pin/unpin` (refs), mode/zoom. Хранение: `frames.data` = **результат** применения transition; сами operations в БД не пишутся (событие `frame.transitioned` несёт parent/frame/emission/rejections/warnings/memory_tensions — step.go L309–323).

**Пробел №3 (для ТЗ §7/§8):**
- история смен фокуса восстанавливается только диффом parent→child по цепочке frames (возможно, но дорого и без причин);
- **причина/триггер смены фокуса не фиксируется нигде** — ни в transition, ни в событии. ТЗ §7 требует `from/to/trigger/evidence/frame_before/frame_after/timestamp`.

### 2.7 Attention — `frp/attention/engine.go`

`SelectAmbient(candidates, previous, limit)` — линейный скоринг Features×Weights; `SelectDeliberate(target)` — валидация фокуса. Events `attention.suggested/selected` — журнал; из render скрыты (audit only, render.go L66–76). Процедуры рендерятся через `MatchProcedures` (порог 0.1, cap 8).

**К ТЗ:** кандидат-генерация из долговременной памяти есть (`world_memory`); релевантности к objective нет (ранжирование по confidence — что и породило вредную доставку в бенчмарке).

### 2.8 RenderPacket — `frp/render/render.go` (`Packet`, `Section{Kind,Attention,Items}`)

Секции: `identity, objective, map, focus, attention_health, world_memory, memory_health, identity_health, periphery, working_set, procedures, recent, affordances`. Trim-порядок зафиксирован. `world_memory`: только `candidate|supported` (live на cutoff), сортировка confidence desc, **cap 24**; item = `{ref, proposition, confidence, scope, [subject/predicate/object]}` — **без статуса знания, без происхождения, без evidence, без «проверено/опровергнуто»** (render.go L268–313).

**Пробел №4 (ТЗ §10/§12):** delivered item не сообщает модели, факт это или гипотеза; refuted-знания не доставляются вообще (даже как «ранее опровергнуто — не ходи туда»); формат — плоский список, не `CONFIRMED/REFUTED/INVESTIGATED/RELEVANT`.

### 2.9 Entity / EntityRelation — `frp/entity/entity.go`, `frp/projection/entity.go`, таблицы `entities/entity_relations`

Проекция из claim-триплетов; учитываются `candidate|supported`. Рефы `type:name`. Достаточно как основа для «объектов исследования» (ТЗ §7/AC3: «исследовался ли объект»), но связи «объект ↔ гипотеза ↔ проверка» пока нет.

### 2.10 Procedure — `frp/procedure/procedure.go`, таблица `procedures`

`semantic_trigger, preconditions, affordance_sequence, expected_outcomes, evidence_execution_ids, successes/failures/success_rate`. Рендер с эпизодным скоупом мира; анти-паттерны отсекаются порогом. Релевантен ТЗ как «episode experience» §11; гигиена уже работает.

### 2.11 CognitiveEmission — `frp/cognition/emission.go` L209+

`schema frp.cognitive-emission.v1`: `observation[], reasoning[], claims[{proposition,confidence,status,supersedes}], claim_ops[{op:refute,claim}], attention[{op,target}], frame_ops[{op:pin/unpin,ref}], actions[{affordance,args}], completion`. Нормализация частых ошибок модели (ref-объекты, tool-call форма actions). `EmittedClaim.Status` декодируется, но **игнорируется** step-пайплайном (всё равно candidate) — несоответствие внутри схемы.

**К ТЗ:** нет операции `confirm` (аналог refute для supported) и нет `evidence`/`origin` у claims. Причину смены фокуса модель может выразить только свободным текстом.

### 2.12 Replay — `frp/replay/`, `frp/timetravel/`, HTTP `POST /v1/replay`

Детерминированный: события → восстановление цепочки frames, `frame_hash/deterministic_hash`; render-пакеты **не реконструируются** — хранятся в `cognitive_steps.render_packet` (+hash). Проверено живьём 2026-09-18 на БД прогона бенчмарка.

**К ТЗ (§20, AC7):** каркас достаточен; вопрос только в том, чтобы новые события (verification, focus change) проходили через тот же event-log → replay автоматически.

### 2.13 Cognitive Debugger — `debugger/` (React/Vite)

API-клиент (`debugger/src/api.ts`): events, frame, execution, render, replay, blame, fork, model-step, modelConfig, rebuildRegions. UI: timeline событий (реверсивный, с мемоизацией), RenderPacket-просмотр, model progress. Claims/траектории/дельт нет.

**Пробел №5 (ТЗ §13/§14, AC6):** нет представлений trajectory (объект×время), Memory Delta, «откуда пришло знание», «почему фокус сменился».

## 3. Сводные пробелы относительно ТЗ

| # | Пробел | Пункты ТЗ | Этап |
|---|---|---|---|
| 1 | Нет автоматического подтверждения claims (`supported` недостижим в loop-е) | §5 CONFIRMED, Этап 2 | 2 |
| 2 | Модельные claims без evidence/origin | §4.1, §6, AC1 | 2 |
| 3 | Смена фокуса без причины (trigger/evidence) в журнале | §7, §9, AC3, AC6 | 3 |
| 4 | `world_memory` не различает факты/гипотезы/опровергнутое; нет investigation history в доставке | §10–12, AC2, AC4, AC5 | 4 |
| 5 | Debugger без trajectory/Memory Delta/provenance-навигации | §13–14, AC6 | 5 |

## 4. Что переиспользуем, что строим (вывод)

**Переиспользуем как есть:** Event-модель и event log; claim-статусы и transition-граф (`CanTransition` уже разрешает candidate→supported); `claim_evidence`; reducer/step-пайплайн и его атомарность; replay/timetravel; render-механика секций и trim; entity-проекцию; procedures.

**Минимальные добавления по этапам (не переписывая):**

- **Этап 2 (knowledge lifecycle):** `confirm` в `claim_ops` (симметрично `refute`, тот же guard-механизм, событие `claim.supported`); опциональный `evidence[]` в `EmittedClaim` → `Commit.Evidence` (проверка существования events уже есть в store); статус `OBSERVED` не заводим как новый claim-статус — вместо этого claims с evidence из `world.observation`/execution-результатов получают признак через origin (см. ниже) — иначе ломаем существующий transition-граф и миграции.
- **Этап 3 (investigation history):** новое событие `focus.changed` (from/to/trigger/evidence/frame_before/frame_after) — эмитится step-пайплайном при `OpAttend`, когда focus реально меняется; trigger извлекается из emission (claim refuted/superseded в том же шаге / reasoning). Плюс projection «investigated objects»: claim×entity×execution.
- **Этап 4 (warm memory):** render `world_memory` → структурированные подсекции `CONFIRMED / REFUTED(кратко, как «не ходи») / PREVIOUSLY INVESTIGATED / RELEVANT`; сортировка не по confidence, а по статусу×релевантности; refuted доставляются только в этой форме (AC2/AC5).
- **Этап 5 (visualization):** debugger — trajectory-вью (объект×время из `focus.changed`+claims) и Memory Delta между frames; без нового frontend.

**Спорное решение, которое предстоит принять на этапе 2:** ТЗ §5 требует OBSERVED как отдельный статус, но введение пятого статуса ломает `CanTransition`, проекции и миграцию БД. Альтернатива — `origin` поле (`observed|inferred`) у claim: observed-claims создаются только ingest'ом/execution-результатами, inferred — моделью. Это сохраняет граф статусов и даёт семантику «факт мира vs гипотеза». В документе этапа 2 зафиксирую выбор.
