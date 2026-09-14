#!/usr/bin/env python3
import json
import os
import subprocess
import time
import urllib.error
import urllib.request

BASE = os.environ.get("TEMPORALITY_URL", "http://127.0.0.1:18080")
BINARY = os.environ.get("TEMPORALITY_BINARY", "./bin/temporality-runtime")
DATABASE_URL = os.environ.get("DATABASE_URL", "postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable")


def request(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method, headers={"content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=5) as response:
        return response.status, json.load(response)


def wait_until_ready():
    for _ in range(50):
        try:
            status, _ = request("GET", "/healthz")
            if status == 200:
                return
        except (urllib.error.URLError, ConnectionError):
            time.sleep(0.1)
    raise RuntimeError("runtime did not become ready")


def main():
    env = os.environ.copy()
    env.update({"DATABASE_URL": DATABASE_URL, "HTTP_ADDR": "127.0.0.1:18080"})
    runtime = subprocess.Popen([BINARY], env=env)
    try:
        wait_until_ready()
        episode_id = "018f47a7-34b2-7d10-a932-4f3ff37a5b02"
        create = {
            "frame": {
                "agent_id": "018f47a7-34b2-7d10-a932-4f3ff37a5b01",
                "episode_id": episode_id,
                "branch_id": "018f47a7-34b2-7d10-a932-4f3ff37a5b03",
                "objective_id": "018f47a7-34b2-7d10-a932-4f3ff37a5b04",
                "focus": {"type": "query", "query": "verify Temporality smoke workflow"},
                "mode": "explore",
                "attention": {"policy": "balanced", "deliberate": True, "ambient": True, "max_candidates": 32},
                "zoom": 2,
                "filters": {"trust_min": 0.5},
                "budget": {"tokens": 8000},
            },
            "event": {"payload": {}, "provenance": {"source": "smoke"}},
        }
        _, created = request("POST", "/v1/frames", create)
        initial = created["frame"]
        transition = {
            "transition": {
                "operations": [
                    {"op": "set_mode", "mode": "verify"},
                    {"op": "pin", "ref": {"type": "event", "id": created["event"]["event_id"]}},
                ]
            },
            "event": {"payload": {}, "provenance": {"source": "smoke"}},
        }
        _, transitioned = request("POST", f"/v1/frames/{initial['frame_id']}/transitions", transition)
        next_frame = transitioned["frame"]
        _, restored = request("GET", f"/v1/frames/{next_frame['frame_id']}")
        _, replay = request("POST", "/v1/replay", {"episode_id": episode_id})
        assert restored["parent_frame_id"] == initial["frame_id"]
        assert restored["mode"] == "verify" and restored["revision"] == 1
        assert len(replay["events"]) >= 2 and replay["digest"]
        print(json.dumps({"status": "ok", "initial_frame_id": initial["frame_id"], "next_frame_id": restored["frame_id"], "replay_events": len(replay["events"]), "replay_digest": replay["digest"]}, indent=2))
    finally:
        runtime.terminate()
        try:
            runtime.wait(timeout=5)
        except subprocess.TimeoutExpired:
            runtime.kill()


if __name__ == "__main__":
    main()
