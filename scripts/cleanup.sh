#!/bin/bash
# List projects
docker compose exec -T postgres psql -U temporality -d temporality -t -A -c "SELECT id, name FROM workspace_project ORDER BY created_at;" 2>&1