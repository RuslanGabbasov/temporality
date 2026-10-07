# Experiment 9 — corpus of experience evolution comprehension

Назначение: проверить, что **человек-оператор** понимает эволюцию опыта агента по
одному только UI (Experience Timeline), без чтения событий и внутреннего
состояния. В отличие от экспериментов 4–8, проверяется не событийная модель, а
читаемость жизненного цикла знаний.

Корпус зафиксирован как acceptance-фикстура
`debugger/src/fixtures/experiment9-webconfig.json` (генерируется
детерминированно из описанных ниже seed-последовательностей) и сворачивается
фолдом `debugger/src/experience.ts`; утверждения — в
`debugger/src/experience.fixtures.test.ts`.

## Сценарий

Проект `exp9-webconfig`: приложение `webconfig` с тремя конкурирующими
гипотезами конфигурации базы данных:

- **Гипотеза A** — переменная окружения `DATABASE_URL` (проверяется через
  `webconfig migrate`, lane `migrate`);
- **Гипотеза B** — файл `config.yaml` (проверяется через `webconfig serve`,
  lane `serve`);
- **Гипотеза C** — флаг `--db-url`, перекрывающий обе (lane `serve`).

Окружение меняется между запусками: v1 (работают все три механизма) → v2
(переменные окружения вырезаны, флаг удалён — работает только файл) → v3
(механизм окружения возвращён, флага по-прежнему нет).

## Seed-последовательности

Восемь запусков `webconfig-20261007-01…08`, 161 событие, 8 узлов знаний.

| Run | Что происходит | Жизненный цикл |
|-----|----------------|----------------|
| 01 | Разобран README, `go build`, `migrate` с `DATABASE_URL` → exit 0, `go test` → exit 0 | A1 proposed; auto-узлы `go build`/`go test` proposed (kernel-heuristic) |
| 02 | Повторная проверка env (exit 0), проверка config.yaml (exit 0) | A1 **confirmed** (extraction-reverification.v1); B1 proposed; `go test` confirmed |
| 03 | Флаг перекрывает env и файл | C1 proposed; A1 и B1 **challenged** (extraction-contradiction.v1); B1 использован как hint в этом же run |
| 04 | Флип на v2: env → exit 1, флаг → exit 2, файл → exit 0 | A1 (challenged) **предложен и использован** — stale use; B1 rescued → confirmed; C1 **invalidated** оператором без преемника; E1 (end-to-end) proposed; `go test` reused |
| 05 | Ночной прогон | A1 предложен, но **проигнорирован**; B1 confirmed повторно (repeated confirmation); `go build` confirmed |
| 06 | Флип на v3: env снова работает | A2 proposed — **ресуррекция** утверждения A1; оператор инвалидирует A1 с указанием на A2 (lineage A1→A2) |
| 07 | Аудит «что сейчас валидно» | A2/B1/E1/`go test` отозваны и использованы; AUDIT proposed (никогда не отзывается) |
| 08 | Release checks | Отзываются только живые узлы: A2, B1, E1, `go test`, `go build` |

Каждое событие фиксирует правдоподобные поля реального журнала: kernel-потоки
`<hash>/event/NNNNNN`, hint-события от `temporality-activation`, ручные
инвалидации от `human-operator` (source `temporality-manual`, `run: null`),
approval-события с argv для словаря механизмов.

## Вопросы оператору (acceptance)

Ответ должен читаться из UI без консоли:

1. Какая гипотеза появилась первой? (A1, run 01)
2. Какие подтверждены и чем? (A1 — повторное сведение в run 02; B1 — дважды,
   runs 04–05; правило видно в точках `validated`)
3. Что противоречило старым гипотезам? (C1, run 03; причины в `contradicted`)
4. Какое знание использовано устаревшим? (A1 использован в run 04 в состоянии
   challenged; предложен-но-проигнорирован в run 05)
5. Что умерло и почему? (C1 — v2 удалил флаг, преемника нет; A1 — superseded)
6. Что воскресло? (A2 восстанавливает утверждение A1 в run 06; lineage A1→A2)
7. Что валидно сейчас? (A2, B1, E1, auto-узлы; run 08 отзывает только их)
8. Что реально использовалось? (hint.used / execution-reuse.v1 по каждому узлу)

## Покрытие сценариев ROADMAP

- **competing hypotheses** — B1 и C1 в одной lane `serve`, C1 появляется при
  живом B1;
- **repeated confirmation** — A1 validated (run 02), B1 validated дважды
  (runs 04, 05);
- **contradiction** — extraction-contradiction.v1 к A1 и B1 в run 03;
- **resurrection** — A2 повторяет утверждение A1 в новом живом узле;
- **stale knowledge** — использование challenged-узла (run 04) и
  offered-but-ignored (run 05);
- **supersession** — инвалидация A1 с указанием преемника A2 (lineage);
- **different scopes** — пять lane: `build`, `end-to-end`, `migrate`, `serve`,
  `test`; end-to-end-утверждение получает вторичные scope.

## Замечания по модели

- Терминальные состояния (`invalidated` и др.) не допускают переходов, поэтому
  «ресуррекция» — всегда новый узел; связь с прошлым выражается lineage и
  кластеризацией по содержанию.
- Lineage-рёбра выводятся из текста причины инвалидации (id вида
  `…/knowledge/…`), поэтому наследование scope работает и для ручных
  инвалидаций оператора.
- Наследник наследует lane предшественника (приоритет: execution scope >
  lineage > собственный scope утверждения) — поэтому E1 не является
  преемником C1 по lineage: его end-to-end scope обязан быть собственным.
