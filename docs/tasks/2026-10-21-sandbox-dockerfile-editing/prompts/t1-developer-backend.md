# Промпт T1 — Developer (backend)

Задача: реализовать backend возможности полного редактирования Dockerfile в
конструкторе песочниц (режим «operator recipe»).

Входы (прочитай обязательно):
- `docs/tasks/2026-10-21-sandbox-dockerfile-editing/spec.md` — контракт: §3
  (решения D1–D5), §4 (данные/API), §5 (AC-1…AC-7, AC-9, AC-10).
- Ключевые файлы: `workspace/sandboxprofiles.go` (модель+валидация),
  `kernel/sandbox/build.go` (CustomDefinition/BuildDefinition, stdin-only
  docker build без build context), `cmd/agent-kernel/sandboxprofiles.go`
  (PUT/POST preview/build), `kernel/agent/sandboxbuilder.go` (мастер-промпт),
  `migrations/workspace/0001_baseline_v2026.2.21.up.sql`.

Что сделать (в точности по spec.md):
1. Миграция `migrations/workspace/0002_*`: колонка `dockerfile_override
   TEXT NOT NULL DEFAULT ''` в `workspace_sandbox_profile` (следуй принятой в
   каталоге схеме именования/применения миграций).
2. `SandboxProfile.DockerfileOverride`, инварианты D2.1–D2.4 в
   `ValidateSandboxProfile`/store (сброс `image` при правке рецепта, запреты
   для builtin и для смешивания источников рецепта).
3. `kernel/sandbox.OverrideDefinition(profileID, dockerfile)` (строгая
   авторитетная валидация D3: FROM обязателен, multi-stage разрешён, запреты
   ADD / COPY без --from / ONBUILD с номером строки в ошибке, ≤16 KiB,
   UTF-8 без NUL) + использование override в пути сборки.
4. Маппинг ошибок D4 в `BuildDefinition`: отказы apk/apt о несуществующем
   пакете → ошибка, называющая пакет и подсказку apt↔apk, поверх сырого лога.
5. API: PUT/POST принимают поле; `POST …/preview` — опциональный
   `dockerfile_override` (вернуть валидированный текст или 422 с ошибкой
   построчно); `POST …/build` строит из override verbatim; аудит/события как
   у существующих обновлений профиля.
6. Мастер (`sandboxDraftPrompt`): ужесточение D5; в контракте `SandboxDraft`
   поля рецепта нет и не появляется.
7. Unit-тесты под AC-1…AC-7 (валидация, store-инвалидация, OverrideDefinition,
   маппинг ошибок, мастер-контракт) и обновление `docs/sandbox-profiles.md`
   (AC-10, уточнение принципа §4 по D1).

Критерии готовности: `go test ./...` и `go vet ./...` зелёные; AC-1…AC-7, AC-10
закрыты тестами; регрессий нет. Ограничения: не трогать незакоммиченные правки
`debugger/src/{Artifacts,Dashboard,ExperienceTimeline}.tsx` и локали (смежная
работа); не менять контур изоляции песочницы; молчаливые подмены запрещены —
любое несоответствие контракта = явная ошибка. В отчёте перечисли: что сделано,
какими тестами подтверждено, что осталось.
