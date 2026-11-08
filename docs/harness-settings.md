# Настройки харнеса: модели и бюджеты в одном месте

Статус: действующая модель (v1).

Всё, что **генерализовано** влияет на работу агентного харнеса, живёт в двух
слоях с одним домом на каждый — ничего не выбирается «от балды».

## 1. Модельные роли (БД, редактируемые)

Панель супервайзера (`GET/PUT /v1/workspace/supervisor`), ключи
`workspace_setting`, каждое имя модели валидируется по реестру провайдеров
при сохранении. Пустая роль = каскад фолбэков ниже.

| Роль | Ключ настройки | Что обслуживает | Каскад при пустой роли |
| --- | --- | --- | --- |
| judge | `eval.judge.model` (есть) | судейство: оценки, конфликты знаний, area-саммари | выключен |
| vision | `supervisor.vision.model` (есть) | картинки, когда основная модель не мультимодальная | основная модель |
| compaction | `harness.compaction.model` | карточка состояния при компакции контекста | judge → env-модель |
| title | `harness.title.model` | заголовки ранов | judge → env-модель |
| extraction | `harness.extraction.model` | извлечение знаний по завершении рана | модель рана → judge → env-модель |

Резолвер — `kernel/agent` → `Activities.HarnessModel` (сборка в
`cmd/agent-kernel/knowledgeareas.go`): слот роли → `providerModelClient`
(провайдер + ключ) → судья. Дополнительные фолбэкаи — в самих потребителях
(`extractionModelClient`, `evaluationModel`, `GenerateTitle`).

Env-модель по умолчанию (`TEMPORALITY_MODEL_BASE_URL` / `_ID` / `_API_KEY`) —
точка отсчёта для `activities.Model` (дефолтный aux-клиент) и для
роутинга живых ходов (модель рана → workspace-провайдер → env-fallback).

## 2. Бюджеты, таймауты, капы (env, единая таблица)

Дом — пакет `kernel/harness` (`Settings`, `FromEnv`, `Current`); дефолты =
сегодняшние значения. Невалидное значение деградирует до дефолта.

| Переменная | Дефолт | Что делает |
| --- | --- | --- |
| `TEMPORALITY_MAX_MODEL_REQUEST_BYTES` | 2 МиБ − 32 КиБ | потолок инпута `call_model` (лимит Temporal `limit.blobSize.error` = 2 МиБ; выше — честный `run.failed` вместо TMPRL1103-зависания) |
| `TEMPORALITY_COMPACTION_TRIGGER_BYTES` | 1.2 МиБ | порог компакции контекста (docs/context-compaction.md) |
| `TEMPORALITY_COMPACTION_CARD_MAX_BYTES` | 8 КиБ | размер карточки состояния (едет в каждом следующем запросе) |
| `TEMPORALITY_COMPACTION_TAIL_BYTES` | 256 КиБ | дословный хвост после карточки |
| `TEMPORALITY_COMPACTION_KEEP_GROUPS_MIN` / `_MAX` | 2 / 8 | сколько целых групп ходов сохранять в хвосте |
| `TEMPORALITY_COMPACTION_DIGEST_HEAD_BYTES` / `_TAIL_BYTES` | 3584 / 512 | кап одного сообщения в дайджесте для суммаризации |
| `TEMPORALITY_TOOL_OUTPUT_MAX_BYTES` / `_TAIL_BYTES` | 512 КиБ / 16 КиБ | кап тул-результата в истории (head+tail с пометкой) |
| `TEMPORALITY_IMAGE_ENCODED_MAX_BYTES` | 512 КиБ | кап закодированного vision-блока (base64 ×4/3 на проводе) |
| `TEMPORALITY_IMAGE_MAX_SIDE` | 1568 | длинная сторона картинки (тайлы провайдера) |
| `TEMPORALITY_IMAGE_SOURCE_MAX_BYTES` | 5 МиБ | кап исходного файла/загрузки до декодирования |
| `TEMPORALITY_MODEL_ACTIVITY_TIMEOUT` | 20m | потолок `kernel.call_model` |
| `TEMPORALITY_TOOL_ACTIVITY_TIMEOUT` | 16m | потолок `kernel.run_tool` (должен перекрывать sandbox-потолок 15m) |

## 3. Replay-безопасность

Бюджеты, которые **меняют команды** workflow (порог компакции, гард
пейлоада, капы истории — они двигают размеры запросов и, следовательно,
решения), пинятся на ран: `AgentRun` один раз читает снапшот
(`kernel.harness_budgets`, local activity — паттерн `RecordEventLocal`) и
работает только с ним. Смена настроек посреди рана не ломает его replay:
решения повторяются идентично. Раны до введения механизма (и тестовые
пробы с нулевым снапшотом) получают `harness.DefaultBudgets()` — те же
литералы, что зашиты в их истории.

Активити-сторона (кап картинок, карточка, таймауты) читает `Current()`:
результат активити лежит в истории и replay не зависит от окружения.

## 4. API

`GET /v1/workspace/supervisor` отдаёт роли (`judge_model`, `vision_model`,
`compaction_model`, `title_model`, `extraction_model`), HAB-коэффициенты,
статус эмбеддингов и **эффективный** `harness`-снапшот (read-only) — одно
место, чтобы видеть, чем харнес реально работает. `PUT` принимает роли
(`compaction_model`/`title_model`/`extraction_model` — pointer-семантика:
пропущенное поле хранится как есть, пустая строка сбрасывает роль);
валидация имён — по реестру провайдеров.

## 5. Ограничения

* Бюджеты редактируются через env и применяются к **новым** ранам
  (пин на старте); UI-редактирование бюджетов — следующий шаг, место уже
  есть (`harness`-секция ответа).
* Модельные роли — глобальные на инсталляцию; per-project роли не нужны,
  пока у проектов есть свои дефолтные модели ранов.
