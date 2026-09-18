# Pivot Temporality — Этап 4: warm memory

Дата: 2026-09-18. ТЗ §10–12: доставить следующему эпизоду не просто propositions, а **состояние знания**; REFUTED никогда не выглядит фактом; память компактная и структурированная; существующий render pipeline.

## Что было (пробел №4 из `01-archaeology.md`)

`world_memory` = плоский топ-24 claims сортировкой по confidence, item `{ref, proposition, confidence, scope}`. Без статуса, без evidence, без опровергнутых. Бенчмарк 2026-09-18 показал следствие: вредный decoy-claim (conf 0.85) доминировал в доставке и уводил warm-агента по ложному пути.

## Что сделано

### `world_memory` → четыре группы по состоянию знания (`frp/render/render.go`)

Порядок фиксирован — PREVIOUSLY CONFIRMED → PREVIOUSLY REFUTED → PREVIOUSLY INVESTIGATED → LIVE HYPOTHESES:

| Группа | Источник | Item | Cap |
|---|---|---|---|
| `confirmed` | supported claims мира (не свои), live на cutoff | `{ref, proposition, state, confidence, evidence:[≤2 event refs], [triple]}` | 8 |
| `refuted` | refuted/superseded, retired на cutoff, свежие первые | `{ref, proposition, state, note:"do not re-investigate without new evidence"}` — **без confidence** | 6 |
| `investigated` | `focus.changed` события прошлых эпизодов мира (Этап 3), dedupe, свежие первые | `{ref:event, state, target:"query:X"}` | 6 |
| `hypothesis` | live candidates | `{ref, proposition, state, confidence, [triple]}` | 6 |

Ключевые свойства:

- **Trim eats speculation first**: бюджетная лестница режет секцию с конца — первыми страдают hypotheses, затем investigated, refuted, и только потом confirmed-факты.
- **Time-travel честность**: группы фильтруются по cutoff (claim retired после cutoff не показывается как refuted-история; live-группы учитывают valid_to).
- **Evidence у confirmed**: до 2 event-рефов — модель может цитировать и верифицировать; facts distinguishable from speculation by construction.
- Refuted item не несёт confidence — число рядом с опровергнутой гипотезой читалось бы как вес факта.
- Свои claims эпизода исключены (как раньше — они в recent/identity).

### Промпт модели (`frp/model/model.go`)

Добавлена секция объяснения world_memory: confirmed — строить на них; refuted — не ре-исследовать без новых данных; investigated — уже изучено; hypothesis — проверить и подтвердить/опровергнуть.

### Версии

`render-0.4.5` → `render-0.5.0` (формат доставки изменился; персистящиеся render packets несут версию).

## Что НЕ сделано (осознанно)

- **Семантическая релевантность** внутри групп не вводилась (нет embeddings — `embedding_model:"none"`); сортировка confidence×recency. Это A-эксперименты поверх, не блокер: главная вредность старой доставки (неразличимость статусов) устранена конструктивно.
- Отдельного «quality gate» для world knowledge vs episode experience (ТЗ §11) пока нет: confirmed-группа по построению требует evidence (Этап 2), hypotheses явно помечены. Ужесточение (например, minimum evidence count) — после бенчмарка Этапа 6.
- `investigated` строится полным сканом событий прошлых эпизодов в рендере — на малых эпизодах приемлемо; если бенчмарк покажет дорого — вынесем в проекцию.

## Тесты

- `TestRenderWorldMemoryDeliversKnowledgeState`: порядок групп, confirmed с evidence, refuted без confidence, investigated из focus.changed, hypothesis live.
- `TestRenderWorldMemoryAcrossEpisodes` обновлён: refuted теперь доставляется, но с `state:"refuted"`.
- Полный `go test ./...` + `scripts/smoke.py` — зелёные.
