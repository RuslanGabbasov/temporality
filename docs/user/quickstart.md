---
order: 2
section: Начало
title: Быстрый старт
description: От нуля до первой задачи агента — установка, онбординг, первый запуск.
---

# Быстрый старт

## Требования

- Docker (Linux, macOS; Windows — через WSL2). Рекомендуется BuildKit.
- ~4 ГБ свободной памяти для стека (PostgreSQL, Temporal, Journal, Kernel, UI).
- Ключ любого OpenAI-совместимого провайдера (OpenAI, OpenRouter, локальный Ollama и т.д.). Anthropic подключается напрямую: base URL `https://api.anthropic.com/v1`, модель — например `claude-sonnet-4-5` (совместимый слой Anthropic поддерживает инструменты и стриминг).

## Запуск

Готовые образы из GHCR — рекомендуемый путь, ничего клонировать и собирать
не нужно:

```sh
mkdir temporality && cd temporality
curl -o docker-compose.yml https://raw.githubusercontent.com/RuslanGabbasov/temporality/main/docker-compose.yml
curl -o .env.example https://raw.githubusercontent.com/RuslanGabbasov/temporality/main/.env.example
cp .env.example .env
# отредактируйте .env: TEMPORALITY_MODEL_BASE_URL / _ID / _API_KEY
docker compose up -d
```

По умолчанию поднимается последний опубликованный релиз (образы — из
[релизов](https://github.com/RuslanGabbasov/temporality/releases)). Для
контролируемого обновления версию можно закрепить:
`TEMPORALITY_VERSION=2026.2.113 docker compose up -d`. Обновление до новой
версии: `docker compose pull && docker compose up -d` (или новая закреплённая
версия) — миграции применяются ядром на старте.

Сборка из исходников (для разработки самого Temporality):

```sh
git clone https://github.com/RuslanGabbasov/temporality.git && cd temporality
cp .env.example .env
docker compose -f docker-compose.yml -f docker-compose.dev.yml up --build -d
```

Поднимаются: PostgreSQL, Journal (`:8080`), UI (`:3000`), Temporal (UI `:8233`),
Agent Kernel (`:8090`), SearXNG (`127.0.0.1:8889`, метапоиск для агента) и
headless-браузер Lightpanda. Пара `TEMPORALITY_MODEL_BASE_URL` + `_ID` —
обязательный минимум; без них ядро не стартует.

Примеры конфигурации провайдера:

```dotenv
# OpenAI
TEMPORALITY_MODEL_BASE_URL=https://api.openai.com/v1
TEMPORALITY_MODEL_ID=gpt-4.1-mini
TEMPORALITY_MODEL_API_KEY=sk-...

# Локальный Ollama
TEMPORALITY_MODEL_BASE_URL=http://host.docker.internal:11434/v1
TEMPORALITY_MODEL_ID=llama3.2
TEMPORALITY_API_KEY=
```

Остановить стек, сохранив данные: `make stack-stop`.

## Онбординг

Откройте `http://localhost:3000`. При первом входе мастер онбординга проведёт
через обязательные шаги и спрячет навигацию, пока они не пройдены:

1. **Первый проект** — рабочая область для задач агента.
2. **Администратор** — ваш пользователь.
3. **Провайдер модели** — base URL, ключ (сохраняется в зашифрованные секреты), автополучение списка моделей.
4. **Агент** — из готового шаблона или по описанию.
5. **Сборка песочницы** — образ, в котором будут выполняться команды агента (нужен доступ к Docker daemon).

Два случая, когда мастер ведёт себя иначе:

- задан `KERNEL_AUTH_TOKENS` — аутентификация включена вашим же env: мастер
  не стартует анонимно, войдите одним из этих токенов (режим «API-токен»)
  или закомментируйте переменную для анонимного онбординга;
- задан `KERNEL_SANDBOX_IMAGE` — образ песочницы закреплён вашим env:
  шаг сборки предлагается пропустить (пересобрать — `sandbox/build.sh`).

## Первая задача

Откройте **Workspace** — чат с агентом — и напишите задачу. Агент ответит
стримингом; по ходу работы он может:

- выполнить команды в песочнице (`/workspace` проекта — персистентное хранилище),
- делегировать подзадачи другим агентам (`plan`/`delegate`),
- задать вам вопрос через **подтверждение** (карточка в чате, дублируется в «Запусках»),
- записать полезное наблюдение в знания проекта.

После завершения загляните в **Запуски** (трасса шаг за шагом) и **Знания**
(что агент вынес из задачи).

## Что настроить дальше

- **Каналы** — чтобы агент получал задачи из Matrix/Telegram/Slack и задавал вопросы туда, где вам удобно: [Входящие и каналы](inbound.html).
- **Триггеры** — задачи по расписанию или событию: [Триггеры](triggers.html).
- **Навыки** — закрепить повторяющуюся процедуру: [Навыки](skills.html).
- **Пользователи и доступы** — команда и RBAC: [Администрирование](administration.html).
