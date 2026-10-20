#!/bin/sh
# Entrypoint for the agent-kernel container.
#
# The kernel shells out to the Docker CLI against the host daemon socket, and
# socket group ownership differs by host: Docker Desktop / OrbStack proxy it
# as root:root, a plain Linux host mounts it as root:docker (host-specific
# GID). Compose therefore starts the container as root so this script can
# pick the socket's real group up at runtime and add it to the process before
# dropping privileges — no per-host GID configuration. TEMPORALITY_DOCKER_GID
# overrides the detection for exotic setups (the socket is then expected to
# be group-accessible by that GID).
set -e

TARGET_UID="${TEMPORALITY_UID:-10001}"
TARGET_GID="${TEMPORALITY_GID:-10001}"

if [ "$(id -u)" = "0" ]; then
  socket_gid="${TEMPORALITY_DOCKER_GID:-}"
  if [ -z "$socket_gid" ] && [ -S /var/run/docker.sock ]; then
    socket_gid="$(stat -c %g /var/run/docker.sock 2>/dev/null || echo 0)"
  fi
  [ -z "$socket_gid" ] && socket_gid=0
  # The sandbox root lives on a host bind mount; on a fresh install docker
  # auto-creates that path root:root, and after the privilege drop the kernel
  # (uid 10001) cannot mkdir inside it — every project start died with
  # «create project workspace: permission denied». While still root: create
  # the directory tree and hand it to the unprivileged user.
  SANDBOX_ROOT="${KERNEL_SANDBOX_ROOT:-${TEMPORALITY_SANDBOX_ROOT:-/srv/temporality/sandbox}}"
  if [ -d "$(dirname "$SANDBOX_ROOT")" ] || [ -d "$SANDBOX_ROOT" ]; then
    mkdir -p "$SANDBOX_ROOT/projects" 2>/dev/null || true
    chown -R "$TARGET_UID:$TARGET_GID" "$SANDBOX_ROOT" 2>/dev/null || \
      echo "WARN: could not chown $SANDBOX_ROOT — pre-create it manually and chown to $TARGET_UID:$TARGET_GID"
  fi
  exec setpriv --reuid="$TARGET_UID" --regid="$TARGET_GID" \
    --groups="$socket_gid" --inh-caps=-all --bounding-set=-all \
    -- /usr/local/bin/temporality-agent-kernel "$@"
fi

# Started as a non-root user (e.g. docker run -u or a hardened override):
# run directly — the operator has arranged access to the socket themselves.
exec /usr/local/bin/temporality-agent-kernel "$@"
