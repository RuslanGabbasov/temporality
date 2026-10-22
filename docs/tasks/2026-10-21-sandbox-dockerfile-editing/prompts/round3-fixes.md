# Промпты суб-агентов, раунд 3 (точечные фиксы после ре-аудита)

Дата: 2026-10-21. Автор: Lead. Основание: `security-audit.md` (подтверждающий
проход, открыты F-04a, F-06, F-08). Эти промпты отправлены суб-агентам как есть;
файл хранит их для трассируемости.

Общий контекст (включён в каждый промпт):

> Репозиторий: `/workspace/temporality` (HEAD 223eb0a; изменения в рабочем
> дереве НЕ коммитить). Возможность полного редактирования Dockerfile в
> конструкторе песочниц: spec `docs/tasks/2026-10-21-sandbox-dockerfile-editing/spec.md`,
> аудит `docs/tasks/2026-10-21-sandbox-dockerfile-editing/security-audit.md`.
> Валидаторы продублированы в двух зеркалах и обязаны совпадать семантически:
> `kernel/sandbox/override.go` (авторитет) и `workspace/sandboxprofiles.go`
> (зеркало на API-границе). Парсер RUN учитывает флаговый сегмент и тело команды
> отдельно (`runFlagViolations` работает по склеенным continuation-строкам,
> токены после ключевого слова инструкции) — менять это разделение нельзя.

## T3a — developer: F-08 + F-04a (строгий разбор `--mount`)

См. plan-задачу `fix-mount`. Владеет файлами: `kernel/sandbox/override.go`,
`kernel/sandbox/override_test.go`, `workspace/sandboxprofiles.go`,
`workspace/sandboxprofiles_override_test.go`.

## T3b — developer: F-06 (санитизация вывода docker)

См. plan-задачу `fix-sanitize`. Владеет файлами: `kernel/sandbox/build.go`,
`kernel/sandbox/build_test.go`, `docs/tasks/.../spec.md` (§2.4).

## T3c — QA: верификация

См. plan-задачу `qa-verify`. Полный `go build`/`go vet`/`go test`, подтверждение
зелёного статуса, фиксация фактических выводов команд.

## T3d — Security Auditor: короткий ре-аудит

См. plan-задачу `reaudit`. Пробный корпус обходов (включая duplicate-key
`--mount`) через `go test -overlay` из `/scratch`, вердикт по AC-11,
отчёт `security-audit-round3.md`.

## T3e — Reviewer: финальный гейт

См. plan-задачу `final-gate`. Ревью диффов по AC-1…AC-11, учёт выводов QA и
аудитора, вердикт «принято / возврат на доработку».
