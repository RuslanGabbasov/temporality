"""LangGraph → Temporality adapter: the experience layer for an existing graph.

Wraps a compiled LangGraph so every graph invocation becomes a Temporality
run: events stream to the journal, terminal runs get knowledge extraction,
and new invocations receive hints from accumulated experience. No changes to
the graph itself are required.

    pip install requests langgraph langchain-openai
    export TEMPORALITY_URL=http://localhost:8080
    export TEMPORALITY_TOKEN=<journal bearer>
    export KERNEL_URL=http://localhost:8090
    export EXTERNAL_SOURCE_ID=langgraph-billing
    export OPENAI_API_KEY=...
    export OPENAI_BASE_URL=https://api.openai.com/v1
    export OPENAI_MODEL=gpt-4o-mini

    graph = build_graph()  # your existing LangGraph
    graph = TemporalityGraph(graph, project="billing").wrap()
    graph.invoke({"messages": [...]}, config={"configurable": {"thread_id": "..."}})
"""

import functools
import time
import uuid

import requests

JOURNAL = ""
KERNEL = ""
TOKEN = ""
SOURCE = "langgraph-app"
PROJECT = "default"


def _headers():
    return {"Authorization": f"Bearer {TOKEN}"}


def report_event(project, run_id, seq, kind, data):
    """Append one run event (temporality.event/1) to the journal."""
    body = {
        "events": [{
            "schema": "temporality.event/1",
            "event_id": f"{SOURCE}/{run_id}/{seq}",
            "occurred_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "source": {"id": SOURCE, "integration": "langgraph", "version": "1"},
            "context": {"project": project, "run": run_id,
                        "actor": {"id": SOURCE, "type": "agent"}},
            "type": kind,
            "data": data,
        }]
    }
    requests.post(f"{JOURNAL}/v1/observations/events", json=body,
                  headers=_headers(), timeout=10).raise_for_status()


def fetch_hints(project, query):
    """Prior experience for the task, with states and cautions."""
    response = requests.post(
        f"{JOURNAL}/v1/observations/hints",
        json={"project": project, "query": query, "limit": 5},
        headers=_headers(), timeout=30)
    response.raise_for_status()
    return response.json().get("hints", [])


def kick_extraction(project, run_id):
    """Ask the kernel to extract knowledge from a terminal run (idempotent)."""
    response = requests.post(
        f"{KERNEL}/v1/agent/runs/{run_id}/extract",
        params={"project": project}, json={"source_id": SOURCE},
        headers=_headers(), timeout=10)
    # 409 = already extracted — fine, the first call won.
    if response.status_code not in (200, 202, 409):
        response.raise_for_status()


class TemporalityGraph:
    """Attaches the Temporality experience loop to a compiled LangGraph."""

    def __init__(self, graph, project):
        self.graph = graph
        self.project = project

    def wrap(self):
        self.graph.invoke = self._instrumented(self.graph.invoke)
        return self.graph

    def _instrumented(self, invoke):
        @functools.wraps(invoke)
        def wrapper(input_state, config=None, **kwargs):
            project = (config or {}).get("configurable", {}).get("temporality_project", self.project)
            run_id = f"{SOURCE}-{uuid.uuid4().hex[:8]}"
            messages = input_state.get("messages", [])
            task = messages[-1].get("content", "") if messages else ""
            seq = 0

            # Prior experience lands in the system prompt context: cheap, and
            # the model sees the cautions.
            hints = fetch_hints(project, task)
            hint_block = "\n".join(
                f"- [{h.get('state')}] {h['proposition']}" + (f" (caution: {h['caution']})" if h.get("caution") else "")
                for h in hints)
            if hint_block:
                input_state = dict(input_state)
                input_state["messages"] = list(messages) + [{
                    "role": "system",
                    "content": f"Prior team experience for this task:\n{hint_block}",
                }]

            seq += 1
            report_event(project, run_id, seq, "run.started",
                         {"task_id": run_id, "prompt": task})
            seq += 1
            report_event(project, run_id, seq, "turn.started", {"turn": 1})
            try:
                result = invoke(input_state, config, **kwargs)
            except Exception as failure:
                seq += 1
                report_event(project, run_id, seq, "run.failed",
                             {"error": str(failure)[:500]})
                kick_extraction(project, run_id)
                raise
            seq += 1
            report_event(project, run_id, seq, "turn.completed", {"turn": 1})

            # The final message of the graph is the run's answer.
            answer = ""
            result_messages = result.get("messages", [])
            if result_messages:
                last = result_messages[-1]
                answer = getattr(last, "content", "") or str(last)
            seq += 1
            model_name = ((config or {}).get("configurable", {}) or {}).get("model", "")
            report_event(project, run_id, seq, "model.completed",
                         {"turn": 1, "model": model_name, "content": answer[:4000]})
            seq += 1
            report_event(project, run_id, seq, "run.completed", {"turns": 1})
            kick_extraction(project, run_id)
            return result
        return wrapper
