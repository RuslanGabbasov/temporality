---
order: 12
section: Интеграции
title: Внешние агенты (experience-слой)
description: Как подключить любой фреймворк или собственный пайплайн к петле опыта — события, извлечение знаний, подсказки.
---

# Внешние агенты: Temporality как experience-слой

Temporality — не только харнес. Журнал опыта доступен по HTTP, поэтому
петлю «события → знания → подсказки» можно включить для агента, который
работает в любом фреймворке — LangGraph, LangChain, собственный
пайплайн — без переноса исполнения в kernel.

## Контракт из трёх шагов

1. **Репортьте события рана** в журнал: `POST /v1/observations/events`
   (batch до 100 событий), схема `temporality.event/1`.
2. **Дождитесь терминального события** (`run.completed` / `run.failed` /
   `run.cancelled`) и вызовите извлечение: `POST /v1/agent/runs/{run}/extract?project=…`
   (это kernel-API; идемпотентно — повторный вызов вернёт 409).
3. **Спрашивайте подсказки** перед новыми задачами:
   `POST /v1/observations/hints` с текстом задачи — вернутся релевантные
   знания проекта с состоянием и предостережениями.

Минимальный набор событий, который понимает извлечение: `run.started`,
`turn.started`, `model.started`, `model.completed` (полезны
`output_tokens`, `content`), `tool.started` / `tool.completed` /
`tool.failed` (с `tool` и результатом), `run.completed`. Всё
неизвестное просто игнорируется — репортьте столько, сколько считаете
нужным.

## Аутентификация

Журнал принимает bearer-токены пользователей workspace (те же, что и
kernel). Для сервисной интеграции создайте отдельного пользователя с
ролью writer на нужный проект.

## Готовый пример

[`examples/external-agent/external_agent.py`](https://github.com/RuslanGabbasov/temporality/blob/main/examples/external-agent/external_agent.py) —
~100 строк на Python без SDK: мини-агент на OpenAI-совместимом API,
который репортит события, вызывает извлечение и запрашивает подсказки.

```sh
pip install requests
export TEMPORALITY_URL=http://localhost:8080
export TEMPORALITY_TOKEN=<journal bearer>
export KERNEL_URL=http://localhost:8090
export MODEL_BASE_URL=https://api.openai.com/v1
export MODEL_API_KEY=sk-…
export MODEL_NAME=gpt-4o-mini
python3 external_agent.py my-project "Настрой CI для monorepo"
```

Первый прогон — выполняет задачу и извлекает знания. Второй прогон с
похожей задачей — уже получает подсказки из накопленного опыта.

## Что доступно внешнему агенту

| Возможность | Эндпоинт |
|---|---|
| Репорт событий рана | `POST /v1/observations/events` |
| Извлечение знаний (kickoff) | `POST /v1/agent/runs/{run}/extract` |
| Подсказки перед задачей | `POST /v1/observations/hints` |
| Листинг и досье знаний | `GET /v1/observations/knowledge` |
| Исправить/оспорить/повысить знание | `POST …/knowledge/{invalidate,challenge,promote}` |
| Семантические соседи знания | `GET /v1/observations/knowledge/embeddings/neighbors` |
| Трейс и сравнение ранов | `GET /v1/observations/runs/{state,compare}` |

Ограничение: durable-исполнение, HITL, песочницы и делегирование — часть
kernel-харнеса. Если агенту нужны они — переносите исполнение в kernel
(см. «Быстрый старт»); опыт при этом не теряется: события тех же типов.

## Как это выглядит в интерфейсе

Раны внешнего агента появляются в тех же списках и таймлайне (проект +
run id связывают события), извлечённые знания — в исследователе знаний с
происхождением до исходного рана, конфликты — в очереди супервайзера.
