#!/usr/bin/env python3
"""Print a compact event listing for one run of a project."""
import json
import sys
import urllib.request

project, run = sys.argv[1], sys.argv[2]
url = f"http://localhost:8080/v1/observations/events?project={project}&run={run}&limit=200"
with urllib.request.urlopen(url, timeout=30) as response:
    data = json.load(response)
for event in data["events"]:
    payload = event.get("data", {})
    extra = ""
    if event["type"] == "tool.completed":
        extra = f"exit={payload.get('exit_code', '?')} {payload.get('tool', '')}"
    elif event["type"] == "tool.failed":
        extra = payload.get("error_type", "")
    elif event["type"] == "approval.auto_granted":
        command = payload.get("operation", {}).get("arguments", {}).get("command")
        extra = " ".join(command) if isinstance(command, list) else ""
    elif event["type"] == "model.failed":
        extra = str(payload)[:180]
    elif event["type"] == "knowledge.proposed":
        extra = payload.get("knowledge_id", "")
    print(event["occurred_at"][11:19], event["type"].ljust(22), extra)
