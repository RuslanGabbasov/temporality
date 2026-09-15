#!/usr/bin/env python3
"""First contact: an empty-memory agent meets a real git repository (M15 demo).

Drives the full closed loop with a live LLM through the public API:

    WORLD -> OBSERVE -> MEMORY -> ATTENTION -> FRAME -> COGNITION (LLM)
         -> AFFORDANCE -> EXECUTION -> WORLD

The script creates a small git repository with a subtly buggy function and a
failing test, declares it as a World, runs the bootstrap discovery, then drives
/v1/model-step cycles where the model itself chooses read-only world actions
until it answers or the step budget runs out. Every narration line maps to an
event you can then inspect in the debugger or via /v1/events.

Preconditions:
  * PostgreSQL shared with the debugger stack (docker compose up -d postgres debugger)
  * `docker compose stop executor` (a compose executor steals executions)
  * ./bin/temporality-runtime and ./bin/temporality-executor built (make build)
  * model credentials in .env or the environment (TEMPORALITY_MODEL_*)

By default the script spawns its own local runtime so world paths stay on the
host (bootstrap ingestion runs inside the runtime); use --runtime URL to attach
to an existing runtime that can see the paths.
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid

EXECUTOR_BINARY = os.environ.get("TEMPORALITY_EXECUTOR_BINARY", "./bin/temporality-executor")
RUNTIME_BINARY = os.environ.get("TEMPORALITY_BINARY", "./bin/temporality-runtime")
DATABASE_URL = os.environ.get("DATABASE_URL", "postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable")

LIMITS = {"timeout_sec": 30, "cpu": 1, "memory_mb": 128, "disk_mb": 64}
FAILURE_POLICY = {"retry_transient": False, "allow_strategy_change": False, "max_retries": 0}


def request(method, path, body=None, base=None, timeout=15):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request((base or BASE) + path, data=data, method=method, headers={"content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as response:
        return response.status, json.load(response)


def request_status(method, path, body=None, base=None, timeout=15):
    try:
        return request(method, path, body, base, timeout)
    except urllib.error.HTTPError as error:
        return error.code, json.loads(error.read().decode() or "{}")


def say(text=""):
    print(text, flush=True)


def head(text):
    say(f"\n=== {text} " + "=" * max(0, 66 - len(text)))


def compose_executor_running():
    try:
        out = subprocess.run(["docker", "compose", "ps", "--format", "json", "executor"], capture_output=True, text=True, timeout=15)
        if out.returncode != 0:
            return False
        data = json.loads(out.stdout or "[]")
        if isinstance(data, dict):
            data = [data]
        return any((item.get("State") or item.get("state") or "").lower() in ("running", "restarting") for item in data)
    except Exception:
        return False


def read_affordances():
    """Fetch the canonical affordance definitions from the runtime so the
    frozen registry never sees a hand-crafted variant of a standard id."""
    wanted = {"inspect_repository", "list_files", "read_file", "git_status", "git_log"}
    _, payload = request("GET", "/v1/affordances")
    definitions = [item for item in payload.get("definitions") or [] if item.get("id") in wanted]
    missing = wanted - {item["id"] for item in definitions}
    if missing:
        sys.exit(f"runtime did not serve canonical affordances: missing {sorted(missing)}")
    return sorted(definitions, key=lambda item: item["id"])


def create_workspace():
    workspace = tempfile.mkdtemp(prefix="temporality-first-contact-")
    os.makedirs(os.path.join(workspace, "cmd", "app"))
    with open(os.path.join(workspace, "go.mod"), "w") as handle:
        handle.write("module example.com/calculator\n\ngo 1.23\n")
    with open(os.path.join(workspace, "README.md"), "w") as handle:
        handle.write("# calculator\n\nTiny Go module. Arithmetic helpers live in cmd/app.\nRun tests with `go test ./...`.\n")
    with open(os.path.join(workspace, "cmd", "app", "main.go"), "w") as handle:
        handle.write("package main\n\n// Sum returns the sum of two integers.\nfunc Sum(a, b int) int {\n\treturn a - b\n}\n\nfunc main() {}\n")
    with open(os.path.join(workspace, "cmd", "app", "main_test.go"), "w") as handle:
        handle.write("package main\n\nimport \"testing\"\n\nfunc TestSum(t *testing.T) {\n\tif got := Sum(2, 3); got != 5 {\n\t\tt.Fatalf(\"Sum(2, 3) = %d, want 5\", got)\n\t}\n}\n")
    git_available = subprocess.run(["git", "--version"], capture_output=True).returncode == 0
    if git_available:
        subprocess.run(["git", "-c", "init.defaultBranch=main", "init", "-q", workspace], check=False)
        subprocess.run(["git", "-C", workspace, "add", "."], check=False)
        subprocess.run(["git", "-C", workspace, "-c", "user.email=agent@temporality.dev", "-c", "user.name=FirstContact", "commit", "-q", "-m", "initial: calculator with Sum"], check=False)
    return workspace, git_available


def stop(process):
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


def load_dotenv(path=".env"):
    """Parse KEY=VALUE lines; shell environment always wins over the file."""
    values = {}
    try:
        with open(path) as handle:
            for line in handle:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                key, _, value = line.partition("=")
                values[key.strip()] = value.strip().strip('"').strip("'")
    except FileNotFoundError:
        pass
    return values


def spawn_runtime(port):
    """Start a local runtime so world paths stay host-local (the compose runtime
    cannot see host paths, and bootstrap ingestion runs inside the runtime)."""
    env = {**load_dotenv(), **os.environ}
    env.update({"DATABASE_URL": DATABASE_URL, "HTTP_ADDR": f"127.0.0.1:{port}"})
    if not env.get("TEMPORALITY_MODEL_BASE_URL") or not env.get("TEMPORALITY_MODEL_ID"):
        sys.exit("TEMPORALITY_MODEL_BASE_URL / TEMPORALITY_MODEL_ID are not set (neither in .env nor in the environment)")
    process = subprocess.Popen([RUNTIME_BINARY], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    base = f"http://127.0.0.1:{port}"
    for _ in range(100):
        try:
            if request("GET", "/healthz", base=base)[0] == 200:
                return process, base
        except Exception:
            time.sleep(0.1)
    stop(process)
    sys.exit(f"local runtime did not become ready on {base} (is PostgreSQL up at {DATABASE_URL}?)")


def snippet(text, width=100):
    line = " ".join(str(text).split())
    return line if len(line) <= width else line[: width - 1] + "…"


def wait_execution(execution_id, attempts=100):
    for _ in range(attempts):
        _, state = request("GET", f"/v1/executions/{execution_id}")
        if state["status"] in ("completed", "failed", "unavailable"):
            return state
        time.sleep(0.1)
    return state


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--runtime", default=None, help="attach to an existing runtime instead of spawning a local one (world paths must be visible to that runtime)")
    parser.add_argument("--port", type=int, default=18085, help="local runtime port (default 18085)")
    parser.add_argument("--max-steps", type=int, default=4, help="model-step budget (default 4)")
    parser.add_argument("--budget-tokens", type=int, default=16000)
    parser.add_argument("--keep", action="store_true", help="keep the temporary workspace for inspection")
    args = parser.parse_args()
    global BASE

    head("0. Preconditions")
    runtime_process = None
    if args.runtime:
        BASE = args.runtime.rstrip("/")
    else:
        if not os.path.exists(RUNTIME_BINARY):
            sys.exit(f"runtime binary not found: {RUNTIME_BINARY}\nrun: make build")
        runtime_process, BASE = spawn_runtime(args.port)
    try:
        request("GET", "/healthz")
    except Exception as error:
        if runtime_process is not None:
            stop(runtime_process)
        sys.exit(f"runtime is not reachable at {BASE}: {error}")
    _, config = request_status("GET", "/v1/model/config")
    if not config.get("configured"):
        if runtime_process is not None:
            stop(runtime_process)
        sys.exit(f"model is not configured on {BASE}: {config.get('error') or 'set TEMPORALITY_MODEL_* in .env or the environment'}")
    provenance = config.get("provenance") or {}
    say(f"runtime   : {BASE} ({'local' if runtime_process is not None else 'attached'})")
    say(f"model     : {provenance.get('model')} (max_output_tokens={provenance.get('max_output_tokens')}, timeout={provenance.get('timeout_ms')}ms)")
    if compose_executor_running():
        if runtime_process is not None:
            stop(runtime_process)
        sys.exit("compose executor is running and will steal executions: run `docker compose stop executor` first")
    if not os.path.exists(EXECUTOR_BINARY):
        if runtime_process is not None:
            stop(runtime_process)
        sys.exit(f"executor binary not found: {EXECUTOR_BINARY}\nrun: make build")
    say("executor  : local, world-bound (ok)")

    workspace, git_available = create_workspace()
    say(f"workspace : {workspace} (git={git_available})")
    resource_type = "git_repository" if git_available else "filesystem"
    world_id = "first-contact-" + uuid.uuid4().hex[:10]
    executor = None
    try:
        head("1. Declare the world (M11)")
        world = {
            "world_id": world_id,
            "state_version": 1,
            "resources": [{"id": "repo", "type": resource_type, "path": workspace}],
            "capabilities": ["filesystem.read", "git.read"],
        }
        status, registered = request_status("POST", "/v1/worlds", world)
        if status != 201:
            sys.exit(f"world registration failed: {status} {registered}")
        say(f"world     : {world_id} resources=[repo:{resource_type}] capabilities={world['capabilities']}")

        head("2. Bootstrap: first contact with empty memory (M13+M15)")
        status, boot = request_status("POST", "/v1/bootstrap", {
            "world_id": world_id,
            "objective_text": "Разберись в структуре проекта, найди падающий тест и определи причину его падения",
            "budget_tokens": args.budget_tokens,
        })
        if status != 201:
            sys.exit(f"bootstrap failed: {status} {boot}")
        summary = boot["summary"]
        say(f"episode   : {boot['episode_id']}")
        say(f"frame     : {boot['frame_id']}")
        say(f"discovery : observations={summary['observations']} claims={summary['claims']} regions={summary['regions']} edges={summary['edges']} entities={summary['entities']} relations={summary['relations']}")
        for ingestion in boot.get("ingestions", []):
            if ingestion.get("error"):
                say(f"  ! resource {ingestion['resource_id']}: {ingestion['error']}")
        say("memory now contains a map of the world built only from observation events")

        executor_env = os.environ.copy()
        executor_env.update({"DATABASE_URL": DATABASE_URL, "EPISODE_ID": boot["episode_id"], "WORLD_ID": world_id})
        executor = subprocess.Popen([EXECUTOR_BINARY], env=executor_env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

        head("3. Cognition loop: /v1/model-step (LLM chooses actions)")
        definitions = read_affordances()
        current_frame = boot["frame_id"]
        final_answer = None
        result = {}
        for step in range(1, args.max_steps + 1):
            say(f"\n--- step {step}/{args.max_steps} frame={current_frame[:8]}…")
            # A failed model-step commits nothing: render/emit fail before any
            # persistence and CommitStep is transactional, so retrying the same
            # frame on emission (502/504) or step validation (422) errors is
            # safe; live models are nondeterministic. 400/404 are hard failures.
            for attempt in range(1, 5):
                status, result = request_status("POST", "/v1/model-step", {
                    "frame_id": current_frame,
                    "objective_id": boot["objective_id"],
                    "budget_tokens": args.budget_tokens,
                    "definitions": definitions,
                    "world_id": world_id,
                }, timeout=300)
                if status == 201 or status in (400, 404) or attempt == 4:
                    break
                say(f"retry     : model-step rejected (attempt {attempt}/4, http {status}): {str(result.get('error'))[:160]}")
                time.sleep(2)
            if status != 201:
                sys.exit(f"model-step failed: {status} {json.dumps(result, ensure_ascii=False)[:800]}")
            emission = result["emission"]
            for claim in emission.get("claims") or []:
                say(f"claim     : {snippet(claim['proposition'])} (conf={claim.get('confidence')})")
            for reasoning in emission.get("reasoning") or []:
                say(f"reasoning[{reasoning.get('kind')}] {snippet(reasoning.get('text'))}")
            actions = emission.get("actions") or []
            executions = result["step"].get("executions") or []
            if not actions:
                say("actions   : none this step")
            for action in actions:
                say(f"action    : {action['affordance']} {json.dumps(action.get('args') or {}, ensure_ascii=False)}")
            for execution in executions:
                if not execution.get("world_id"):
                    sys.exit("execution is not world-bound: the runtime image predates world-aware model-step; rebuild with `docker compose up --build -d`")
                say(f"execution : {execution['affordance_id']} -> {execution['status']} (world={execution.get('world_id')}@v{execution.get('world_version')})")
            for execution in executions:
                final = wait_execution(execution["execution_id"])
                mark = "ok" if final["status"] == "completed" else "FAILED"
                say(f"executor  : {execution['affordance_id']} {mark} ({final['status']})")
            if executions:
                _, events_now = request("GET", f"/v1/events?episode_id={boot['episode_id']}&limit=500")
                world_events = [item for item in events_now["events"] if item["type"] == "world.observation"]
                say(f"substrate : world.observation events in episode = {len(world_events)}")
                _, projection = request("POST", "/v1/projections/regions/rebuild", {"episode_id": boot["episode_id"], "branch_id": boot["branch_id"]})
                say(f"substrate : regions rebuilt = {len(projection.get('regions') or [])}")
            if emission.get("completion"):
                final_answer = emission["completion"]
                say(f"completion: {snippet(final_answer, 400)}")
                current_frame = result["step"]["frame"]["frame_id"]
                break
            current_frame = result["step"]["frame"]["frame_id"]

        head("4. What the agent now knows")
        _, events = request("GET", f"/v1/events?episode_id={boot['episode_id']}&limit=500")
        counts = {}
        for item in events["events"]:
            counts[item["type"]] = counts.get(item["type"], 0) + 1
        for kind in sorted(counts):
            say(f"event {kind:<24} x{counts[kind]}")
        status, rebuilt = request_status("POST", "/v1/projections/entities/rebuild")
        entity_count = len(rebuilt.get("entities") or [])
        relation_count = len(rebuilt.get("relations") or [])
        say(f"entity graph rebuilt: entities={entity_count} relations={relation_count} (bootstrap had {summary['entities']}/{summary['relations']})")
        if final_answer is None:
            emissions_answer = [r.get("text") for r in (result.get("emission") or {}).get("reasoning") or [] if r.get("kind") == "answer"]
            final_answer = emissions_answer[0] if emissions_answer else "(model did not emit completion within the step budget)"

        head("5. Result")
        say(f"final frame : {current_frame}")
        say(f"answer      : {snippet(final_answer, 600)}")
        say("")
        say("Inspect the run:")
        say(f"  debugger           : open http://localhost:3000 and paste episode {boot['episode_id']}")
        say(f"  raw events         : curl -s '{BASE}/v1/events?episode_id={boot['episode_id']}&limit=500' | jq")
        say(f"  replay any frame   : curl -s -XPOST {BASE}/v1/replay -d '{{\"frame_id\":\"{current_frame}\"}}' | jq")
        say("")
        say("The debugger answers 'what did the agent know at this step?':")
        say("every frame's render packet is persisted, not reconstructed after the fact.")
        if args.keep:
            say(f"workspace kept: {workspace}")
    finally:
        if executor is not None:
            stop(executor)
        if runtime_process is not None:
            stop(runtime_process)
        if not args.keep:
            shutil.rmtree(workspace, ignore_errors=True)


if __name__ == "__main__":
    main()
