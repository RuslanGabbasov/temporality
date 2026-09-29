#!/bin/bash
PGPASSWORD=temporality psql -h localhost -U temporality -d temporality -c "SELECT id, name FROM workspace_project ORDER BY created_at;"