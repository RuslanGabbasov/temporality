# Temporality

**Русский** · [English](README.en.md) · [Документация](https://ruslangabbasov.github.io/temporality/docs/) · [Эксплуатация харнеса](https://ruslangabbasov.github.io/temporality/docs/operations.html)

Temporality — среда исполнения и контур управления для ИИ-агентов: долговременные
задачи на Temporal, полная трасса работы, права и подтверждения — и накопление
опыта, который возвращается в следующие запуски.

<p align="center">
  <a href="https://ruslangabbasov.github.io/temporality/assets/knowledge-expantions.mp4">
    <video src="pages/assets/knowledge-expantions.mp4" autoplay muted loop playsinline width="100%"></video>
  </a>
  <em>Карта знаний: база знаний выкладывается на карту по смысловому сходству — грузится на глазах, кластеры собираются сами.</em>
</p>

```text
Agent Kernel ──events──▶ Journal ──projection──▶ Knowledge / Hints
     ▲                      │                         │
     └──────── hints ◀──────┴──── Experience Timeline ┘
```

## Быстрый стенд

Нужен только Docker (BuildKit). Никаких .env и ключей на старте — модель,
первый агент и песочница настраиваются в мастере онбординга при первом
открытии интерфейса.

```sh
mkdir temporality && cd temporality
curl -o docker-compose.yml https://raw.githubusercontent.com/RuslanGabbasov/temporality/main/docker-compose.yml
docker compose up -d
```

Откройте `http://localhost:3000` — мастер проведёт через: первый проект →
администратор → провайдер моделей (ключ + автоподгрузка каталога) → профиль
песочницы (Node / Python / Browser E2E, сборка в один клик) → первый агент
из шаблона → модель-супервайзер. Дальше — задача в чат.

Полный гайд (включая прод-заметки вроде `KERNEL_SECRET_MASTER_KEY`):
[Быстрый старт](https://ruslangabbasov.github.io/temporality/docs/quickstart.html).

## Стенд для разработки

```sh
git clone https://github.com/RuslanGabbasov/temporality.git && cd temporality
docker compose -f docker-compose.yml -f docker-compose.dev.yml up --build -d
```

Dev-overlay собирает образы из исходников; миграции применяются ядром на
старте. Dev-сервер debugger с живой перезагрузкой:

```sh
npm --prefix debugger install
npm --prefix debugger run dev     # проксирует /api → :8080 и /kernel-api → :8090
```

Полезные цели:

```sh
make fmt            # gofmt
make test           # go test ./...
make race           # go test -race ./...
make build          # bin/temporality-journal + bin/agent-kernel
make debugger-test  # витесты фронтенда
make debugger-build # сборка статики UI
```

## Состав

| Компонент | Путь | Назначение |
|---|---|---|
| Journal | `observation/`, `cmd/temporality-journal` | append-only события, проекция знаний, подбор подсказок |
| Agent Kernel | `kernel/`, `cmd/agent-kernel` | harness агента на Temporal: модельный шлюз, MCP, песочница, подтверждения, делегирование |
| Workspace UI | `debugger/` | чат с агентом, трассы, карта знаний, настройки |

Подробная карта интерфейсов и внутренностей — в
[документации](https://ruslangabbasov.github.io/temporality/docs/) и
[`docs/architecture.md`](docs/architecture.md); схемы событий и контрактов —
в [`schemas/`](schemas/).

## Лицензия

[Apache License 2.0](LICENSE).
