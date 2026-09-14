#!/usr/bin/env python3
import json
import os
import subprocess
import time
import urllib.error
import urllib.request
import uuid

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
        episode_id = str(uuid.uuid4())
        create = {
            "frame": {
                "agent_id": str(uuid.uuid4()),
                "episode_id": episode_id,
                "branch_id": str(uuid.uuid4()),
                "objective_id": str(uuid.uuid4()),
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
        emission = {
            "schema": "frp.cognitive-emission.v1",
            "emission_id": str(uuid.uuid4()),
            "frame_id": initial["frame_id"],
            "attention": [{"op": "attend", "target": {"type": "query", "text": "evidence that validates Temporality"}}],
            "frame_ops": [{"op": "pin", "ref": f"event:{created['event']['event_id']}"}],
        }
        _, reduced = request("POST", f"/v1/frames/{initial['frame_id']}/emissions", emission)
        next_frame = reduced["decision"]["frame"]
        _, restored = request("GET", f"/v1/frames/{next_frame['frame_id']}")
        _, replay = request("POST", "/v1/replay", {"episode_id": episode_id})
        assert restored["parent_frame_id"] == initial["frame_id"]
        assert restored["focus"]["query"] == "evidence that validates Temporality" and restored["revision"] == 1
        assert len(restored["working_set"]) == 1
        assert len(replay["events"]) == 2 and replay["digest"]
        print(json.dumps({"status": "ok", "initial_frame_id": initial["frame_id"], "next_frame_id": restored["frame_id"], "replay_events": len(replay["events"]), "replay_digest": replay["digest"]}, indent=2))
    finally:
        runtime.terminate()
        try:
            runtime.wait(timeout=5)
        except subprocess.TimeoutExpired:
            runtime.kill()


if __name__ == "__main__":
    main()
