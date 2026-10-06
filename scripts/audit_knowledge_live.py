#!/usr/bin/env python3
"""Audit docs/knowledge-evolution.md §11 scenarios A/B/D against live journal data."""
import json
import urllib.request
import urllib.parse

JOURNAL = "http://localhost:3000/api"
TOKEN = "2160a5c69fd15423ebd848a8aa709fa5c78a707e99103a41"


def get(base, path, params=None):
    query = ("?" + urllib.parse.urlencode(params)) if params else ""
    request = urllib.request.Request(base + path + query, headers={"Authorization": "Bearer " + TOKEN})
    with urllib.request.urlopen(request) as response:
        return json.load(response)


def all_events(project, event_type=None):
    events, cursor = [], ""
    while True:
        params = {"project": project, "limit": 500}
        if event_type:
            params["type"] = event_type
        if cursor:
            params["cursor"] = cursor
        page = get(JOURNAL, "/v1/observations/events", params)
        events.extend(page.get("events") or [])
        cursor = page.get("next_cursor") or ""
        if not cursor or not page.get("events"):
            return events


def projects():
    data = get("http://localhost:3000/kernel-api", "/v1/workspace/projects")
    return [p["id"] for p in data.get("projects") or []]


report = {}
for project in projects():
    knowledge = (get(JOURNAL, "/v1/observations/knowledge", {"project": project}) or {}).get("knowledge") or []
    extractions = all_events(project, "knowledge.extraction.completed")
    failures = all_events(project, "knowledge.extraction.failed")
    runs = all_events(project, "run.started")

    # Scenario A: one-off runs should teach little.
    ext_runs = len(extractions)
    zero = sum(1 for e in extractions if int(e["data"].get("candidates_count") or 0) == 0)
    small = sum(1 for e in extractions if 0 < int(e["data"].get("candidates_count") or 0) <= 2)
    by_prefix = {}
    for item in knowledge:
        prefix = item["id"].split("/")[0] + "/"
        by_prefix[prefix] = by_prefix.get(prefix, 0) + 1

    # Scenario B: reinforcement = proposed in run X, confirmed/used from another run Y.
    reinforced, examples = 0, []
    for item in knowledge:
        proposal_runs = {t.get("event_id", "").split("/")[0] for t in item.get("history") or [] if t["type"] == "knowledge.proposed"}
        later = [t for t in item.get("history") or []
                 if t["type"] in ("knowledge.confirmed", "knowledge.used")
                 and (t.get("rule") or "").startswith(("extraction-", "execution-"))]
        if proposal_runs and later:
            reinforced += 1
            if len(examples) < 4:
                examples.append((item["id"][:44], item["state"], item["proposition"][:70]))
    # State histogram
    states = {}
    for item in knowledge:
        states[item["state"]] = states.get(item["state"], 0) + 1
    # Scenario D: scope leakage — anything wider than project birth scope.
    widened = [(i["id"][:40], i.get("scope_kind"), i.get("scope_id"), i["proposition"][:50])
               for i in knowledge if i.get("scope_kind") not in ("", None, "project")]

    report[project] = {
        "runs": len(runs),
        "extractions": ext_runs,
        "extraction_failures": len(failures),
        "zero_candidate_extractions": zero,
        "upto2_candidate_extractions": small,
        "knowledge_total": len(knowledge),
        "knowledge_by_prefix": by_prefix,
        "states": states,
        "reinforced": reinforced,
        "reinforced_examples": examples,
        "widened_scope": widened,
    }

print(json.dumps(report, ensure_ascii=False, indent=1))
