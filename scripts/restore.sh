#!/bin/sh
# Restore (docs/resilience-and-scale.md §НТ / administration guide): the
# counterpart of scripts/backup.sh. Stops the writers, restores PostgreSQL,
# restores the sandbox root, restarts the stack.
#
# Usage: scripts/restore.sh <db-dump.sql.gz> <sandbox.tar.gz>
# The sandbox root defaults to /srv/temporality/sandbox (override with
# TEMPORALITY_SANDBOX_ROOT to match your .env).
set -eu

DB_DUMP="${1:?usage: restore.sh <db-dump.sql.gz> <sandbox.tar.gz>}"
SANDBOX_TGZ="${2:-}"
SANDBOX_ROOT="${TEMPORALITY_SANDBOX_ROOT:-/srv/temporality/sandbox}"

[ -f "$DB_DUMP" ] || { echo "no such dump: $DB_DUMP"; exit 1; }

echo "== stopping writers (agent-kernel, journal) =="
docker compose stop agent-kernel journal

echo "== restoring PostgreSQL =="
if docker compose exec -T postgres psql -U temporality -d postgres -c \
     "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='temporality' AND pid <> pg_backend_pid()" >/dev/null; then
  :
fi
docker compose exec -T postgres psql -U temporality -d postgres -c "DROP DATABASE IF EXISTS temporality"
docker compose exec -T postgres psql -U temporality -d postgres -c "CREATE DATABASE temporality"
if gunzip -c "$DB_DUMP" | docker compose exec -T postgres psql -U temporality -d temporality; then
  echo "   database restored from $DB_DUMP"
else
  echo "   FAILED: psql restore exited nonzero" >&2
  exit 1
fi

if [ -n "$SANDBOX_TGZ" ]; then
  [ -f "$SANDBOX_TGZ" ] || { echo "no such sandbox archive: $SANDBOX_TGZ"; exit 1; }
  echo "== restoring sandbox root ($SANDBOX_ROOT) =="
  mkdir -p "$SANDBOX_ROOT"
  tar xzf "$SANDBOX_TGZ" -C "$(dirname "$SANDBOX_ROOT")"
  echo "   sandbox root restored"
fi

echo "== starting the stack =="
docker compose up -d
echo "== done. The kernel applies its migrations on start; verify with:"
echo "   curl -s http://localhost:8090/healthz"
