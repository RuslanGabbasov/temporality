#!/usr/bin/env python3
"""Export the full event stream of a project into a debugger fixture file."""
import json
import os
import sys
import urllib.request

project = sys.argv[1]
target = sys.argv[2]
events = []
cursor = ""
while True:
    url = f"http://localhost:8080/v1/observations/events?project={project}&limit=500"
    if cursor:
        url += f"&cursor={cursor}"
    with urllib.request.urlopen(url, timeout=60) as response:
        page = json.load(response)
    events.extend(page["events"])
    cursor = page.get("next_cursor", "")
    if not cursor:
        break
os.makedirs(os.path.dirname(target), exist_ok=True)
with open(target, "w", encoding="utf-8") as handle:
    json.dump(events, handle, indent=1, ensure_ascii=False)
    handle.write("\n")
print(f"exported {len(events)} events -> {target}")
