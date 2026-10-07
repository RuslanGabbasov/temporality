# Sandbox security matrix

Матрица изоляции `run_command` (kernel/sandbox/docker.go): каждый слой закреплён
тестами в `kernel/sandbox/docker_test.go`. Песочница — защита от **ошибок и
несанкционированных действий агента**, не от компрометации Docker-демона или
ядра хоста (граница доверия — хост).

| Слой | Механизм | Тест |
|------|----------|------|
| Образ | только digest `@sha256:…`, mutable-теги запрещены; `--pull=never` — образ не может «уехать» под песочницей | `TestSandboxImageMustUseSHA256Digest`, `TestExecuteCapturesCommandAndAlwaysRemovesContainer` |
| Сеть | `--network=none` по умолчанию; `bridge` только при явной возможности (agent.network_access / capability network / run policy) | `TestNetworkDefaultsToNoneAndBridgeIsExplicit` |
| Credentials | в контейнер не передаётся **ничего** из окружения хоста: только `HOME=/scratch`, `TMPDIR=/scratch`; токены ядра и ключи провайдеров физически не попадают в argv | `TestEnvironmentCarriesNoCredentials` |
| Файловая система | корень контейнера `--read-only`; единственный mount — workspace (`:ro` для reviewer/qa и read-only агентов, иначе `:rw`); никаких `--mount`/`-v` кроме workspace | `TestWorkspaceIsTheOnlyVolumeMount`, `TestDockerArgumentsEnforceIsolation` |
| /tmp | tmpfs `rw,noexec,nosuid,size=64m` — временные файлы без исполнения | `TestDockerArgumentsEnforceIsolation` |
| /scratch | tmpfs `rw,exec,nosuid` (по умолчанию 512m, `KERNEL_SANDBOX_SCRATCH_SIZE`) — единственная исполняемая зона для сборки/тестов; `HOME`/`TMPDIR` указывают на неё | `TestDockerArgumentsEnforceIsolation`, `TestScratchSizeMustBeAPositiveByteSize` |
| Privileges | `--cap-drop=ALL`, `--security-opt=no-new-privileges`, без `--privileged` | `TestDockerArgumentsEnforceIsolation` |
| Identity | непривилегированный `--user uid:gid` хоста (владелец файлов workspace) | `TestDockerArgumentsEnforceIsolation` |
| Resources | `--pids-limit`, `--memory`/`--memory-swap` (swap = memory, т.е. без доп. swap), `--cpus`, `--ulimit nofile` | `TestDockerArgumentsEnforceIsolation` |
| Workspace containment | workspace обязан существовать, быть каталогом внутри `KERNEL_SANDBOX_ROOT` (после symlink-резолва); symlink-побег отклоняется; `:`/`,` в пути запрещены (docker-инъекция опций) | `TestWorkspaceMustStayWithinConfiguredRoot` |
| Команда | argv валидируется: непустой, ограничение длины аргументов; вывод ограничен буфером с отметкой обрезки | `TestValidateCommandBoundsArguments`, `TestOutputBufferTruncatesWithoutBlockingWriter` |
| Время | таймаут запроса (по умолчанию/максимум с клампом) через context; контейнер всегда удаляется `rm --force`, включая упавший | `TestExecuteEnforcesRequestTimeout`, `TestExecuteCapturesCommandAndAlwaysRemovesContainer` |

## Известные границы

- Убийство по таймауту действует на прямой дочерний процесс (docker CLI);
  сироты внутри контейнера гаснут вместе с ним благодаря `--init` и `rm --force`.
- `bridge`-сеть — это полный egress хоста; гранулярная фильтрация (allowlist
  доменов) не реализована и фиксируется в ROADMAP.
- Контейнер с `--network=bridge` видит локальную сеть хоста — доверяйте
  `network_access` только проверенным агентам.
- Доступ к Docker-сокету = root на хосте; инсталляция должна держать сокет
  закрытым от агентов (он монтируется только в контейнер kernel).

## Живая проверка сети (2026-10-07, образ из `.env`)

- `--network=none` (профиль по умолчанию): `curl http://example.com` →
  exit 6 (DNS/сеть недоступны) — egress заблокирован.
- `--network=bridge` (network_access выдан): тот же запрос → HTTP 200.

Изоляция `/scratch` между агентами — конструктивная: каждое выполнение команды
  = новый контейнер с собственным tmpfs, который умирает вместе с контейнером
  (флаги закреплены в `TestDockerArgumentsEnforceIsolation`).
