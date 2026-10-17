# Contributing

## Версионирование

Календарная схема: **`YYYY.MINOR.PATCH`** (например, `2026.1.0`).

- `MINOR` — новые функции в течение года (`2026.1.0` → `2026.2.0`);
- `PATCH` — исправления (`2026.1.0` → `2026.1.1`);
- тег формата `v2026.1.0` запускает релизный pipeline.

## Процесс разработки

1. Ветка от `main`: `feat/…`, `fix/…`, `docs/…`.
2. Коммиты — conventional style (`feat:`, `fix:`, `docs:`, `chore:`) — из них
   собирается changelog релиза.
3. **Pull request в `main`** — CI должен быть зелёным (Go build/vet/test,
   debugger tsc/test/build, сборка docs-сайта).
4. Merge в `main` — squash либо merge-коммит; прямые пуши в `main` закрыты
   (branch protection).

## Релиз

```sh
git tag v2026.1.0
git push origin v2026.1.0
```

GitHub Actions соберёт образы `agent-kernel`, `journal`, `debugger`, опубликует
их в GHCR (`ghcr.io/ruslangabbasov/temporality/*:<версия>`) и создаст
GitHub Release с changelog из коммитов.

## Локальная проверка перед PR

```sh
go build ./... && go vet ./... && go test ./...
cd debugger && npm ci && npx tsc -b && npm test && npm run build
cd .. && npm ci && npm run build:docs   # docs-сайт: результат коммитится
```
