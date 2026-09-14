#!/usr/bin/env python3
"""End-to-end smoke test for the OpenAI-compatible model path."""

import json
import os
import subprocess
import threading
import time
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

RUNTIME_URL = "http://127.0.0.1:18080"
MODEL_ADDR = ("127.0.0.1", 18081)
BINARY = os.environ.get("TEMPORALITY_BINARY", "./bin/temporality-runtime")
DATABASE_URL = os.environ.get(
    "DATABASE_URL",
    "postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable",
)


def api(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(
        RUNTIME_URL + path,
        data=data,
        method=method,
        headers={"content-type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        return response.status, json.load(response)


class MockModel(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_error(404)
            return
        length = int(self.headers.get("content-length", "0"))
        request = json.loads(self.rfile.read(length))
        user_message = next(
            message["content"] for message in request["messages"] if message["role"] == "user"
        )
        frame_id = json.loads(user_message)["frame_id"]
        emission = {
            "schema": "frp.cognitive-emission.v1",
            "emission_id": str(uuid.uuid4()),
            "frame_id": frame_id,
            "observation": [],
            "reasoning": [{"kind": "decision", "text": "Continue with the smoke objective"}],
            "claims": [],
            "attention": [
                {
                    "op": "attend",
                    "target": {"type": "query", "text": "verify model configuration"},
                }
            ],
            "actions": [],
            "frame_ops": [],
            "completion": None,
        }
        payload = json.dumps(
            {"choices": [{"message": {"role": "assistant", "content": json.dumps(emission)}}]}
        ).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, format, *args):
        pass


def wait_ready():
    for _ in range(100):
        try:
            status, _ = api("GET", "/healthz")
            if status == 200:
                return
        except (urllib.error.URLError, ConnectionError):
            time.sleep(0.1)
    raise RuntimeError("runtime did not become ready")


def stop(process):
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


def main():
    mock = ThreadingHTTPServer(MODEL_ADDR, MockModel)
    mock_thread = threading.Thread(target=mock.serve_forever, daemon=True)
    mock_thread.start()
    env = os.environ.copy()
    env.update(
        {
            "DATABASE_URL": DATABASE_URL,
            "HTTP_ADDR": "127.0.0.1:18080",
            "TEMPORALITY_MODEL_BASE_URL": "http://127.0.0.1:18081/v1",
            "TEMPORALITY_MODEL_ID": "mock-model",
            "TEMPORALITY_MODEL_API_KEY": "smoke-only-not-a-secret",
            "TEMPORALITY_MODEL_TEMPERATURE": "0",
            "TEMPORALITY_MODEL_TIMEOUT": "5s",
        }
    )
    runtime = subprocess.Popen([BINARY], env=env)
    try:
        wait_ready()
        episode_id = str(uuid.uuid4())
        objective_id = str(uuid.uuid4())
        api(
            "POST",
            "/v1/objectives",
            {
                "objective": {
                    "objective_id": objective_id,
                    "episode_id": episode_id,
                    "text": "Verify model configuration end to end",
                    "success_conditions": ["model_step_is_replayable"],
                    "constraints": {},
                },
                "event": {"payload": {}, "provenance": {"source": "model-smoke"}},
            },
        )
        _, created = api(
            "POST",
            "/v1/frames",
            {
                "frame": {
                    "agent_id": str(uuid.uuid4()),
                    "episode_id": episode_id,
                    "branch_id": str(uuid.uuid4()),
                    "objective_id": objective_id,
                    "focus": {"type": "query", "query": "test model adapter"},
                    "mode": "explore",
                    "attention": {
                        "policy": "balanced",
                        "deliberate": True,
                        "ambient": True,
                        "max_candidates": 32,
                    },
                    "zoom": 2,
                    "filters": {"trust_min": 0.5},
                    "budget": {"tokens": 4000},
                },
                "event": {"payload": {}, "provenance": {"source": "model-smoke"}},
            },
        )
        parent = created["frame"]
        _, config = api("GET", "/v1/model/config")
        assert config["configured"] is True
        assert config["provenance"]["model"] == "mock-model"
        assert "api_key" not in json.dumps(config).lower()

        _, result = api(
            "POST",
            "/v1/model-step",
            {
                "frame_id": parent["frame_id"],
                "objective_id": objective_id,
                "budget_tokens": 2000,
                "definitions": [],
            },
        )
        child = result["step"]["frame"]
        assert child["parent_frame_id"] == parent["frame_id"]
        assert child["frame_id"] != parent["frame_id"]
        assert result["model_provenance"] == config["provenance"]
        assert any(event["type"] == "attention.suggested" for event in result["step"]["events"])

        _, replay = api("POST", "/v1/replay", {"frame_id": child["frame_id"]})
        assert replay["frame"]["frame_id"] == child["frame_id"]
        assert replay["frame"]["parent_frame_id"] == parent["frame_id"]
        assert replay["events"] and replay["deterministic_hash"]
        print(json.dumps({"status": "ok", "model": config["provenance"], "child_frame_id": child["frame_id"]}, indent=2))
    finally:
        stop(runtime)
        mock.shutdown()
        mock.server_close()
        mock_thread.join(timeout=5)


if __name__ == "__main__":
    main()
