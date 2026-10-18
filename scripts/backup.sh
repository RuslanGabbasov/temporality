#!/bin/sh
# Backup (docs/resilience-and-scale.md §НТ / administration guide): the two
# durable stores of a Temporality install —
#   1. PostgreSQL  (runs, knowledge, tasks, users, chat transcripts, journal)
#   2. sandbox root (project /workspace files written by agents)
# The rembed cache is re-downloadable and skipped.
#
# Usage:   scripts/backup.sh [target-dir]   (default ./backups)
# Keeps the last 14 of each artifact; older ones rotate away.
set -eu

TARGET="${1:-backups}"
TS="$(date +%Y%m%d-%H%M%S)"
KEEP="${BACKUP_KEEP:-14}"

mkdir -p "$TARGET"

echo "== 1/2 PostgreSQL dump =="
if docker compose exec -T postgres pg_dump -U temporality -d temporality | gzip > "$TARGET/db-$TS.sql.gz"; then
  echo "   db-$TS.sql.gz ($(du -h "$TARGET/db-$TS.sql.gz" | cut -f1))"
else
  echo "   FAILED: pg_dump exited nonzero" >&2
  exit 1
fi

echo "== 2/2 sandbox root =="
SANDBOX_ROOT="${TEMPORALITY_SANDBOX_ROOT:-/srv/temporality/sandbox}"
if [ -d "$SANDBOX_ROOT" ]; then
  # .sandbox-work excludes nothing important: the tree IS the data.
  if tar czf "$TARGET/sandbox-$TS.tar.gz" -C "$(dirname "$SANDBOX_ROOT")" "$(basename "$SANDBOX_ROOT")"; then
    echo "   sandbox-$TS.tar.gz ($(du -h "$TARGET/sandbox-$TS.tar.gz" | cut -f1))"
  else
    echo "   FAILED: tar exited nonzero" >&2
    exit 1
  fi
else
  echo "   sandbox root $SANDBOX_ROOT not found — skipped (no files backed up yet)"
fi

echo "== rotation (keeping $KEEP of each) =="
ls -1t "$TARGET"/db-*.sql.gz 2>/dev/null | tail -n +$((KEEP + 1)) | xargs -r rm -v || true
ls -1t "$TARGET"/sandbox-*.tar.gz 2>/dev/null | tail -n +$((KEEP + 1)) | xargs -r rm -v || true
echo "== done: $TARGET =="
