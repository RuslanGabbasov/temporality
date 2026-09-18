# Pivot Temporality — Этап 5: visualization (trajectory + Memory Delta)

Дата: 2026-09-18. ТЗ §13–14: показать историю исследования (не таблицу claims и не только timeline), и дать Cognitive Debugger'у отвечать на вопросы «почему агент пошёл сюда / что знал в момент T / что изменилось между Frame».

## Что сделано

### Логика: `debugger/src/trajectory.ts` (чистые функции над event log)

- `buildTrajectory(events)` — лейны исследования:
  - **focus lane**: последовательность `focus.changed` (Этап 3) — target, trigger (`deliberate` / `hypothesis_retired` / `hypothesis_confirmed`), evidence-рефы, frame;
  - **claim lanes**: по одному на claim — lifecycle-узлы ○ candidate → ● supported → ✕ refuted / → superseded с proposition и evidence.
- `claimKnowledgeAt(events, frameId)` — «что агент знал в момент T»: fold событий до создания выбранного frame (включая его transition) — claims со статусами **на тот момент** (time-travel семантика cutoff'а).
- `memoryDelta(events, frameId)` — «что изменилось между двумя Frame»: окно строго между parent-frame cutoff и текущим transition: added / confirmed / refuted / superseded + focus move с trigger. Proposition'ы подтягиваются из birth-событий (transition-события несут только claim_id).
- `frameCutoffIndex` — индекс события-границы frame (transition или created).

### UI: `debugger/src/Investigation.tsx` + вставка в `App.tsx`

Новая панель в инспекторе (когда frame выбран):

- **Investigation trajectory** — лейны focus/claims с глифами состояний, триггерами и evidence.
- **Memory delta** — по выбранному frame: focus-переход с trigger + группы added/confirmed/refuted/superseded + свёрнутый список «What the agent knew here (N)».

Обе карточки — чистая деривация из уже загруженного event list: **ноль новых API**, ноль backend-изменений.

### Тесты

`trajectory.test.ts`: лейны с trigger/evidence; cutoff по frame; knowledge-at-frame-2 vs frame-3 (статусы на момент); delta одного шага (не всего лога) с proposition из birth-события; пустая delta для root/чужого frame. Debugger suite: 50/50.

## Ответы на вопросы ТЗ §14 (теперь)

| Вопрос | Механизм |
|---|---|
| Что агент знал в момент T? | `claimKnowledgeAt` + персистенные render packets (`cognitive_steps.render_packet`) |
| Почему пошёл в этот файл? | focus.changed: from/to/trigger/evidence + reasoning эмиссии (emission_id) |
| Почему перестал рассматривать путь? | claim lane: candidate → ✕ refuted (+ evidence-событие проверки) |
| Что уже проверял? | focus lane + claim lanes |
| Какие знания из прошлого эпизода? | world_memory группы с state (Этап 4) — в том же render packet |
| Что изменилось между Frame? | Memory delta card |
| Где появилась ошибочная гипотеза? | claim lane → первый узел ○ (frame/time/evidence) |
| Почему следующему агенту показали это знание? | world_memory item: state + evidence refs + origin (Этап 4) |

## Осознанно не сделано

- Timeline-графика «объект × время» с линиями между узлами (как ASCII-диаграмма ТЗ) — текущий вид лейнов даёт ту же информацию компактнее; графическую отрисовку имеет смысл делать после того, как Этап 6 покажет, какими реальными данными она наполняется.
- Memory Delta между произвольными двумя frame (не parent→child) — тривиально добавить позже (`claimKnowledgeAt(a) vs (b)`), UI пока показывает пошаговую дельту.
