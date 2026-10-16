# Дашборд здоровья системы (System Dashboard)

Дата: 2026-10-16. Заменяет бестолковую вкладку «Обзор» (`/project`, `ProjectOverview.tsx`)
рабочим дашбордом и переносит туда переработанную вкладку Observability.

## 1. Цель

Когда одновременно идут сотни запросов от пользователей и запусков по триггерам,
оператору нужна одна страница, отвечающая на вопросы за секунды:

* что происходит прямо сейчас — сколько ранов выполняется, сколько вызовов
  модели в полёте, кто ждёт подтверждения человека;
* здоровы ли провайдеры — латентность, ошибки, ретраи, «застрявшие» стримы;
* во что обходится работа — токены и деньги по проектам за окно;
* не деградировал ли контур качества — гейты, override-ы, judge-degraded,
  незакрытые предложения эволюции;
* куда провалиться — в раны, операции, трассы, агентов, знания.

Принцип: **агрегации считаются на сервере, фронтенд только рендерит**. Клиентский
перебор журнала (лимит 500 событий на страницу) при реальной нагрузке невозможен.

## 2. Структура страницы

Маршрут `/dashboard` (пункт меню «Дашборд»; до переименования — `/project`,
который остаётся рабочим редиректом) получает внутренние вкладки:

| Вкладка | Содержимое | Источник |
|---|---|---|
| Обзор | health-полоса, провайдеры/модели, проекты, оргструктура | journal stats + kernel overview |
| Качество | сводка по агентам + здоровье петли качества | kernel quality summary |
| Саги | текущий `ProjectOverview` (учёт по проекту) | `/v1/agent/cost/project-budget` |
| Знания | переработанный Observability (битемпоральный обзор) | `/v1/observations/knowledge` |

`/observability` и `/project` остаются рабочими маршрутами-алиасами (редирект
на `/dashboard`; observability открывает вкладку «Знания»), пункта Observability
в главном меню нет.

### 2.1. Вкладка «Обзор»

* **Health-полоса**: running-раны, вызовы моделей в полёте, ошибки моделей за
  окно, подтверждения в ожидании (pending human approvals), неопределённые
  операции (uncertain operations). Каждый чип — проваливание: раны, операции,
  входящие.
* **Провайдеры и модели**: по модели (и провайдеру, когда известен) — вызовы,
  in-flight, ошибки, p50/p95 латентности, токены, ретраи/наджи. Сортировка по
  активности; проблемные строки — сверху.
* **Проекты**: за окно (по умолчанию 24 ч) — раны (всего/выполняется/ошибки),
  вызовы моделей, токены, стоимость, pending-подтверждения, последняя активность.
  Клик по строке выбирает проект. Разрез по оргструктуре: проекты группируются
  по org unit'ам текущего принципала.
* Обновление: кнопка + автоповтор раз в 30 с, пока вкладка видима.

### 2.2. Вкладка «Качество»

* **Петля качества (окно 7 дней)**: гейты passed/blocked/overridden, override
  rate (цель < 5 %), доля judge-degraded прогонов, протухшие golden-кейсы.
* **По агентам** (видимые принципалу): последняя оценка (verdict, pass rate,
  judge agreement), версия определения, pending-предложения эволюции, ссылка на
  карточку агента.

### 2.3. Вкладка «Знания» (бывший Observability)

Сверху — сводка здоровья знаний (alive/stale/invalidated, конфликты, свежие
изменения), ниже — битемпоральные фильтры компактными пресетами и кластеры тем,
сравнение снапшотов как сейчас. Содержимое без изменений переезжает из
`Observability.tsx`.

## 3. API

### 3.1. Journal: `GET /v1/observations/stats/health`

Серверная агрегация событий журнала за окно. Параметры: `since` (RFC3339,
по умолчанию теперь−1 ч), `project` (опционально; без него — глобально, с
ограничением до видимых проектов принципала, как в `listObservations`).

Ответ (все счётчики считаются по событиям с `occurred_at >= since`, «сейчас» —
по тем же событиям, терминальное событие могло не прийти):

```json
{
  "since": "…", "generated_at": "…",
  "models": [{
    "model": "gpt-5.2", "provider": "openai",
    "calls": 120, "completed": 118, "failed": 2, "in_flight": 3,
    "retries": 4, "nudged": 1,
    "p50_latency_ms": 3200, "p95_latency_ms": 9800,
    "total_tokens": 1234567, "cached_tokens": 0
  }],
  "totals": { "model_calls": 120, "model_failures": 2, "in_flight": 3, "nudged": 1 },
  "runs": { "started": 15, "completed": 12, "failed": 2, "cancelled": 0, "running_now": 1 },
  "approvals": { "requested": 8, "granted": 6, "rejected": 1, "timed_out": 0, "auto_granted": 3, "pending": 1, "wait_ms_avg": 45000 }
}
```

Семантика:

* `in_flight` — `model.started` в окне без `model.completed`/`model.failed`
  с тем же (`run_id`, `turn`). Вызов, начавшийся до окна, не учитывается —
  задокументированное ограничение (окно по умолчанию 1 ч превышает любые
  таймауты вызова).
* Латентности и провайдер — из `model.completed` (`latency_ms`, `provider`).
* Процентили считаются по накопленным значениям (nearest-rank).
* `pending` approvals — `approval.requested` в окне без ответа (granted /
  rejected / timed_out) по ключу (run, operation_id, arguments_hash).
* Реализация: чистая функция `ComputeHealthStats(events, now)` в пакете
  `observation` + методы стора (postgres — один проход по окну без лимита
  страниц; memory — перебор среза), опциональный интерфейс, который httpapi
  проверяет ассертом.

### 3.2. Kernel: `GET /v1/agent/cost/projects-overview`

Сводка по проектам за окно. Параметры: `window` (например `24h`, максимум 7d,
по умолчанию 24h). RoleReader. Ядро читает журнал служебным токеном (окно +
отдельный lookback 7d для approval-событий), агрегирует per-project и
фильтрует результат по видимым проектам принципала (перечисление проектов в
workspace + орг-цепочка). Цены — своя таблица ядра.

Ответ:

```json
{
  "window": "24h", "generated_at": "…",
  "projects": [{
    "project": "omni", "org_units": ["unit-research"],
    "runs_started": 15, "runs_running": 1, "runs_failed": 2,
    "model_calls": 120, "total_tokens": 1234567,
    "total_cost_usd": 12.34, "pending_approvals": 1,
    "last_activity_at": "…"
  }]
}
```

### 3.3. Kernel: `GET /v1/workspace/quality/summary`

RoleReader. Видимость — как у списка агентов (`ListAgentsVisible` + фильтр по
`project`, если задан). Собирается лёгкими SQL-запросами (не через тяжёлый
`AgentQualityReport`):

* последний прогон оценки на агента (`DISTINCT ON` по
  `workspace_agent_evaluation_run`);
* протухшие goldens на агента (разбор `cases` jsonb,
  `workspace_agent_evaluation_suite`);
* pending-предложения (`PendingProposalCounts`);
* события гейтов за 7 дней из `observation_events`
  (`agent.evaluation.gate.*`) и доля `verdict='judge-degraded'` за 7 дней.

Ответ:

```json
{
  "agents": [{
    "agent_id": "analyst", "name": "Analyst", "project": "omni",
    "definition_version": 4,
    "last_evaluation": { "at": "…", "verdict": "pass", "passed": 8, "failed": 0, "pass_rate": 1.0, "judge_agreement": 0.91 },
    "pending_proposals": 1, "stale_goldens": 0
  }],
  "loop": { "window_days": 7, "gate_passed": 3, "gate_blocked": 1, "gate_overridden": 0, "override_rate": 0.0, "judge_degraded_runs": 0, "evaluation_runs": 3 }
}
```

## 4. Права

* Journal stats — тот же gate, что и листинг событий (Reader, скоупинг
  видимых проектов для глобальных запросов).
* Kernel endpoints — RoleReader; агрегаты не раскрывают ничего, чего принципал
  не видит в списках ранов/агентов/проектов.

## 5. Что не входит в скоуп

* Алертинг и пороги (когда появятся SLA — отдельный док).
* Исторические тренды/графики по окнам длиннее 7 дней (хранение агрегатов —
  отдельная задача).
* Потоковая передача (SSE) — дашборд опрашивает.

## 6. Этапы

1. Journal `stats/health` (+ память/postgres, тесты).
2. Kernel `projects-overview` (+ `since` в пейджинге CostAPI).
3. Kernel `quality/summary`.
4. Фронтенд: `Dashboard.tsx` на `/dashboard` с вкладками, переезд Observability,
   меню, локали ru/en.
5. Валидация: `go build ./...`, `go test ./... -count=1`, `npx tsc -b`,
   `npm test`; проверка вручную на стенде не входит в агентский прогон.

## 7. Открытые вопросы

* Автообновление чаще 30 с при 500 req/s — нужен кэш агрегатов в journal
  (сейчас считаем на каждый запрос, один SQL-проход по окну).
* Группировка моделей по провайдеру в UI, когда `provider` пуст у старых
  событий (до появления поля) — группируем по model.
