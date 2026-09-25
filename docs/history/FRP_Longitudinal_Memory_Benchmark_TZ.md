# ТЗ: FRP Longitudinal Memory Benchmark

## 1. Цель

Проверить главную гипотезу FRP, которую текущий fresh-task benchmark ещё не проверил:

> **Накопленная память FRP должна превращаться в актив, уменьшающий стоимость и/или количество когнитивной работы на последующих задачах в том же мире.**

M0–M16 уже реализованы. Сейчас не нужно расширять функциональность FRP. Нужно провести контролируемый эксперимент на существующей реализации и, если потребуется, внести только минимальные изменения для корректного межэпизодного reuse памяти.

Предыдущий benchmark показал: на свежей coding-задаче FRP примерно в 4× дороже по total tokens и в 9× медленнее классического harness. При этом Claims и Procedures скоупятся по эпизоду, поэтому ключевая ценность памяти фактически не участвует в сравнении.

## 2. Главный вопрос

Ответить:

> Если агент один раз исследовал репозиторий и сформировал структурированную память о нём, становится ли следующая независимая задача на том же репозитории существенно дешевле/быстрее и требует ли она меньше повторного discovery?

Измерять минимум:

- total prompt tokens;
- total completion tokens;
- total tokens;
- wall-clock;
- cognitive steps;
- affordance requests;
- `read_file`;
- повторные чтения одного ресурса;
- `run_tests`;
- патчи/записи;
- использованные Claims;
- использованные Procedures;
- долю нового discovery;
- успешность решения;
- регрессии/изменения тестов.

## 3. Инварианты эксперимента

Нельзя:

- менять модель между сравниваемыми сериями;
- менять reasoning level или temperature;
- менять fixture ради улучшения результата;
- вручную подсказывать агенту содержимое Claims/Procedures;
- давать warm-агенту transcript предыдущего эпизода;
- удалять память между warm-эпизодами;
- считать эксперимент успешным только потому, что записи существуют в БД.

Warm-агент получает знания **только через штатный FRP memory/render/attention pipeline**.

## 4. Fixture

Использовать ту же coding fixture:

- Go repository `example.com/ledger`;
- `--scale 8`;
- около 27 файлов;
- около 17 plausible decoys;
- два скрытых бага;
- тесты менять запрещено;
- checker: `go test ./...`.

Если fixture уже модифицирована предыдущими экспериментами — создать чистую копию/checkout.

## 5. Эксперимент

### Phase A — Fresh Episode

Создать чистый FRP substrate.

Запустить задачу №1:

> Исследовать репозиторий, найти и исправить два скрытых бага. Тесты менять нельзя.

Сохранить Episode, Frames, RenderPackets, Events, Claims, Relations, Entities/Relations, Procedures, Executions, attention data, outcome и metrics.

### Phase B — Warm Episode

Не очищать substrate.

Создать **новый независимый Episode** для того же repository/world.

Запустить независимую задачу №2. Она должна требовать реального анализа репозитория, но не быть простым повторением задачи №1.

Предпочтительно использовать другой bug seed / другой набор скрытых дефектов в той же архитектуре. Если текущая fixture этого не позволяет — сделать отдельную fixture аналогичной сложности на том же репозитории.

Warm Episode не получает transcript, список найденных ранее багов или ручные summary.

### Phase C — Fresh Control

Для задачи №2 провести fresh-control:

- тот же repository;
- та же задача;
- чистый substrate;
- та же модель и параметры.

Получаем:

```text
A = Episode 1 / FRP fresh
B = Episode 2 / FRP warm
C = Episode 2 / FRP fresh control
```

Главное сравнение: **B vs C**.

Дополнительное: **B vs A**.

## 6. Повторяемость

Минимум 3 независимые пары warm/fresh-control, предпочтительно 5.

Порядок чередовать:

```text
pair 1: fresh → warm
pair 2: warm → fresh
pair 3: fresh → warm
...
```

Если порядок может влиять на окружение — зафиксировать это.

Основной агрегат — **медиана**, а не среднее.

## 7. Проверка межэпизодной памяти

Не переписывать архитектуру. Сначала найти фактический путь данных.

### Claims

Проверить:

- почему `claimsScopedByEvents` ограничивает видимость Claims;
- какие Claims являются world/repository knowledge, а какие episode-local hypotheses;
- можно ли безопасно дать world/repository scope durable Claims;
- сохраняется ли provenance до исходного Observation/Event;
- работают ли supersession/refutation между эпизодами;
- не происходит ли leakage branch-local/speculative Claims.

Минимальное логическое разделение:

```text
World/Repository knowledge
Episode-local hypothesis
Execution observation/result
```

В глобальную память попадают только знания с достаточной provenance/evidence и допустимой областью действия.

### Procedures

Проверить:

- текущий scope;
- возможность repository/world scope;
- условия попадания Episode в Procedure;
- poisoning mitigation;
- minimum evidence;
- success/failure smoothing;
- использование Procedure из нового Episode;
- provenance Procedure → Episodes/Executions.

### Render

Проверить:

- попадают ли существующие Claims/Procedures в RenderPacket нового Episode;
- становятся ли они attention candidates;
- может ли Attention их выбрать;
- видит ли модель происхождение knowledge;
- не теряется ли memory при trimming;
- не существует ли «мертвая память»: записи есть в БД, но cognition её не получает.

### Attention

Проверить candidate generation из durable memory и влияние:

- semantic relevance;
- graph proximity;
- recency;
- trust;
- task relevance;
- activation;
- explicit pins;
- budget;
- missed candidates.

Особенно измерить:

> Была ли релевантная память, но она не попала в Frame?

Это отделяет проблему storage от проблемы attention/render.

## 8. Instrumentation

Если нужных метрик нет, добавить минимальную instrumentation.

Для каждого warm step желательно:

```json
{
  "memory_candidates_total": 0,
  "memory_candidates_rendered": 0,
  "memory_candidates_selected": 0,
  "memory_candidates_new": 0,
  "memory_candidates_reused": 0,
  "claims_reused": 0,
  "procedures_reused": 0,
  "prior_episode_refs": 0,
  "repeated_reads": 0
}
```

Названия адаптировать к существующей схеме.

Не создавать отдельную memory subsystem.

## 9. Анализ поведения

### Case 1 — Memory works

Агент использует существующие знания, меньше повторяет discovery, меньше читает известное, быстрее формирует гипотезы и дешевле решает задачу.

→ Основная гипотеза подтверждается на fixture.

### Case 2 — Memory exists but is ignored

Релевантные Claims/Procedures существуют, но не попадают в attention/render/frame.

→ Это проблема retrieval/Attention/Render, не обязательно фундаментальная проблема FRP.

### Case 3 — Memory is retrieved but doesn't help

Модель видит память, но всё равно повторяет discovery и не сокращает trajectory.

→ Серьёзный сигнал против текущей формы cognitive memory.

### Case 4 — Memory actively hurts

Stale/incorrect Claims ведут агента по ложному пути, увеличивают действия или мешают найти новый дефект.

→ Зафиксировать provenance и точный момент, когда память вызвала неверное действие.

## 10. Go/No-Go

Основной engineering gate:

```text
median(warm total tokens) <= 0.7 × median(fresh-control total tokens)
```

При этом качество решения не должно ухудшиться.

Дополнительные признаки:

- меньше cognitive steps;
- меньше repeated reads;
- меньше discovery actions;
- больше полезного reuse Claims/Procedures;
- не растёт число ложных гипотез;
- уменьшается wall-clock.

0.7 — engineering threshold, а не статистический закон.

### PASS

Warm ≤ 0.7 × fresh-control и качество не ухудшилось.

→ Memory-as-asset подтверждена на данной fixture.

Следующие шаги: накопительный multi-episode benchmark, затем A1/A2/A3 performance rescue, затем long-horizon.

### PARTIAL

Warm дешевле fresh, но эффект слабее 30% либо проявляется только в отдельных метриках.

→ Исследовать Attention/Render/Procedure retrieval.

### FAIL

Warm примерно равен fresh, хотя релевантные Claims/Procedures реально доступны модели.

→ Провести один controlled experiment с доставкой релевантного memory subset через штатный Render/Attention.

Если cognition всё равно не дешевеет и trajectory не сокращается → рассмотреть hybrid FRP meta-layer.

### NEGATIVE

Warm дороже fresh из-за misleading/stale memory.

→ Исследовать trust, temporal validity, supersession/refutation и memory hygiene. Не лечить увеличением context budget.

## 11. Накопительный тест

Если Phase A/B показывает эффект:

```text
Episode 1 — fresh
Episode 2 — warm
Episode 3 — warm
Episode 4 — warm
...
```

Проверить, что ценность действительно накапливается, а не является одноразовым эффектом.

Таблица:

| Episode | Fresh/Warm | Tokens | Time | Steps | Reads | Reused Claims | Reused Procedures |
|---|---|---:|---:|---:|---:|---:|---:|

Построить графики:

1. total tokens vs episode;
2. cognitive steps vs episode;
3. discovery/read actions vs episode;
4. reused memory vs episode.

## 12. Long-horizon smoke test

После warm experiment, если он имеет смысл, провести задачу на 30–50+ cognitive steps.

Проверить:

- context overflow;
- memory rendering;
- repeated reads из-за recent trimming;
- attention degradation;
- deterministic replay;
- потерю уже известной информации.

Не тратить на этот тест время до получения результата warm experiment.

## 13. Что НЕ делать

На этом этапе не реализовывать:

- новый M17 только ради эксперимента;
- новый graph storage;
- Neo4j;
- Kafka/Redpanda;
- Redis;
- отдельную vector DB;
- новый planner;
- новый agent framework;
- полный rewrite RenderPacket;
- новый executor;
- новый protocol.

Не смешивать memory experiment с A1/A2/A3:

- A1 — cacheable render;
- A2 — native tool-calling;
- A3 — short aliases.

Они должны тестироваться отдельной веткой, иначе невозможно определить источник эффекта.

## 14. Итоговый отчёт

Создать:

`docs/benchmarks/frp-longitudinal-memory-YYYY-MM-DD.md`

Структура:

1. Executive summary
2. Hypothesis
3. Experimental setup
4. Exact model/provider parameters
5. Fixture
6. Fresh runs
7. Warm runs
8. Fresh-control runs
9. Metrics
10. Memory reuse analysis
11. Attention/render analysis
12. Repeated-read analysis
13. Failure cases
14. Useful reused knowledge
15. Ignored/harmful memory
16. Results
17. Go/No-Go
18. Conclusions
19. Next experiment

В отчёте явно разделять:

- измеренный факт;
- наблюдаемое поведение;
- интерпретацию;
- гипотезу.

Не выдавать расчётную оценку за измеренный эффект.

## 15. Acceptance criteria

- [ ] fresh + warm + fresh-control проведены;
- [ ] минимум 3 независимые пары, предпочтительно 5;
- [ ] memory reuse идёт через штатный FRP pipeline;
- [ ] нет transcript leakage;
- [ ] измерены tokens/time/steps/actions;
- [ ] измерен reuse Claims/Procedures;
- [ ] измерены repeated reads;
- [ ] проверен путь memory → attention → frame → cognition;
- [ ] зафиксированы полезная, игнорируемая и вредная память;
- [ ] deterministic replay не сломан;
- [ ] создан итоговый markdown report;
- [ ] принято решение PASS / PARTIAL / FAIL / NEGATIVE;
- [ ] перечислены следующие инженерные действия.

## 16. Главный критерий эксперимента

Не отвечать на вопрос:

> «Есть ли у нас Claims и Procedures?»

Они уже есть.

Ответить на вопрос:

> **«Изменяет ли накопленная память поведение нового агента настолько, чтобы это имело измеримую экономическую и когнитивную ценность?»**

Если да — FRP начинает конкурировать с классическим harness на другой оси: не стоимость первого запуска, а стоимость работы с миром на протяжении множества эпизодов.

Если нет — это нужно зафиксировать до дальнейшего наращивания архитектуры.
