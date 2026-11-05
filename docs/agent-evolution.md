# Эволюция агентов: версии, предложения, наблюдение (v2)

Статус: действующая модель. Заменяет контур качества
(docs/agent-quality-loop.md — упразднён).

## 1. Принцип

> **Применяй мгновенно, доказывай потом, откатывай одним нажатием.**

Версия определения агента — это релиз. Улучшение нельзя доказать до
применения — его можно только **зафиксировать после**, на реальной работе.
Поэтому:

* применение предложения — одна кнопка, без условий и гейтов;
* каждая применённая версия живёт под наблюдением («канарейка») и сравнивается
  с версией-базой по agentSRE-метрикам за дни реальной работы;
* деградация — громкий алерт и откат одной кнопкой (авто-отката нет);
* откат создаёт **новую** версию с содержимым прежней — история не
  переписывается никогда.

Тестовые прогоны (контрольные примеры) — не гейт, а необязательная проверка
перед применением и источник примеров «из практики».

## 2. Объекты

### 2.1. Версия (`workspace_agent_version`, расширена)

Неизменяемый снапшот определения + скомпилированный промпт (было). Новое:

| Поле | Смысл |
| --- | --- |
| `kind` | `edit` (ручная правка) \| `proposal` (применено предложение) \| `rollback` |
| `baseline_version` | версия, которую эта заменила; с ней сравниваются метрики |
| `rollback_of` | исходная версия, если `kind=rollback` |
| `change_summary` | «что изменится в поведении», 3–5 строк человеческим языком |

`Run.AgentVersion` (уже есть) пинит ран на версию — метрики проецируются без
новых сигналов.

### 2.2. Предложение (`workspace_agent_proposal`, расширено)

Единый объект для всех источников:

| Поле | Смысл |
| --- | --- |
| `source` | `agent` (agent_propose) \| `user` (кнопка «Предложить улучшение») \| `supervisor` |
| `change_summary` | поведенческое резюме изменения (генерируется при создании) |
| `context` | JSONB: `{"run_id", "project"}` — из какого рана родилось |
| `applied_version` | номер версии, в которую применилось |

Существующие поля (problem/change/effect/evidence/definition/…) сохраняются.
Предложение хранит **полное целевое определение** — diff считается против
базовой версии, apply — тот же транзакционный путь, что и раньше
(`ApplyAgentProposalAudited` остаётся: claim + version++ + снапшот + аудит).

### 2.3. Окно наблюдения (per-agent)

На агенте (`workspace_agent`): `observation_days` (по умолчанию 7),
`observation_min_runs` (по умолчанию 10). Окно закрывается, когда выполнены
**оба** условия: прошло ≥ `observation_days` **и** набрано ≥
`observation_min_runs` ранов версии. Агент «раз в месяц» ставит min_runs=3 и
days=30; «плотный» — min_runs=20 и days=2.

### 2.4. Контрольные примеры (`workspace_agent_check_case`, новая)

Минималистичная замена сьютов (старые таблицы сьютов/прогонов/кандидатов/
калибровки **удалены**, данные вычищены — набиваем заново):

* `input`, `must_contain[]`, `must_not_contain[]`, `from_run_id`, `created_by`;
* сохранить пример можно из рана одним кликом;
* «Проверить на примерах» — необязательный дымовой прогон против выбранной
  версии (отдельные вызовы модели, паттерн-проверки), результат — информационный
  сигнал в карточке предложения/версии, **не условие применения**;
* судья упразднена вместе с калибровкой.

## 3. Метрики (agentSRE / SLI версии)

Ежедневный rollup в `workspace_agent_version_metrics` (PK: agent_id, version,
day). Источники — уже существующие данные: `workspace_run` (agent_version,
status, task_id, turns), `human_request` + проекция вердиктов
(`workspace.Verdict`, `ClassifyRunOutcome`), журнал (токены/стоимость из
`run.completed`, `operation.reconciled`, `supervisor.verdict`,
`approval.requested`). Новых событий в журнале для метрик не нужно.

| SLI | Считается как | Сигнал из |
| --- | --- | --- |
| Успешность | succeeded / runs (failed, cancelled, turn_limited отдельно) | workspace_run.status + ClassifyRunOutcome |
| Приёмка | accepted / corrected / rejected по вердиктам | human_request + Verdict() |
| Автономность | вмешательств на ран (ask_human + согласования) | human_request, approval.requested |
| Rework | доля задач, перезапущенных после рана этой версии (self-join по task_id) | workspace_run |
| Цена и время | средние токены, $, wall-time на успешный ран | run.started/completed |
| Инциденты | неопределённые операции на ран, needs_attention супервайзера | operation.reconciled, tool.failed, supervisor.verdict |

### 3.1. Правила честности

* сравнение like-for-like: окно версии против **такого же по длине** окна
  версии-базы, кончающегося на момент переключения;
* минимум наблюдений: `observation_min_runs` ранов на версию; иначе —
  «недостаточно данных», никогда «улучшение»;
* шум: разница называется сигналом, только если |Δ| превышает порог
  `max(0.05, 1/sqrt(min(n_current, n_baseline))` по доле; иначе «в пределах
  шума»;
* итог окна: **improved / stable / degraded / insufficient** — improved|degraded
  только когда ≥2 первичных SLI (успешность, коррекции, вмешательства,
  инциденты) сигналят в одну сторону и ни один — в противоположную.

### 3.2. Алерт и откат

* Каждый ночной проход (вместе с rollup) пересчитывает итог для версий в
  наблюдении; **degraded** → событие `agent.version.degraded`, уведомление
  владельцу/писателю агента, красный баннер в UI.
* «Откатить» → `POST …/versions/{v}/rollback` (уже есть) создаёт новую версию
  `kind=rollback, rollback_of=текущая, baseline=текущая` с определением версии
  `v`. Откат сам идёт под наблюдение на общих основаниях.
* Авто-откат сознательно не делаем.

## 4. API (контракт)

Существующие пути (изменённое семантика — «без гейта»):

| Путь | Изменение |
| --- | --- |
| `GET …/agents/{id}/proposals` | + `source`, `change_summary`, `context`, `applied_version` |
| `POST …/agents/{id}/proposals` | тело + `source` (`user`), `context`; сервер дописывает `change_summary` |
| `POST …/agents/{id}/proposals/{id}/apply` | **без гейта**: применяет всегда; в ответе `{version}` |
| `POST …/agents/{id}/proposals/{id}/reject` | без изменений |
| `GET …/agents/{id}/versions` | элементы + `kind`, `baseline_version`, `rollback_of`, `change_summary`, `verdict`, краткие метрики |
| `POST …/agents/{id}/versions/{v}/rollback` | без изменений (создаёт новую версию) |
| `GET …/agents/{id}/versions/diff?from=&to=` | без изменений (структура + промпт) |

Новое:

| Путь | Ответ |
| --- | --- |
| `POST …/agents/{id}/proposals/from-text` | тело `{problem, context?}` → LLM-черновик предложения: `{proposal_id, change_summary, definition}` (pending, source=user) |
| `GET …/agents/{id}/proposals/{id}/diff` | тот же формат, что `versions/diff`, + `change_summary` |
| `GET …/agents/{id}/versions/{v}/metrics?days=` | `{observation, current, baseline, delta[], daily[]}` (см. §5) |
| `GET/PUT …/agents/{id}/observation` | `{days, min_runs}` |
| `GET/POST …/agents/{id}/check-cases` | список / добавить (`{input, must_contain[], must_not_contain[], from_run_id?}`) |
| `DELETE …/agents/{id}/check-cases/{cid}` | — |
| `POST …/agents/{id}/check-cases/run?version=` | синхронный дымовой прогон: `{passed, failed, results:[{input, pass, detail}]}` |

Удаляются: `…/evaluation-suite` (GET/PUT), `…/evaluation-runs`,
`…/evaluations` (GET/POST), `…/quality`, кандидаты golden-кейсов, судья и
калибровка (`…/judge*`), вся гейт-логика `evalgate.go` (202/409-машина,
свежесть, held-out). Роут `…/proposals/{id}/apply` сохраняется — очищается.

## 5. Метрики: JSON

```json
{
  "observation": {
    "state": "observing | closed",
    "verdict": "improved | stable | degraded | insufficient | null",
    "days": 7, "min_runs": 10,
    "started_at": "…", "elapsed_days": 3, "runs": 12
  },
  "current":  { "runs": 12, "succeeded": 10, "failed": 1, "cancelled": 1,
                "turn_limited": 0, "verdicts": { "accepted": 8, "corrected": 3, "rejected": 1 },
                "interventions": 2.1, "reworks": 1,
                "tokens_avg": 41000, "cost_usd_avg": 0.21, "wall_ms_avg": 183000,
                "uncertain_ops_avg": 0.4, "incidents": 0 },
  "baseline": { "…": "та же форма за окно версии-базы" },
  "delta": [
    { "metric": "success_rate",   "current": 0.83, "baseline": 0.90, "delta": -0.07, "signal": "noise" },
    { "metric": "correction_rate","current": 0.25, "baseline": 0.11, "delta":  0.14, "signal": "signal" }
  ],
  "daily": [ { "day": "2026-11-01", "runs": 3, "succeeded": 3, "…": "…" } ]
}
```

## 6. UI: вкладка «Эволюция» (переписывается начисто)

1. **Карточка здоровья текущей версии**: наблюдение («день 3/7 · ранов 12/10»),
   таблица SLI «сейчас / база / Δ» с метками signal/noise, итог окна.
2. **Инбокс предложений**: бейдж источника (агент/пользователь/супервайзер),
   `change_summary`, контекст («из рана …»), кнопки **Применить** / Отклонить /
   Проверить на примерах (опция). Модалка деталей: поведенческое резюме +
   структурный diff + diff промпта.
3. **«Предложить улучшение»**: свободный текст («агент делает X, а надо Y») →
   `from-text` → черновик с diff на ревью. Кнопка «Предложить улучшение агенту»
   в просмотре рана — с прикреплённым контекстом.
4. **Лента версий**: дата, автор, kind-бейдж, change_summary, вердикт-бейдж,
   spark метрик; действия Сравнить / **Откатить**.
5. **Настройка наблюдения**: days / min_runs.
6. **Контрольные примеры**: список, «сохранить этот ран как пример», дымовой
   прогон.

Деградация: красный баннер на карточке агента + уведомление.

## 7. События

`agent.definition.proposed` (source: agent|user|supervisor),
`agent.definition.applied`, `agent.definition.rollback` — уже существующий
след изменений определения; новый — жизненный цикл наблюдения:
`agent.version.observed` (окно закрыто, вердикт), `agent.version.degraded`
(тревога: версия хуже базы). Все — через журнал (durable outbox/POST),
best-effort: строки версий и метрики — источник истины.

## 8. Что вырезано и почему

| Вырезано | Почему |
| --- | --- |
| Eval Gate (202/409, свежесть в рабочих днях, base_version_stale, held-out) | улучшение нельзя доказать до применения; гейт превратился в QA-ритуал |
| Калибровка судьи, judge-degraded | сложность без продуктовой отдачи; судья удалена вместе с механикой |
| Харвест golden-кейсов + ревью-очередь + stale_after | заменены «сохранить пример из рана» в один клик |
| Мёртвые прогоны, обходы с justification | вместе с гейтом |
| Таблицы `workspace_agent_evaluation_*`, `workspace_eval_case_candidate`, `workspace_judge_calibration` | упразднены; контрольные примеры живут в `workspace_agent_check_case` |

## 9. Этапность реализации

* **P0**: предложения (источники, from-text, change_summary), apply без гейта,
  лента версий, откат, diff предложений. Продукт пригоден к работе.
* **P1**: rollup метрик, карточка здоровья, сравнение с базой, вердикт окна,
  алерт о деградации.
* **P2**: контрольные примеры + дымовой прогон.

Реализуются одним заходом; порядок — только в разборе коммитов.

## 10. Ограничения

* Метрики агрегируются по агенту целиком (кросс-проектно): сдвиг состава
  задач между версиями — источник шума; правила §3.1 честно обозначают
  «в пределах шума», но не нормализуют смесь задач.
* Наблюдение не останавливает применение следующей версии: досрочное
  переключение закрывает окно как есть (вердикт может быть `insufficient`).
* Скиллы и знания развиваются своей жизнью (living-skills,
  docs/knowledge-evolution.md) — здесь только определение агента.
