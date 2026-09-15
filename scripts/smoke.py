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


def request_status(method, path, body=None):
    """Like request() but returns (status, payload) instead of raising on 4xx."""
    try:
        status, payload = request(method, path, body)
        return status, payload
    except urllib.error.HTTPError as error:
        return error.code, json.loads(error.read().decode() or "{}")


def world_scenario():
    """M11: register a World, bind executions to it, and verify that read-only
    observations flow back through the event log as world.observation events."""
    import shutil
    import tempfile

    workspace = tempfile.mkdtemp(prefix="temporality-world-")
    try:
        with open(os.path.join(workspace, "README.md"), "w") as handle:
            handle.write("# world workspace\n")
        episode_id = str(uuid.uuid4())
        branch_id = str(uuid.uuid4())
        world_id = "smoke-" + uuid.uuid4().hex[:12]
        world = {
            "world_id": world_id,
            "state_version": 1,
            "resources": [{"id": "workspace", "type": "filesystem", "path": workspace}],
            "capabilities": ["filesystem.read", "git.read"],
            "limits": {"max_read_bytes": 4096, "max_entries": 100, "timeout_sec": 10},
        }
        _, registered = request("POST", "/v1/worlds", world)
        assert registered["world"]["world_id"] == world_id
        stale_status, _ = request_status("POST", "/v1/worlds", world)
        assert stale_status == 422, "stale world state was accepted"
        definition = {"id": "list_files", "execution_mode": "deterministic", "input_schema": {}, "capabilities": ["filesystem.read"], "limits": {"timeout_sec": 30, "cpu": 1, "memory_mb": 128, "disk_mb": 64}, "planner": {}, "failure_policy": {"retry_transient": False, "allow_strategy_change": False, "max_retries": 0}}
        denied_definition = dict(definition, id="run_tests", capabilities=["process.execute"])
        denied_status, _ = request_status("POST", "/v1/executions", {"definition": denied_definition, "episode_id": episode_id, "branch_id": branch_id, "world_id": world_id, "arguments": {}})
        assert denied_status == 422, "capability not granted by world was accepted"
        _, created = request("POST", "/v1/executions", {"definition": definition, "episode_id": episode_id, "branch_id": branch_id, "world_id": world_id, "arguments": {"path": "."}})
        assert created["execution"]["world_id"] == world_id and created["execution"]["world_version"] == 1
        executor_env = os.environ.copy()
        executor_env.update({"DATABASE_URL": DATABASE_URL, "EPISODE_ID": episode_id, "WORLD_ID": world_id})
        executor = subprocess.Popen([EXECUTOR_BINARY], env=executor_env)
        try:
            final = None
            for _ in range(80):
                _, final = request("GET", f"/v1/executions/{created['execution']['execution_id']}")
                if final["status"] in ("completed", "failed"):
                    break
                time.sleep(0.1)
            assert final["status"] == "completed", f"world execution status={final['status']}"
            _, events = request("GET", f"/v1/events?episode_id={episode_id}&limit=100")
            observations = [item for item in events["events"] if item["type"] == "world.observation"]
            assert observations, "world.observation events were not committed"
            first = observations[0]["payload"]
            assert first["world_id"] == world_id and first["execution_id"] == created["execution"]["execution_id"]
            assert first["observation_type"] in ("stat", "directory_listing")
            _, projection = request("POST", "/v1/projections/regions/rebuild", {"episode_id": episode_id, "branch_id": branch_id})
            kinds = {region["label"] for region in projection["regions"]}
            assert "world.observation" in kinds, f"observations missing from regions: {kinds}"
            return {"world_id": world_id, "observations": len(observations), "execution_status": final["status"], "region_kinds": len(kinds)}
        finally:
            executor.terminate()
            try:
                executor.wait(timeout=5)
            except subprocess.TimeoutExpired:
                executor.kill()
    finally:
        shutil.rmtree(workspace, ignore_errors=True)


def ingestion_scenario():
    """M13+M14: bootstrap knowledge. An empty substrate is populated from a real
    workspace through one bounded ingestion run: observations become
    world.observation events, deterministic extractors create candidate claims
    citing those events as evidence, and triple-bearing claims project into the
    entity knowledge graph that render can attend over."""
    import shutil
    import tempfile

    workspace = tempfile.mkdtemp(prefix="temporality-ingest-")
    try:
        with open(os.path.join(workspace, "go.mod"), "w") as handle:
            handle.write("module example.com/smoke-demo\n\ngo 1.23\n\nrequire (\n\tgithub.com/smoke/dep v1.2.3\n)\n")
        with open(os.path.join(workspace, "README.md"), "w") as handle:
            handle.write("# Smoke Demo\n")
        os.makedirs(os.path.join(workspace, "cmd", "demo"), exist_ok=True)
        with open(os.path.join(workspace, "cmd", "demo", "main.go"), "w") as handle:
            handle.write("package main\n")
        episode_id = str(uuid.uuid4())
        branch_id = str(uuid.uuid4())
        world_id = "smoke-ingest-" + uuid.uuid4().hex[:10]
        world = {
            "world_id": world_id,
            "state_version": 1,
            "resources": [{"id": "repo", "type": "filesystem", "path": workspace}],
            "capabilities": ["filesystem.read"],
        }
        _, registered = request("POST", "/v1/worlds", world)
        assert registered["world"]["world_id"] == world_id
        _, result = request("POST", "/v1/ingest", {"world_id": world_id, "resource_id": "repo", "episode_id": episode_id, "branch_id": branch_id, "depth": 2})
        assert result["observations"], "ingestion produced no observations"
        propositions = {claim["proposition"]: claim for claim in result["claims"]}
        module_claim = propositions.get('Go module "example.com/smoke-demo" is declared at go.mod')
        assert module_claim, f"module claim missing: {list(propositions)}"
        assert 0 < module_claim["confidence"] < 1, "extraction confidence must stay below 1"
        assert module_claim["evidence"], "extracted claim carries no evidence"
        _, evidence = request("GET", f"/v1/claims/{module_claim['claim_id']}/evidence")
        assert evidence["events"], "evidence endpoint returned no events"
        assert all(item["type"] == "world.observation" for item in evidence["events"])
        assert any(item["payload"]["observation_type"] == "file_content" for item in evidence["events"])
        _, events = request("GET", f"/v1/events?episode_id={episode_id}&limit=100")
        observations = [item for item in events["events"] if item["type"] == "world.observation"]
        assert len(observations) == len(result["observations"]), "committed observations differ from run result"
        assert all(item["provenance"]["source"] == "ingestion" for item in observations)
        _, projection = request("POST", "/v1/projections/regions/rebuild", {"episode_id": episode_id, "branch_id": branch_id})
        assert projection["regions"], "regions missing after ingestion"
        assert any(region["label"] == "world.observation" for region in projection["regions"])

        # M14: rebuild the entity graph from the triple-bearing claims.
        _, entities = request("POST", "/v1/projections/entities/rebuild", {})
        assert entities["projection_version"] == "entity-claims.v1"
        assert entities["entities"] and entities["relations"]
        predicates = {relation["predicate"] for relation in entities["relations"]}
        assert {"declared_in", "targets", "depends_on", "contains"} <= predicates, predicates
        _, modules = request("GET", "/v1/entities?type=module")
        module_entity = next(item for item in modules["entities"] if item["name"] == "example.com/smoke-demo")
        assert module_entity["mention_count"] >= 3
        _, module_detail = request("GET", f"/v1/entities/{module_entity['entity_id']}")
        module_predicates = {relation["predicate"] for relation in module_detail["relations"]}
        assert {"declared_in", "targets", "depends_on"} <= module_predicates, module_predicates

        # M14.3: entities compete for attention inside a render of a frame in
        # the ingested episode.
        objective_id = str(uuid.uuid4())
        request("POST", "/v1/objectives", {"objective": {"objective_id": objective_id, "episode_id": episode_id, "text": "Understand the smoke demo module and its dependencies", "success_conditions": ["map_contains_entities"], "constraints": {}}, "event": {"payload": {}, "provenance": {"source": "smoke"}}})
        create = {
            "frame": {
                "agent_id": str(uuid.uuid4()),
                "episode_id": episode_id,
                "branch_id": branch_id,
                "objective_id": objective_id,
                "focus": {"type": "query", "query": "smoke demo module dependencies"},
                "mode": "explore",
                "attention": {"policy": "balanced", "deliberate": True, "ambient": True, "max_candidates": 32},
                "zoom": 2,
                "filters": {"trust_min": 0.0},
                "budget": {"tokens": 8000},
            },
            "event": {"payload": {}, "provenance": {"source": "smoke"}},
        }
        _, created = request("POST", "/v1/frames", create)
        frame = created["frame"]
        _, rendered = request("POST", "/v1/render", {"frame_id": frame["frame_id"], "objective_id": objective_id, "budget_tokens": 16000})
        map_items = next(section for section in rendered["sections"] if section["kind"] == "map")["items"]
        entity_refs = [item["candidate"]["ref"] for item in map_items if item["candidate"]["ref"]["type"] == "entity"]
        assert entity_refs, "render map contains no entity candidates"
        return {"world_id": world_id, "observations": len(result["observations"]), "claims": len(result["claims"]), "regions": len(projection["regions"]), "entities": len(entities["entities"]), "entity_relations": len(entities["relations"]), "render_entity_candidates": len(entity_refs)}
    finally:
        shutil.rmtree(workspace, ignore_errors=True)


def main():
    env = os.environ.copy()
    env.update({"DATABASE_URL": DATABASE_URL, "HTTP_ADDR": "127.0.0.1:18080"})
    runtime = subprocess.Popen([BINARY], env=env)
    executor = None
    try:
        wait_until_ready()
        episode_id = str(uuid.uuid4())
        objective_id = str(uuid.uuid4())
        objective = {"objective": {"objective_id": objective_id, "episode_id": episode_id, "text": "Inspect environment and verify Temporality render and cognition workflow", "success_conditions": ["render_is_deterministic", "frame_transition_is_replayable"], "constraints": {"max_cost": 1.0}}, "event": {"payload": {}, "provenance": {"source": "smoke"}}}
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
        assert projection["projection_version"] == "region-event-type.v1" and projection["edge_projection_version"] == "region-edges.v1" and projection["regions"]
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
        _, procedure_projection = request("POST", "/v1/projections/procedures/rebuild", {"episode_id": episode_id, "minimum_evidence": 1})
        assert procedure_projection["procedures"]
        _, learned_render = request("POST", "/v1/render", {"frame_id": restored["frame_id"], "objective_id": objective_id, "budget_tokens": 3000})
        procedure_items = next(section for section in learned_render["sections"] if section["kind"] == "procedures")["items"]
        assert procedure_items and procedure_items[0]["provenance"]["projection_version"] == "procedure-projector.v1"
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
        world_result = world_scenario()
        ingest_result = ingestion_scenario()
        print(json.dumps({"status": "ok", "objective_id": objective_id, "initial_frame_id": initial["frame_id"], "render_id": first_render["render_id"], "attention_version": first_render["provenance"]["attention_version"], "region_count": len(projection["regions"]), "next_frame_id": restored["frame_id"], "execution_id": execution_id, "execution_status": final_execution["status"], "snapshot_id": snapshot["metadata"]["snapshot_id"], "frame_hash": frame_replay["frame_hash"], "frame_replay_events": len(frame_replay["events"]), "blame_nodes": len(blame["nodes"]), "fork_group_id": fork_group["fork_group_id"], "fork_branches": len(fork_group["branches"]), "procedure_count": len(procedure_projection["procedures"]), "matched_procedures": len(procedure_items), "replay_events": len(replay["events"]), "replay_digest": replay["digest"], "world": world_result, "ingestion": ingest_result}, indent=2))
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
