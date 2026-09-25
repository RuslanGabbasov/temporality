#!/usr/bin/env python3
"""Record an operator knowledge event (challenge / invalidate via raw event).

Usage:
  operator_event.py challenged <knowledge_id> <reason>
"""
import datetime
import json
import sys
import urllib.request
import uuid

kind, knowledge_id, reason = sys.argv[1], sys.argv[2], sys.argv[3]
event = {
    "schema": "temporality.event/1",
    "event_id": str(uuid.uuid4()),
    "occurred_at": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
    "source": {"id": "temporality-manual", "integration": "temporality", "version": "1"},
    "context": {"project": "forge", "actor": {"id": "human-operator", "type": "human"}},
    "type": f"knowledge.{kind}",
    "data": {"knowledge_id": knowledge_id, "reason": reason},
}
request = urllib.request.Request(
    "http://localhost:8080/v1/observations/events",
    data=json.dumps({"events": [event]}).encode(),
    headers={"Content-Type": "application/json"},
    method="POST",
)
with urllib.request.urlopen(request, timeout=30) as response:
    print(response.status, json.load(response))
