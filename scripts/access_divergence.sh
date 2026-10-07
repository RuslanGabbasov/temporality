#!/bin/sh
# Read-only divergence report: old JSONB access lists vs new org model
# (docs/plan-org-structure.md, wave G; docs/org-structure.md §48 stage 4).
#
# Usage: scripts/access_divergence.sh
# Env overrides: POSTGRES_CONTAINER, POSTGRES_USER, POSTGRES_DB
set -eu
cd "$(dirname "$0")/.."

container="${POSTGRES_CONTAINER:-temporality-postgres-1}"
user="${POSTGRES_USER:-temporality}"
db="${POSTGRES_DB:-temporality}"

exec docker exec -i "$container" psql -U "$user" -d "$db" -v ON_ERROR_STOP=1 \
  < scripts/access-divergence.sql
