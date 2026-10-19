#!/usr/bin/env python3
"""External-agent adapter example: any framework, one experience loop.

The script drives a minimal agent loop with a plain OpenAI-compatible API —
no Temporality SDK, no kernel harness — and wires it into Temporality's
experience layer:

  1. reports the run's events to the journal (temporality.event/1),
  2. kicks off knowledge extraction when the run is terminal,
  3. queries hints (prior experience) before a NEW task.

Contract (docs/external-agents.md): the journal understands a small set of
standard event types; the run id ties everything together.

Usage:
    pip install requests
    export TEMPORALITY_URL=http://localhost:8080
    export TEMPORALITY_TOKEN=<journal bearer>
    export KERNEL_URL=http://localhost:8090
    export MODEL_BASE_URL=https://api.example.com/v1
    export MODEL_API_KEY=...
    export MODEL_NAME=...
    python3 external_agent.py <project> "the task text"
"""

import os
import sys
import time
import uuid

import requests

JOURNAL = os.environ.get("TEMPORALITY_URL", "http://localhost:8080").rstrip("/")
KERNEL = os.environ.get("KERNEL_URL", "http://localhost:8090").rstrip("/")
TOKEN = os.environ.get("TEMPORALITY_TOKEN", "")
SOURCE = os.environ.get("EXTERNAL_SOURCE_ID", "external-agent-demo")
MODEL_BASE = os.environ.get("MODEL_BASE_URL", "")
MODEL_KEY = os.environ.get("MODEL_API_KEY", "")
MODEL_NAME = os.environ.get("MODEL_NAME", "gpt-4o-mini")


def report(project, run_id, kind, data, seq):
    """Append one run event to the Temporality journal."""
    body = {
        "events": [{
            "schema": "temporality.event/1",
            "event_id": f"{SOURCE}/{run_id}/{seq}",
            "occurred_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "source": {"id": SOURCE, "integration": SOURCE, "version": "1"},
            "context": {"project": project, "run": run_id, "actor": {"id": "external-demo", "type": "agent"}},
            "type": kind,
            "data": data,
        }]
    }
    response = requests.post(
        f"{JOURNAL}/v1/observations/events", json=body,
        headers={"Authorization": f"Bearer {TOKEN}"}, timeout=10)
    response.raise_for_status()


def hints(project, query):
    """Ask Temporality for prior experience relevant to the task."""
    response = requests.post(
        f"{JOURNAL}/v1/observations/hints",
        json={"project": project, "query": query, "limit": 5},
        headers={"Authorization": f"Bearer {TOKEN}"}, timeout=30)
    response.raise_for_status()
    return response.json().get("hints", [])


def extract(project, run_id):
    """Kick off knowledge extraction for a finished run (idempotent)."""
    response = requests.post(
        f"{KERNEL}/v1/agent/runs/{run_id}/extract",
        params={"project": project},
        json={"source_id": SOURCE},
        headers={"Authorization": f"Bearer {TOKEN}"}, timeout=10)
    if response.status_code == 409:
        print("  extraction already ran for this run — skipped")
    else:
        response.raise_for_status()
        print("  extraction accepted")


def chat(task):
    """One model call through any OpenAI-compatible API."""
    response = requests.post(
        f"{MODEL_BASE}/chat/completions",
        json={"model": MODEL_NAME, "messages": [{"role": "user", "content": task}]},
        headers={"Authorization": f"Bearer {MODEL_KEY}"}, timeout=120)
    response.raise_for_status()
    data = response.json()
    return data["choices"][0]["message"]["content"], data.get("usage", {})


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(1)
    project, task = sys.argv[1], sys.argv[2]
    run_id = f"{SOURCE}-{uuid.uuid4().hex[:8]}"
    seq = 0
    print(f"run {run_id} in project {project}")

    # Prior experience first: what does Temporality already know about this?
    for hint in hints(project, task):
        state = hint.get("state", "")
        caution = f" ({hint['caution']})" if hint.get("caution") else ""
        print(f"  hint: {hint['proposition'][:100]} [{state}{caution}]")

    seq += 1; report(project, run_id, "run.started", {"task_id": task[:40], "prompt": task}, seq)
    seq += 1; report(project, run_id, "turn.started", {"turn": 1}, seq)
    seq += 1; report(project, run_id, "model.started", {"turn": 1, "model": MODEL_NAME}, seq)

    answer, usage = chat(task)

    seq += 1; report(project, run_id, "model.completed",
           {"turn": 1, "model": MODEL_NAME, "output_tokens": usage.get("completion_tokens", 0),
            "total_tokens": usage.get("total_tokens", 0), "content": answer[:4000]}, seq)
    seq += 1; report(project, run_id, "turn.completed", {"turn": 1}, seq)
    seq += 1; report(project, run_id, "run.completed", {"turns": 1}, seq)
    print(f"  answer: {answer[:200]}{'…' if len(answer) > 200 else ''}")

    # The run is terminal — hand it to Temporality's experience loop.
    extract(project, run_id)
    print("done — knowledge from this run will surface as hints on future tasks")


if __name__ == "__main__":
    main()
