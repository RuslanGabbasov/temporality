#!/bin/sh
# Temporality one-line installer.
#
#   curl -fsSL https://raw.githubusercontent.com/RuslanGabbasov/temporality/main/install.sh | sh
#
# The script checks Docker + Compose, fetches the compose file (both required
# configs live inside it), starts the stack and waits until it is healthy.
#
# Subcommands:
#   install (default)  fetch the compose file and start the stack
#   update             refetch the compose file, pull newer images, restart
#   doctor             diagnose a broken install (docker, compose, ports, health)
#   uninstall          stop the stack (add --purge to also delete ALL data)
#
# Flags: --dir <path> (default ./temporality), --version <tag> (pin images),
#        --purge (uninstall: delete volumes), -h/--help.
# The stack itself needs no .env: the model, the first agent and the sandbox
# are configured in the onboarding wizard on first open of http://localhost:3000.
set -eu

RAW_BASE="https://raw.githubusercontent.com/RuslanGabbasov/temporality/main"
COMPOSE_URL="$RAW_BASE/docker-compose.yml"
DEFAULT_DIR="./temporality"
# configs: content: (inline configs) needs Docker Compose v2.23.3+ (Dec 2023).
MIN_MAJOR=2; MIN_MINOR=23; MIN_PATCH=3
UI_PORT=3000

say() { printf '%s\n' "$*"; }
ok() { printf '✓ %s\n' "$*"; }
warn() { printf '  %s\n' "$*" >&2; }
die() { printf '✗ %s\n' "$*" >&2; exit 1; }
need_cmd() { command -v "$1" >/dev/null 2>&1; }

fetch() { # fetch <url> <output-file>
  if need_cmd curl; then curl -fsSL "$1" -o "$2"
  elif need_cmd wget; then wget -qO "$2" "$1"
  else die "Need curl or wget to download the compose file."
  fi
}

check_docker() {
  need_cmd docker || die "Docker is not installed — get it at https://docs.docker.com/get-docker/ and re-run."
  if docker info >/dev/null 2>&1; then ok "Docker is up"; return; fi
  err=$(docker info 2>&1 | head -n 3 || true)
  # Rootless installs keep the socket under the user's runtime dir; a plain
  # non-interactive shell does not know it. Try it before failing.
  rdir=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}
  if [ -S "$rdir/docker.sock" ]; then
    DOCKER_HOST="unix://$rdir/docker.sock"
    export DOCKER_HOST
    if docker info >/dev/null 2>&1; then ok "Docker (rootless socket) is up"; return; fi
  fi
  say "" >&2
  case $err in
    *ermission*denied*|*ot\ in\ the\ docker\ group*)
      say "The Docker socket is not accessible for this user." >&2
      if [ "$(id -u)" != "0" ]; then
        warn "Two ways to fix:"
        warn "• add yourself to the docker group:  sudo usermod -aG docker $USER  (then log out and back in)"
        warn "• or run the installer as root:      curl -fsSL $RAW_BASE/install.sh | sudo sh"
        warn "Note: «sudo curl … | sh» runs the script WITHOUT root — sudo applies to curl only."
      fi
      die "Docker socket: permission denied."
      ;;
    *Cannot\ connect*|*daemon*running*|*refused*)
      die "The Docker daemon is not running — start it (sudo systemctl start docker, or Docker Desktop) and re-run."
      ;;
    *)
      die "Docker is installed but not reachable: $(printf '%s' "$err" | tr '\n' ' ' | cut -c1-160)"
      ;;
  esac
}

# docker compose version --short prints "2.29.7" (older print "v2.24.0").
compose_at_least() {
  raw=$(docker compose version --short 2>/dev/null || true)
  [ -n "$raw" ] || return 1
  raw=${raw#v}
  MAJOR=${raw%%.*}; rest=${raw#*.}
  MINOR=${rest%%.*}; PATCH=${rest#*.}
  PATCH=${PATCH%%-*} # pre-release suffix ("7-beta.1") is not an integer
  [ "$MAJOR" -gt "$1" ] && return 0
  [ "$MAJOR" -lt "$1" ] && return 1
  [ "$MINOR" -gt "$2" ] && return 0
  [ "$MINOR" -lt "$2" ] && return 1
  [ "$PATCH" -ge "$3" ] && return 0
  return 1
}

check_compose() {
  need_cmd docker || die "Docker is not installed."
  docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is missing — install the compose plugin (https://docs.docker.com/compose/install/)."
  if compose_at_least "$MIN_MAJOR" "$MIN_MINOR" "$MIN_PATCH"; then
    ok "Docker Compose $(docker compose version --short)"
  else
    die "Docker Compose $(docker compose version --short 2>/dev/null) is too old — the compose file uses inline configs (needs v$MIN_MAJOR.$MIN_MINOR.$MIN_PATCH+, Dec 2023). Upgrade Docker Desktop / the compose plugin, then re-run."
  fi
}

find_dir() { # resolve the directory holding the compose file
  if [ -n "$DIR" ]; then printf '%s\n' "$DIR"; return; fi
  if [ -f docker-compose.yml ]; then printf '.\n'; return; fi
  if [ -f "$DEFAULT_DIR/docker-compose.yml" ]; then printf '%s\n' "$DEFAULT_DIR"; return; fi
  printf '%s\n' "$DEFAULT_DIR"
}

pin_version() { # write TEMPORALITY_VERSION into .env next to the compose file
  [ -n "$VERSION_PIN" ] || return 0
  if grep -q "^TEMPORALITY_VERSION=" .env 2>/dev/null; then
    sed "s/^TEMPORALITY_VERSION=.*/TEMPORALITY_VERSION=$VERSION_PIN/" .env > .env.tmp && mv .env.tmp .env
  else
    printf 'TEMPORALITY_VERSION=%s\n' "$VERSION_PIN" >> .env
  fi
  ok "Images pinned to $VERSION_PIN (.env)"
}

wait_stack() { # up --wait and translate the failure into next steps
  say "Starting the stack (first pull can take a few minutes)…"
  if ! docker compose up -d --wait --wait-timeout 300; then
    say ""
    say "The stack did not become healthy in time. Look at the failing container:"
    say "  cd $(pwd) && docker compose ps && docker compose logs --tail 30"
    say "Common causes: a busy port (3000/8080/8090/8233), not enough disk or memory."
    exit 1
  fi
}

do_install() {
  check_docker
  check_compose
  # Same directory as a previous install (or an existing compose file):
  # reinstall/update in place instead of nesting temporality/temporality.
  DIR=$(find_dir)
  [ -d "$DIR" ] || mkdir -p "$DIR"
  cd "$DIR"
  say "Fetching the compose file…"
  fetch "$COMPOSE_URL" docker-compose.yml
  pin_version
  wait_stack
  say ""
  ok "Temporality is running → http://localhost:$UI_PORT"
  say "Open the URL — the onboarding wizard walks the first setup:"
  say "project → administrator → model provider → sandbox → first agent."
  say ""
  say "Update later:   curl -fsSL $RAW_BASE/install.sh | sh -s -- update"
  say "Diagnostics:    curl -fsSL $RAW_BASE/install.sh | sh -s -- doctor"
  say "Uninstall:      curl -fsSL $RAW_BASE/install.sh | sh -s -- uninstall"
}

do_update() {
  check_docker
  DIR=$(find_dir)
  [ -f "$DIR/docker-compose.yml" ] || die "No docker-compose.yml in $DIR — run the installer without arguments first."
  cd "$DIR"
  say "Fetching the latest compose file…"
  fetch "$COMPOSE_URL" docker-compose.yml
  say "Pulling newer images…"
  docker compose pull
  wait_stack
  ok "Update complete → http://localhost:$UI_PORT"
}

do_uninstall() {
  DIR=$(find_dir)
  [ -f "$DIR/docker-compose.yml" ] || die "No docker-compose.yml in $DIR — nothing to uninstall."
  cd "$DIR"
  if [ "$PURGE" -eq 1 ]; then
    # The script usually arrives through `curl | sh` — stdin is the SCRIPT
    # stream, so the answer must come from the terminal.
    printf 'This deletes ALL Temporality data (projects, runs, knowledge — the postgres volume). Type YES to continue: '
    read -r answer < /dev/tty 2>/dev/null || die "No terminal to confirm — download the script and run it locally, or drop the stack without --purge."
    [ "$answer" = "YES" ] || die "Aborted — nothing was deleted."
    docker compose down -v --rmi all
    ok "Stack removed. The sandbox files stay in /srv/temporality/sandbox (rm -rf it yourself if you want a clean slate)."
  else
    docker compose down
    ok "Stack stopped and containers removed. Data volumes kept — re-run with --purge to delete them too."
  fi
}

do_doctor() {
  check_docker
  if compose_at_least "$MIN_MAJOR" "$MIN_MINOR" "$MIN_PATCH"; then
    ok "Docker Compose $(docker compose version --short)"
  else
    say "✗ Docker Compose is older than v$MIN_MAJOR.$MIN_MINOR.$MIN_PATCH — the compose file needs inline configs."
  fi
  DIR=$(find_dir)
  if [ -f "$DIR/docker-compose.yml" ]; then
    cd "$DIR"
    say ""
    say "Containers (${DIR}):"
    docker compose ps
  else
    say ""
    say "No install found in $DIR (no docker-compose.yml)."
  fi
  say ""
  say "Probing endpoints (only respond once the stack is up):"
  for probe in "UI http://localhost:$UI_PORT" "journal http://localhost:8080/healthz" "kernel http://localhost:8090/livez"; do
    name=${probe%% *}; url=${probe#* }
    if need_cmd curl && curl -fsS -o /dev/null --max-time 3 "$url" 2>/dev/null; then
      ok "$name answers at $url"
    else
      say "✗ $name does not answer at $url"
    fi
  done
  say ""
  say "Disk space:"; df -h . | tail -n +1
}

usage() {
  cat <<EOF
Temporality installer.

  curl -fsSL $RAW_BASE/install.sh | sh                 install (default)
  curl -fsSL $RAW_BASE/install.sh | sh -s -- update     pull newer images and restart
  curl -fsSL $RAW_BASE/install.sh | sh -s -- doctor     diagnose an install
  curl -fsSL $RAW_BASE/install.sh | sh -s -- uninstall  stop the stack (keep data)

Flags: --dir <path> (default $DEFAULT_DIR), --version <tag> (pin images),
       --purge (uninstall: also delete volumes), -h/--help.
EOF
}

cmd=install; DIR=""; VERSION_PIN=""; PURGE=0
while [ $# -gt 0 ]; do
  case $1 in
    install|update|doctor|uninstall) cmd=$1 ;;
    --dir) [ $# -ge 2 ] || die "--dir needs a value"; DIR=$2; shift ;;
    --version) [ $# -ge 2 ] || die "--version needs a tag"; VERSION_PIN=$2; shift ;;
    --purge) PURGE=1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "Unknown argument: $1 (see --help)" ;;
  esac
  shift
done

case $cmd in
  install) do_install ;;
  update) do_update ;;
  doctor) do_doctor ;;
  uninstall) do_uninstall ;;
esac
