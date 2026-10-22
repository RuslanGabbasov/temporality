# Итерация 2 (доработка после T1–T4): промпты суб-агентов

Контекст итерации: backend реализован (t1, но НЕ верифицирован — go test/vet
не запускались после финальных правок); frontend не сделан (сбой провайдера);
аудит t3 дал design-level вердикт «требует исправлений» (F-02…F-06), но его
проверка «реализация отсутствует» ложна (проверено Lead: изменения в дереве
есть). В spec.md добавлен раздел D7 (обязательные ужесточения).

## t5-backend-verify (developer)

Проверь и доведи backend возможности «operator recipe» (полное редактирование
Dockerfile профиля песочницы) в /workspace/temporality. Спецификация:
docs/tasks/2026-10-21-sandbox-dockerfile-editing/spec.md (§3 D1–D5, D7; §4;
AC-1…AC-7, AC-9, AC-10). Предыдущая работа уже в рабочем дереве (см. git
status: kernel/sandbox/override.go, migrations/workspace/0002_*.sql,
workspace/sandboxprofiles.go, cmd/agent-kernel/sandboxprofiles.go,
kernel/agent/sandboxbuilder.go и тесты) — не переписывать с нуля, а верифицировать
и достроить.

Что сделать:
1. Прогнать go build ./..., go vet ./..., go test ./... — добиться зелёного,
   исправить всё, что сломано (предыдущий исполнитель не успел проверить).
2. Реализовать D7 (spec.md): валидатор override отклоняет parser-директивы
   `# syntax=` / `# escape=` и `RUN … --network=host` (ошибка с номером строки);
   verifyPinned переопределяет entrypoint (`--entrypoint`), чтобы конфигурация
   образа не исполнялась на проверочном шаге. Зеркало валидации
   (workspace) — синхронизировать. Тесты на каждое правило.
3. Проверить инварианты D2 по коду (XOR источников рецепта, сброс digest,
   builtin, модельный путь) и закрыть пробелы тестами, если их нет.
4. Обновить docs/sandbox-profiles.md (D1/D7), если не хватает.
Ограничения: не трогать debugger/src (задача t6) и незакоммиченные правки
смежной работы (Artifacts.tsx, Dashboard.tsx, ExperienceTimeline.tsx,
debugger/docs/, timelineLoad.*). В отчёте: выводы go build/vet/test,
перечень правок D7, что подтверждено тестами.

## t6-frontend (developer)

Промпт итерации 1 (docs/tasks/2026-10-21-sandbox-dockerfile-editing/prompts/
t2-developer-frontend.md) — выполнить в полном объёме: переключатель
«Собирать из базы / Свой Dockerfile», textarea с предзаполненным
композированным Dockerfile, отправка dockerfile_override при base/packages
пустыми, живая валидация через preview-endpoint с показом ошибок построчно,
локали ru/en, vitest-тесты + зелёный npm run build. Контракт API — spec.md §4.
Ограничения: не трогать Go-файлы (задача t5) и незакоммиченные правки
смежной работы (Artifacts.tsx, Dashboard.tsx, ExperienceTimeline.tsx,
debugger/docs/, timelineLoad.* — их не откатывать, локали дополнять). В отчёте:
выводы npm test и npm run build.

## t7-security (security-auditor)

Ре-аудит реализованного кода (дифф против 223eb0a в /workspace/temporality):
возможность «оператор редактирует полный Dockerfile» (spec.md, I-3, D2.2,
D3, D7). Твоя предыдущая проверка дерево видела до записи изменений — сейчас
реализация на месте. Проверь: закрыты ли F-02…F-06 (parser-директивы,
--network=host, entrypoint при верификации, класс доверия writer+, модельный
путь), обходы валидатора (регистр, `\`-продолжения, heredoc, пробелы),
инъекции в логи/ответы D4. Полное заключение (вердикт по I-3 + находки:
id, критичность, вектор, где в коде, рекомендация) СОХРАНИ файлом
docs/tasks/2026-10-21-sandbox-dockerfile-editing/security-audit.md. Код не
изменяй.

## t8-review (reviewer, quality-gate)

Итерация 2 приёмки: t5-backend-verify и t6-frontend по spec.md §5 (AC-1…AC-11).
Прогнать go test ./..., go vet ./..., в debugger/ vitest и npm run build;
проверить каждый AC по коду и тестам; учесть заключение
docs/tasks/2026-10-21-sandbox-dockerfile-editing/security-audit.md (AC-11:
critical/high закрыты или явно признаны). Фокус: D2-инварианты, D3/D7-валидатор
(обходы), отсутствие молчаливых подмен, качество тестов. Вердикт approve/rework
с перечнем дефектов. Заверения без выводов команд не принимаются.
