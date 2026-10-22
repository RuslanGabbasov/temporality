# Декомпозиция: embeddings-load-lifecycle

Сага: saga-embeddings-7c1709. Спецификация: `spec.md` (R1–R6, дефекты D1–D5).

| #  | Подзадача                      | Суб-агент | Зависит от        | Критерии готовности |
|----|--------------------------------|-----------|-------------------|---------------------|
| T1 | Ремонт жизненного цикла загрузки | Developer | —                 | R1–R6: diff только в `kernel/agent/embeddings.go`, `cmd/agent-kernel/embeddingsbackfill.go`, точечно `cmd/agent-kernel/main.go`; `go build ./...`, `go vet ./...`, `go test ./kernel/agent/ ./cmd/agent-kernel/`, `go test -race ./kernel/agent/` — чисто; новые тесты через инъекцию загрузчика; работа в git worktree `/workspace/wt-embeddings-load-lifecycle` (ветка `fix/embeddings-load-lifecycle` от 223eb0a); изменения закоммичены в ветку; отчёт с SHA коммитов, списком изменённых файлов и выводом тестов |
| T2 | Репро-проба rembed.Load в песочнице | Developer | —                 | Вне репозитория (/scratch) запущена минимальная программа с `rembed.Load("intfloat/multilingual-e5-small")`, зафиксированы: текст ошибки либо факт успешной/долгой загрузки, длительность, поведение кэша; результат передан в отчёт; в репозиторий ничего не закоммичено |
| T3 | Верификация (сборка, тесты, гонки) | QA        | T1                | Независимо пересобрано и прогонены `go build ./...`, `go vet ./...`, `go test ./kernel/agent/ ./cmd/agent-kernel/`, `go test -race ./kernel/agent/`; подтверждено соответствие R1–R6 по diff'у; список расхождений (если есть) с точностью до файла/строки |
| T4 | Quality-gate ревью               | Reviewer  | T1 (review_of T1) | Аудит диффа на регрессии, корректность машины состояний (нет гонок/дедлоков), сохранность контракта R6, чистоту scope; вердикт approve/rework с конкретными пунктами |
| T5 | Публикация PR                    | Lead      | T3, T4            | Ветка `fix/embeddings-load-lifecycle` запушена в origin; открыт PR в `RuslanGabbasov/temporality` с описанием по мотивам spec.md; ссылка на PR в отчёте; основная рабочая копия `/workspace/temporality` не изменена |

Порядок этапов: T1 и T2 параллельно → T3 → T4 (гейт) → T5 (PR). Задача не объявляется
завершённой, пока T4 не вынес вердикт approve, T3 не подтвердил критерии и T5 не
создал PR. При вердикте rework T1 возвращается на доработку.

Промпты суб-агентов: `prompts/t1-developer-fix.md`, `prompts/t2-developer-probe.md`,
`prompts/t3-qa-verify.md`, `prompts/t4-reviewer-gate.md`.
