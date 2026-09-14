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
EXECUTOR_BINARY = os.environ.get("TEMPORALITY_EXECUTOR_BINARY", "./bin/temporality-executor")
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
    executor = None
    try:
        wait_until_ready()
        episode_id = str(uuid.uuid4())
        objective_id = str(uuid.uuid4())
        objective = {"objective": {"objective_id": objective_id, "episode_id": episode_id, "text": "Verify Temporality render and cognition workflow", "success_conditions": ["render_is_deterministic", "frame_transition_is_replayable"], "constraints": {"max_cost": 1.0}}, "event": {"payload": {}, "provenance": {"source": "smoke"}}}
        request("POST", "/v1/objectives", objective)
        create = {
            "frame": {
                "agent_id": str(uuid.uuid4()),
                "episode_id": episode_id,
                "branch_id": str(uuid.uuid4()),
                "objective_id": objective_id,
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
        _, projection = request("POST", "/v1/projections/regions/rebuild", {"episode_id": episode_id, "branch_id": initial["branch_id"]})
        assert projection["projection_version"] == "region-event-type.v1" and projection["regions"]
        render_request = {"frame_id": initial["frame_id"], "objective_id": objective_id, "budget_tokens": 2000}
        _, first_render = request("POST", "/v1/render", render_request)
        _, repeated_render = request("POST", "/v1/render", render_request)
        assert first_render == repeated_render
        assert first_render["provenance"]["attention_version"] == "attention-0.3.1"
        map_items = next(section for section in first_render["sections"] if section["kind"] == "map")["items"]
        assert map_items and any(item["candidate"]["ref"]["type"] == "region" for item in map_items)
        definition = {"id": "inspect_environment", "execution_mode": "deterministic", "input_schema": {}, "capabilities": ["filesystem.read"], "limits": {"timeout_sec": 30, "cpu": 1, "memory_mb": 128, "disk_mb": 64}, "planner": {}, "failure_policy": {"retry_transient": False, "allow_strategy_change": False, "max_retries": 0}}
        adaptive_definition = {"id": "adaptive_inspection", "execution_mode": "adaptive", "input_schema": {}, "capabilities": ["filesystem.read"], "limits": {"timeout_sec": 30, "cpu": 1, "memory_mb": 128, "disk_mb": 64}, "planner": {"enabled": True, "model": "recorded", "max_steps": 2}, "failure_policy": {"retry_transient": False, "allow_strategy_change": True, "max_retries": 0}}
        planner_trace = [{"step": {"id": "inspect", "capability": "filesystem.read", "operation": "stat", "input": {"path": "."}}}, {"complete": True, "summary": "environment inspected"}]
        emission = {"schema": "frp.cognitive-emission.v1", "emission_id": str(uuid.uuid4()), "frame_id": initial["frame_id"], "claims": [{"proposition": "Temporality step is atomic", "confidence": 0.95, "status": "candidate"}], "attention": [{"op": "attend", "target": {"type": "query", "text": "evidence that validates Temporality"}}], "frame_ops": [{"op": "pin", "ref": f"event:{created['event']['event_id']}"}], "actions": [{"affordance": "inspect_environment", "args": {"path": "."}}, {"affordance": "adaptive_inspection", "args": {"objective": "inspect environment", "planner_proposals": planner_trace}}]}
        _, stepped = request("POST", "/v1/step", {"frame_id": initial["frame_id"], "emission": emission, "definitions": [definition, adaptive_definition]})
        next_frame = stepped["frame"]
        assert len(stepped["claims"]) == 1 and len(stepped["executions"]) == 2
        claim_id = stepped["claims"][0]["claim_id"]
        _, restored = request("GET", f"/v1/frames/{next_frame['frame_id']}")
        execution_ids = [item["execution_id"] for item in stepped["executions"]]
        execution_id = execution_ids[0]
        executor_env = os.environ.copy()
        executor_env.update({"DATABASE_URL": DATABASE_URL, "EPISODE_ID": episode_id})
        executor = subprocess.Popen([EXECUTOR_BINARY], env=executor_env)
        final_executions = {}
        for _ in range(80):
            for item_id in execution_ids:
                _, final_executions[item_id] = request("GET", f"/v1/executions/{item_id}")
            if all(item["status"] in ("completed", "failed") for item in final_executions.values()):
                break
            time.sleep(0.1)
        assert all(item["status"] == "completed" for item in final_executions.values())
        final_execution = final_executions[execution_id]
        _, snapshot = request("POST", "/v1/snapshots", {"frame_id": restored["frame_id"]})
        _, frame_replay = request("POST", "/v1/replay", {"frame_id": restored["frame_id"]})
        assert frame_replay["snapshot"]["snapshot_id"] == snapshot["metadata"]["snapshot_id"]
        assert frame_replay["deterministic_hash"] and len(frame_replay["events"]) == 9
        _, fork_group = request("POST", "/v1/fork", {"source_frame_id": restored["frame_id"], "branches": [{"label": "model-a", "model_config": {"model_id": "A"}}, {"label": "model-b", "model_config": {"model_id": "B"}}]})
        assert len(fork_group["branches"]) == 2
        left_branch, right_branch = fork_group["branches"]
        assert left_branch["root_frame_id"] != right_branch["root_frame_id"]
        assert left_branch["source_frame_id"] == right_branch["source_frame_id"] == restored["frame_id"]
        _, persisted_left = request("GET", f"/v1/branches/{left_branch['branch_id']}")
        assert persisted_left["head_frame_id"] == left_branch["root_frame_id"]
        _, blame = request("POST", "/v1/blame", {"root_id": claim_id, "max_depth": 8})
        assert blame["root_id"] == claim_id and len(blame["nodes"]) >= 2
        _, replay = request("POST", "/v1/replay", {"episode_id": episode_id})
        assert restored["parent_frame_id"] == initial["frame_id"]
        assert restored["focus"]["query"] == "evidence that validates Temporality" and restored["revision"] == 1
        assert len(restored["working_set"]) == 1
        assert len(replay["events"]) >= 17 and replay["digest"]
        print(json.dumps({"status": "ok", "objective_id": objective_id, "initial_frame_id": initial["frame_id"], "render_id": first_render["render_id"], "attention_version": first_render["provenance"]["attention_version"], "region_count": len(projection["regions"]), "next_frame_id": restored["frame_id"], "execution_id": execution_id, "execution_status": final_execution["status"], "snapshot_id": snapshot["metadata"]["snapshot_id"], "frame_hash": frame_replay["frame_hash"], "frame_replay_events": len(frame_replay["events"]), "blame_nodes": len(blame["nodes"]), "fork_group_id": fork_group["fork_group_id"], "fork_branches": len(fork_group["branches"]), "replay_events": len(replay["events"]), "replay_digest": replay["digest"]}, indent=2))
    finally:
        if executor is not None:
            executor.terminate()
            try:
                executor.wait(timeout=5)
            except subprocess.TimeoutExpired:
                executor.kill()
        runtime.terminate()
        try:
            runtime.wait(timeout=5)
        except subprocess.TimeoutExpired:
            runtime.kill()


if __name__ == "__main__":
    main()
