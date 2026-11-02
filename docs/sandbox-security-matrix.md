# Sandbox security matrix

Матрица изоляции `run_command` (kernel/sandbox/docker.go): каждый слой закреплён
тестами в `kernel/sandbox/docker_test.go`. Песочница — защита от **ошибок и
несанкционированных действий агента**, не от компрометации Docker-демона или
ядра хоста (граница доверия — хост).

| Слой | Механизм | Тест |
|------|----------|------|
| Образ | только digest `@sha256:…`, mutable-теги запрещены; `--pull=never` — образ не может «уехать» под песочницей | `TestSandboxImageMustUseSHA256Digest`, `TestExecuteCapturesCommandAndAlwaysRemovesContainer` |
| Сеть | `--network=none` по умолчанию; `bridge` только при явной возможности (agent.network_access / capability network / run policy) — и он отображается на выделенную сеть `temporality-sandbox` (создаётся идемпотентно при запуске команды), а не на общий docker0: пересборка стека и host-churn не трогают сеть песочниц, контейнеры получают встроенный DNS-форвардер docker | `TestNetworkDefaultsToNoneAndBridgeIsExplicit` |
| Credentials | в контейнер не передаётся **ничего** из окружения хоста: только `HOME=/scratch`, `TMPDIR=/scratch` и переменные кэшей из профиля (`CacheEnv`, пути, не секреты); секреты агента передаются per-command через `docker exec --env` только на исполняющую команду; токены ядра и ключи провайдеров физически не попадают в argv | `TestEnvironmentCarriesNoCredentials` |
| Файловая система | корень контейнера `--read-only`; mount'ы — workspace (`:ro` для reviewer/qa и read-only агентов, иначе `:rw`), per-project кэш `/cache`, run-scoped `/scratch`, лаунчер Landlock `:ro`; опционально docker-сокет (DooD-профиль, явный opt-in) | `TestWorkspaceIsTheOnlyVolumeMount`, `TestProjectCacheMountsPerProjectUnderRoot`, `TestDockerArgumentsEnforceIsolation` |
| /cache | второй mount: `{KERNEL_SANDBOX_ROOT}/cache/{project}` → `/cache:rw` — персистентный кэш тулчейнов (GOCACHE, GOMODCACHE) вне git-воркспейса, чтобы `git clean` и пересоздание проекта не убивали сборочные кэши; остаётся `:rw` даже для read-only воркспейса, потому что не содержит файлов проекта | `TestProjectCacheMountsPerProjectUnderRoot` |
| /tmp | tmpfs `rw,noexec,nosuid` (по умолчанию 256m, `KERNEL_SANDBOX_TMP_SIZE`) — временные файлы без исполнения; 64m не хватало тулчейнам, игнорирующим TMPDIR (линкеры, cgo, упаковщики) | `TestDockerArgumentsEnforceIsolation` |
| /scratch | run-scoped bind-mount `{KERNEL_SANDBOX_ROOT}/scratch/{project}/{run}` → `/scratch:rw` — единственная исполняемая зона для сборки/тестов, живёт между командами одного рана и убирается по TTL; без ScratchKey — tmpfs `rw,exec,nosuid` (по умолчанию 512m, `KERNEL_SANDBOX_SCRATCH_SIZE`) как одноразовый фолбэк; `HOME`/`TMPDIR` указывают на неё | `TestDockerArgumentsEnforceIsolation`, `TestScratchSizeMustBeAPositiveByteSize` |
| Файловая политика (Landlock) | при заданной filesystem-политике каждая команда исполняется через лаунчер `temporality-landlock`, который применяет ruleset (те же read/write/deny области, что и у нативных туллов) до запуска шелла; fail-closed — без ограничений не запускается; `KERNEL_SANDBOX_LANDLOCK=off` — явный лабораторный обход | см. `docs/filesystem-policy-implementation.md` |
| Privileges | `--cap-drop=ALL`, `--security-opt=no-new-privileges`, без `--privileged` | `TestDockerArgumentsEnforceIsolation` |
| Identity | непривилегированный `--user uid:gid` хоста (владелец файлов workspace) | `TestDockerArgumentsEnforceIsolation` |
| Resources | `--pids-limit` (дефолт 512, `KERNEL_SANDBOX_PIDS` — старый хардкод 128 рвал реальную разработку: `go test ./...` плодит десятки процессов-компиляторов), `--memory`/`--memory-swap` (swap = memory, т.е. без доп. swap), `--cpus` (квота клэмпится к числу CPU, видимому docker-демону: дефолт 2, `KERNEL_SANDBOX_CPUS` — иначе на 1-CPU хосте демон отклоняет каждый `docker run`), `--ulimit nofile` | `TestDockerArgumentsEnforceIsolation`, `TestClampCPUsStaysWithinDaemonAvailability`, `TestPidsFromEnvKeepsDefaultOnJunk` |
| Workspace containment | workspace обязан существовать, быть каталогом внутри `KERNEL_SANDBOX_ROOT` (после symlink-резолва); symlink-побег отклоняется; `:`/`,` в пути запрещены (docker-инъекция опций). Нативные файловые инструменты (read_file/write_file/edit_file) идут мимо контейнера и проверяют containment сами — включая symlink-побег и абсолютные пути | `TestWorkspaceMustStayWithinConfiguredRoot`, `TestFileToolsPathsStayInsideWorkspace` |
| Команда | argv валидируется: непустой, до 128 аргументов, аргумент до 32КБ, суммарно до 128КБ (большие файловые полезные нагрузки — через нативный write_file, без шелла); вывод ограничен буфером с отметкой обрезки | `TestValidateCommandBoundsArguments`, `TestOutputBufferTruncatesWithoutBlockingWriter` |
| Время | таймаут запроса (по умолчанию/максимум с клампом) через context; контейнер всегда удаляется `rm --force`, включая упавший | `TestExecuteEnforcesRequestTimeout`, `TestExecuteCapturesCommandAndAlwaysRemovesContainer` |

## Известные границы

- Убийство по таймауту действует на прямой дочерний процесс (docker CLI);
  сироты внутри контейнера гаснут вместе с ним благодаря `--init` и `rm --force`.
- `bridge`-сеть даёт полный egress хоста и остаётся легаси-режимом; гранулярная
  фильтрация реализована отдельно: внутренняя сеть `temporality-sandbox-internal`
  (`--internal`, прямого маршрута наружу нет) + squid-прокси с per-agent
  allowlist (`docs/agent-behavior-monitoring.md` §7).
- Контейнер с `--network=bridge` видит локальную сеть хоста — доверяйте
  `network_access` только проверенным агентам.
- Доступ к Docker-сокету = root на хосте; инсталляция должна держать сокет
  закрытым от агентов (он монтируется только в контейнер kernel).

## Живая проверка сети (2026-10-07, образ из `.env`)

- `--network=none` (профиль по умолчанию): `curl http://example.com` →
  exit 6 (DNS/сеть недоступны) — egress заблокирован.
- `--network=bridge` (network_access выдан): тот же запрос → HTTP 200.

Изоляция `/scratch` между агентами конструктивная, но не «один контейнер на
команду»: команда рана исполняется через `docker exec` в keep-alive контейнере
рана (реестр запущенных контейнеров с fingerprint и idle-reaper), а
run-scoped `/scratch` живёт между командами рана и убирается по TTL; one-shot
`run` + `rm --force` — только фолбэк без ScratchKey (флаги закреплены в
`TestDockerArgumentsEnforceIsolation`).
