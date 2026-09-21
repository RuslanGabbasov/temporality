# AML Experiment 2 — Memory maturation, evolution and conflict (2026-09-19)

## 1. Executive summary

Проведён длинный прогон adaptive memory layer (10 sessions × 5 задач, arm A control
vs arm D adaptive, flip среды на session 6) плюс три специализированных сценария:
конфликт окружений (prod/staging), guardrail fixture и второй модель (qwen3-235b).

Главные измеренные факты:

| метрика (main, 10×5) | A (control) | D (adaptive) | D/A |
|---|---:|---:|---:|
| задачи решены | 50/50 | 50/50 | = |
| tool calls | 176 | 111 | 0.63 |
| failed calls | 56 | 16 | 0.29 |
| total tokens | 181 482 | 146 484 | 0.81 |
| warm tokens (s2–10) | 161 836 | 128 190 | 0.79 |
| warm failed calls | 49 | 11 | 0.22 |
| steady state, tokens/session (s3–5) | 18 801 | 13 328 | 0.71 |
| steady state, tokens/session (s7–10) | 17 205 | 14 397 | 0.84 |

Память созревает (confidence растёт только по независимым сессиям), переживает
изменение среды с одноразовой ценой (1 противоречие → подавление за 1 сессию),
новая стратегия доминирует со следующей сессии, конфликт окружений разрешается
жёстким фильтром по environment (0 кросс-env инъекций у D против 1 у semantic-only B).

Вердикт по §13 ТЗ: **эксперимент успешен по 9 из 10 критериев**; guardrail
механизм не активировался ни разу (модель не повторяет идентичные фейлы) —
зафиксирован как неиспользованный, не как провал.

## 2. Гипотеза

> Может ли внешняя memory layer самостоятельно поддерживать актуальное
> состояние опыта агента на длинной дистанции, не превращаясь в источник
> устаревших или конфликтующих советов?

Проверяется циклом: scope → retrieve → rank → hint → action → outcome →
reinforce/weaken → ↺ на 10 сессиях с изменением среды и конфликтом контекстов.

## 3. Setup

- Модель: `deepseek/deepseek-v4.1-flash` (temperature 0, max_output_tokens 16384,
  reasoning low); второй модель: `qwen/qwen3-235b-a22b-2507`.
- Harness: классический tool-calling loop (`aml/harness`), память подключена
  4 хуками (TaskStart / BeforeToolCall / AfterToolCall / TaskEnd). Harness не менялся.
- Arms: A — без памяти; B — semantic-only ranking (конфликт-диагностика);
  D — полный пайплайн: semantic scope + temporal/env/version ranking +
  pre-action activation + guardrail + adaptive feedback с cause-level attribution.
- Мир: 5 сервисов; billing-api версионирован (prod v3→v5 flip, staging v4),
  каждое окружение принимает свой auth-метод; flaky-gw возвращает 503 без
  `params.mirror=true`.
- Instrumentation (новое относительно Exp 1):
  - `Result.Cause` (auth / parameter / not_found / server) — атрибуция причины фейла;
  - lifecycle-события RECALLED / INJECTED / ACKNOWLEDGED / REUSED / VALIDATED /
    CONTRADICTED / UNRESOLVED / REINFORCED / WEAKENED (с confidence before/after);
  - `environment` + `version_context` у ассетов; dedup key включает environment
    и value рекомендации (разные стратегии = разные ассеты);
  - evidence diversity: confidence растёт только при подтверждении в новой сессии.

Данные: `benchmarks/aml-{main,conflict,guardrail,secondmodel}-run.json`;
forensic DB: `aml2_arm_{a,d}`, `aml2c_arm_{b,d}`, `aml2g_arm_{a,d}`, `aml2m_arm_d`;
сырые таблицы: `docs/benchmarks/aml-{main,conflict,guardrail,secondmodel}-run.md`.

## 4. Main run: long horizon (A vs D, flip на s6)

### 4.1 Динамика по сессиям (tokens/session)

```
A: 19.6K 19.4K 18.5K 19.8K 18.1K | 17.2K 17.2K 17.1K 17.3K 17.2K
D: 18.3K 16.0K 13.2K 13.4K 13.4K | 14.6K 14.4K 14.5K 14.2K 14.5K
                                flip →
```

- A не улучшается со временем (нет памяти): 5–7 фейлов каждую сессию до конца.
- D выходит на steady state за 2 сессии: 1 фейл/сессию, 10 calls vs 17 у A.
- Flip (s6): D +1 фейл и +1.3K токенов к s5 — разовая цена адаптации; с s7
  вернулся к steady state.

### 4.2 Per-task steady state (calls/fail/tokens, среднее на сессию)

| задача | A pre s3–5 | D pre s3–5 | A post s7–10 | D post s7–10 |
|---|---|---|---|---|
| T1 billing auth | 4.0/2.0/4013 | 1.0/0/1726 | 3.0/1.0/2828 | 1.0/0/1756 |
| T2 payments auth | 4.7/2.7/4061 | 1.0/0/1708 | 5.0/3.0/4222 | 1.0/0/1692 |
| T3 templates param | 2.0/0/2400 | 2.0/0/2399 | 2.0/0/2399 | 2.0/0/2399 |
| T4 search pagination | 4.0/0/5020 | 3.0/0/4395 | 4.0/0/4897 | 3.0/0/4422 |
| T5 billing version | 3.3/1.3/3306 | 3.0/1.0/3100 | 3.0/1.0/2860 | 3.0/1.0/4128 |

Наблюдения:
- T2 — самый чистый эффект: A каждый раз заново перебирает auth (docs обещают
  все три метода), D по памяти сразу service_token. 5 calls/3 fail → 1 call/0 fail.
- T3 — контр-пример: задача решается чтением docs, ассета нет, разницы нет.
  Память не нужна там, где всё написано в документации.
- T1 после flip дешевеет и у A (pat стоит первым в docs и теперь работает) —
  часть эффекта flip-окна объясняется миром, а не агентом; корректное сравнение
  всё равно в пользу D (1 call vs 3).
- T5 — единственная задача, где D post-flip дороже собственного pre-flip
  (4128t vs 3100t): из-за oscillation version-param ассета (см. §8.1).

## 5. Maturation: trajectories

Пример (billing prod oauth, ассет b03b4411) — создан s1, рос по независимым
сессиям, убит flip-ом:

```
s1 created  conf 0.70  (1 сессия)
s2 REINFORCED 0.70 → 0.762
s3 REINFORCED 0.762 → 0.809
s4 REINFORCED 0.809 → 0.844
s5 REINFORCED 0.844 → 0.870
s6 flip: REUSED → 403 auth → WEAKENED 0.870 → 0.391   (последняя инъекция)
s7–s10 больше не инъектируется и не подтверждается
```

Новый ассет (billing prod pat, 8c441ae3):

```
s6 created 0.70  (из фейла oauth → успеха pat в той же сессии flip-а)
s7 REINFORCED 0.70 → 0.762  (REUSED+VALIDATED в каждой сессии)
s8 → 0.809;  s9 → 0.844;  s10 → 0.870
```

Стабильные ассеты (payments service_token, search pat, search pagination)
дошли до 0.92–0.93 с confirm=9–10 без единого противоречия.

Evidence diversity подтверждена тестом и данными: повторные подтверждения
внутри одной сессии дают factor 0.2 и не растят confirmation_count; счётчик
сессий совпадает с числом независимых подтверждений.

## 6. Flip & adaptation (главный сценарий ТЗ §5)

- Цена flip-а для D: **1 лишний failed call и одна contradict-инъекция в s6**
  (oauth-ассет ещё ранжировался выше threshold при version-mismatch ×0.5),
  далее подавление. Recovery cost A vs D в billing-окне (s6–10): 10 vs 6
  failed calls; billing-токены примерно равны (28.5K vs 29.6K — подсказки
  компенсируют сэкономленные ретраи), вся экономия D — на невырожденных задачах.
- Sessions until stale suppression: **1** (последняя INJECT старого ассета — s6).
- Sessions until new strategy dominates: **1** (pat-ассет создан в s6,
  VALIDATED в каждой сессии с s7).
- Никакого «зомби-режима»: старый ассет не инъектируется, но и не удаляется
  (эволюция знания инспектируема — §4.1 philosophy сохранена).

## 7. Специализированные сценарии

### 7.1 Conflict (prod vs staging, 4 сессии, B vs D)

Оба окружения валидны, требуют разных auth (prod=pat, staging=oauth):

- **B (semantic-only)**: кросс-env утечка случилась — s2 P1 (prod) получил
  staging-oauth подсказку, REUSED → 403 → CONTRADICTED, 1 потерянный вызов.
  Далее модель сама выбирала правильную из двух подсказок, но обе инъектируются
  каждый раз (2 hints на задачу).
- **D (env-жёсткий фильтр)**: 0 кросс-env инъекций за весь прогон; все 3
  инъекции staging-ассета пришли в staging-задачи, все VALIDATED. Prod-ассет
  не существовал до s4 (см. §8.2) — D решал prod-задачи по docs.
- Итоговые токены равны (B 19.0K vs D 19.4K) — на 8 задачах эффект фильтра
  виден в точности инъекций, не в экономии.

### 7.2 Guardrail (flaky-gw, 3 сессии, A vs D)

- Guardrail **не активировался ни разу** (0 GUARDRAIL events): обе модели после
  первого 503 идут в status.page (detail ошибки прямо советует) и больше
  идентичный фейл не повторяют. repeated_failed = 0 во всех прогонах
  (включая main). Вывод: для компетентных моделей идентичные повторные фейлы —
  не типовой паттерн; guardrail остаётся страховкой, но проверить его in vivo
  нужно fixture-ом без подсказки обхода.
- Параметрическая память сработала: warm-окно D — 1 call/0 fail/1985t
  (s3) против A 4 calls/1 fail/4880t; warm-токены 5.9K vs 9.8K (0.60).

### 7.3 Second model (qwen3-235b-a22b-2507, arm D, 4 сессии, без flip)

- Успешность 13/20 vs 50/50 у deepseek: qwen стабильно сдаёт T5/T2 — переименовывает
  параметры (`invoice_id` вместо `id`, `refund_id`) → 404 → не пытается снова.
- Memory compliance: INJECTED 13 → REUSED 9 (69%), ACKNOWLEDGED-but-not-reused 3,
  VALIDATED 6, CONTRADICTED 0, UNRESOLVED 3 (те самые 404 — слой корректно
  не обвинил память: cause not_found не противоречит auth/param-ассетам).
- Вывод: готовность следовать hints у второй модели сопоставима; узкое место —
  её собственная error-recovery, память не навредила (0 contradicted).

## 8. Failure cases и ограничения

### 8.1 Version-param oscillation (главный дефект цикла)

Ассет «invoices.get требует params.version=2» дважды ослаблялся (s2: 0.70→0.315,
s8: 0.618→0.278) и дважды воскрешался merge-путьём extraction (s3–s6, s9–s10),
застряв в STALE 0.46. Причина: модель передаёт `version: 2` числом, а не строкой
→ 400 parameter → attribution честно считает это contradicted (value совпал,
тип нет), но следующая удачная попытка со строкой снова подтверждает ассет.
Следствие: с s9 подсказка не инъектируется (STALE × threshold), D снова платит
1 фейл на T5 каждую сессию (see §4.2). Это измеримый налог на консервативный
resurrection-порог (0.5) при 15% шаге: честному ассету нужно ~4 чистые сессии
на воскрешение. Кандидат на исправление: merge из нового failure→success в новой
сессии должен считаться полноценной валидацией (сильнее bump / немедленное
воскрешение), а contradiction по причине «значение совпало, но тип другой»
можно различать только на уровне typed comparison в reuse-детекции.

### 8.2 Extraction gap: удачный первый вызов не создаёт память

Правило «failure→success» не создаёт ассет, если модель угадала сразу.
В conflict-фикстуре модель стабильно брала pat для prod с первого раза (pat
первый в docs) → prod-ассет появился только в s4 после случайного отклонения.
В main T3 (channel решается docs) ассета нет вообще — и не нужно. Цена gap-а:
после сброса удачливости (или смены модели) знание «что работало» теряется.
Кандидат: создавать low-confidence «what-worked» ассеты на первый успех и
матереть их только независимыми подтверждениями.

### 8.3 Атрибуция причины — по статусу, не по семантике

Cause выводится из HTTP-кода/источника ошибки (auth-check vs param-check vs 404
vs 5xx). Случаи «правильное значение, неправильный тип» неотличимы от «неверное
значение» (§8.1). Скрытых ложных противоречий не наблюдалось: все 3 CONTRADICTED
в main — реальные (1 stale oauth, 2 version-typing).

### 8.4 Однократность прогонов

Каждая конфигурация прогнана один раз (модель temp=0, мир детерминирован,
но выбор модели стохастичен на сэмплере провайдера). Разницы уровня 10–15%
между arms на отдельных задачах (T5) могут быть шумом; агрегаты по 50 задачам
устойчивы (fail 56 vs 16 — не шум).

## 9. Метрики ТЗ §11

| метрика | значение |
|---|---|
| tool calls / task (warm) | A 3.55 → D 2.22 |
| failed calls / task (warm) | A 1.09 → D 0.24 |
| tokens / task (warm) | A 3596 → D 2849 |
| memories created (D main) | 6 ассетов, 21 EXTRACT (merge включительно) |
| recalled / injected / reused | 49 / 53 / 38 (включая pre-action) |
| reuse rate | 38/53 = 72% |
| validated / contradicted / unresolved | 34 / 3 / 1 |
| helpful : misleading | 34 : 3 (89% валидных reuses) |
| sessions until stale suppression | 1 |
| sessions until new strategy dominates | 1 |
| recovery cost после flip | +1 failed call, +1.3K tokens (одна сессия) |
| guardrail activations | 0 |

## 10. Чек-лист §13 ТЗ

| критерий | статус | evidence |
|---|---|---|
| стабильные memories дешевле в использовании | ✅ | §4.1, §4.2 |
| повторное исследование сокращается | ✅ | calls 17→10 per session |
| env change → временная деградация с восстановлением | ✅ | s6 bump, s7 steady |
| stale memories перестают инъектироваться | ✅ | oauth-ассет: last inject s6 |
| новые memories доминируют после подтверждения | ✅ | pat-ассет s6→s7+ |
| конфликтные memories разрешаются контекстом | ✅ | D: 0 кросс-env инъекций (B: 1 утечка) |
| misleading hints редки | ✅ | 3/53 инъекций (5.7%) |
| false contradictions не систематичны | ✅ | 0 ложных; 4 UNRESOLVED разобраны |
| overhead не съедает выигрыш | ✅ | D дешевле A на всех warm-окнах |
| harness не изменился | ✅ | hooks-only интеграция |

## 11. Выводы

1. Цикл experience → memory → activation → reuse → outcome → update работает
   на длинной дистанции и **дешевле контроля в каждом warm-окне** (0.71–0.84
   по токенам, 0.22 по фейлам), при равном качестве решения (50/50).
2. Созревание честное: confidence и счётчики растут только от независимых
   сессий; повторный показ памяти сам по себе ничего не растит.
3. Изменение среды переживается за одну сессию по цене одного противоречия;
   ни одна устаревшая память не «зомбировалась» после подавления.
4. Конфликт окружений — решается жёстким фильтром (env-партиция мира);
   semantic-only retrieval без контекста среды воспроизводит ожидаемую утечку.
5. Слабые места, найденные экспериментом: oscillation-лок STALE-ассета
   (§8.1), extraction gap удачных вызовов (§8.2), guardrail без in-vivo
   активации (§7.2). Все три — точечные инженерные правки, не архитектурные.

## 12. Следующий эксперимент

По ТЗ пивота: подключение layer к реальному agent harness через HTTP/MCP и
проверка на реальной рабочей траектории (код-агент, реальный репозиторий).
Prior engineering: (a) resurrection из STALE при независимом подтверждении,
(b) «what-worked» extraction, (c) guardrail fixture без явного пути обхода.
