#!/usr/bin/env python3
"""FRP Pivot Longitudinal Memory Benchmark (TZ: temporality-pivot-tz.md §15-19).

Tests the pivot hypothesis after Этапы 1-5 (knowledge lifecycle + investigation
history + warm memory):

    Does the INVESTIGATION STORY (not just the facts) make the next episode
    in the same world cheaper, and does visible refuted history prevent
    repeated investigation?

Arms per pair (pivot TZ §16; same fixture family, Go module example.com/ledger):

  A  fresh             task 1: the two hidden bugs from the base fixture
                        (int32 accumulator, store tombstone); builds the
                        durable world memory (claims by state + focus history)
  B  cold              task 2 on a FRESH substrate — no memory of A at all
  C  warm facts        task 2 on a DB clone right after A; the runtime runs
                        with TEMPORALITY_WORLD_MEMORY_MODE=confirmed, so the
                        agent receives prior FACTS only (no refuted paths, no
                        investigation history, no live hypotheses)
  D  warm investigation task 2 on another clone of the same post-A DB with
                        the full pivot memory (confirmed + refuted +
                        investigated + hypotheses, with provenance)

Task 2 (applied on top of A's fixes): format.go byte order, new parse.go
trailing digit. Bug 1 deliberately reuses task-1's red herring, so stale
warm facts now contradict the world — an honest probe of stale-memory handling.

Main comparison: C vs D (value of the investigation story beyond facts).
Secondary: D vs B (warm vs cold), both vs historical classic-harness numbers
from docs/benchmarks/frp-vs-classic-* (not re-run here).

Warm agents get no transcript, no bug list, no manual summaries — only the
standard world_memory/procedures render path (render-0.5.0+).

Preconditions:
  * PostgreSQL from the compose stack (docker compose up -d postgres)
  * docker compose stop executor
  * make build
  * model credentials in .env (TEMPORALITY_MODEL_*)

Reliability measures (the first full run died in a run_tests-only death
spiral for 42 minutes, poisoning every downstream arm):

  * degenerate guardrail — an episode aborts early once it has run_tests-only
    steps with zero reads/writes (no discovery, no fixes: nothing to learn);
  * per-arm retries with clean-state restore (DB reset/re-clone + git reset);
    a failed checker verdict is retried only for arm A (it seeds everything);
  * task-2 arms B/C/D run in PARALLEL on isolated DBs/workspaces/ports.

Usage:
  python3 scripts/memory_benchmark.py --pairs 3 --scale 8
  python3 scripts/memory_benchmark.py --pairs 1 --stop-after-a   # validate A first
  python3 scripts/memory_benchmark.py --pairs 1 --serial-arms   # provider choking?
"""

import argparse
import json
import os
import re
import shutil
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid
from concurrent.futures import ThreadPoolExecutor

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from benchmark import (  # noqa: E402
    CAP_READ,
    DATABASE_URL,
    EXECUTOR_BINARY,
    FRP_AFFORDANCES,
    OBJECTIVE,
    RUNTIME_BINARY,
    WORLD_LIMITS,
    compose_executor_running,
    create_fixture,
    env_or_fail,
    load_dotenv,
    run_tool,
    say,
    snippet,
    stop,
)

POSTGRES_CONTAINER = os.environ.get("TEMPORALITY_POSTGRES_CONTAINER", "temporality-postgres-1")


# ---------------------------------------------------------------- task 2 delta


def delta_files():
    """Task-2 defects, applied on top of task 1's fixes.

    Bug 1 reuses the task-1 red herring (format.go looked suspicious but was
    correct): after the delta it is genuinely broken, so a stale warm-memory
    claim "format.go is correct" now contradicts the world — an honest probe of
    stale-memory handling (ground truth stays `go test ./...`).
    Bug 2 is brand new code, pure discovery: nothing in prior memory points at
    it.
    """
    format_go = (
        "// Package util holds small formatting helpers.\npackage util\n\nimport \"encoding/hex\"\n\n"
        "// FormatID packs an int64 id into a fixed 16-char hex string.\n//\n"
        "// The reversal below is intentional: ids are stored little-endian on the\n"
        "// wire (design note U-3). Do not \"simplify\" it.\n"
        "func FormatID(id int64) string {\n\tu := uint64(id)\n\tbuf := [8]byte{}\n"
        "\tfor i := 0; i < 8; i++ {\n\t\tbuf[i] = byte(u & 0xff)\n\t\tu >>= 8\n\t}\n"
        "\treturn hex.EncodeToString(buf[:])\n}\n"
    )
    parse_go = (
        "// Parsing helpers for decimal amounts in minor units.\npackage util\n\n"
        "// ParseAmount parses a plain decimal amount in minor units. A trailing\n"
        "// checksum digit is appended by the upstream feed (convention A-77) and is\n"
        "// stripped here before conversion.\n"
        "func ParseAmount(text string) int64 {\n\ttotal := int64(0)\n"
        "\tfor i, r := range text {\n"
        "\t\tif i == len(text)-1 {\n\t\t\tbreak // trailing checksum digit\n\t\t}\n"
        "\t\tif r < '0' || r > '9' {\n\t\t\tcontinue\n\t\t}\n"
        "\t\ttotal = total*10 + int64(r-'0')\n\t}\n\treturn total\n}\n"
    )
    parse_test = (
        "package util\n\nimport \"testing\"\n\n"
        "func TestParseAmount(t *testing.T) {\n"
        "\tif got := ParseAmount(\"1500\"); got != 1500 {\n"
        "\t\tt.Fatalf(\"ParseAmount(1500) = %d, want 1500\", got)\n\t}\n"
        "\tif got := ParseAmount(\"70\"); got != 70 {\n"
        "\t\tt.Fatalf(\"ParseAmount(70) = %d, want 70\", got)\n\t}\n}\n"
    )
    return {
        "internal/util/format.go": format_go,
        "internal/util/parse.go": parse_go,
        "internal/util/parse_test.go": parse_test,
    }


TASK2_FILES = ["internal/util/format.go", "internal/util/parse.go"]


def apply_delta(workspace):
    for relative, content in delta_files().items():
        target = os.path.join(workspace, relative)
        os.makedirs(os.path.dirname(target), exist_ok=True)
        with open(target, "w") as handle:
            handle.write(content)


# ------------------------------------------------- pivot memory instrumentation

GO_PATH_RE = re.compile(r"[\w./\-]+\.go\b")
WORD_RE = re.compile(r"\w+")


def path_refs(text):
    """File paths mentioned in a proposition (used to detect refuted-path
    revisits: a read of a path a refuted claim was about)."""
    return {match.group(0).lstrip("./") for match in GO_PATH_RE.finditer(text or "")}


def paths_match(read_path, mentioned):
    """A read of internal/util/format.go matches a proposition that mentioned
    bare `format.go` or the full path."""
    read_path = read_path.lstrip("./")
    mentioned = mentioned.lstrip("./")
    return read_path == mentioned or read_path.endswith("/" + mentioned) or mentioned.endswith("/" + read_path)


def token_overlap(a, b):
    tokens_a = set(WORD_RE.findall((a or "").lower()))
    tokens_b = set(WORD_RE.findall((b or "").lower()))
    if not tokens_a or not tokens_b:
        return 0.0
    return len(tokens_a & tokens_b) / min(len(tokens_a), len(tokens_b))


# ---------------------------------------------------------------- plumbing


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


def head(text):
    say(f"\n=== {text} " + "=" * max(0, 70 - len(text)))


def psql(database, sql):
    """Run SQL inside the compose postgres container; returns stdout."""
    finished = subprocess.run(
        ["docker", "exec", POSTGRES_CONTAINER, "psql", "-U", "temporality", "-d", database, "-At", "-c", sql],
        capture_output=True, text=True, timeout=30,
    )
    if finished.returncode != 0:
        say(f"psql failed: {finished.stderr.strip()[:200]}")
        return ""
    return finished.stdout.strip()


def reset_database(name):
    subprocess.run(["docker", "exec", POSTGRES_CONTAINER, "psql", "-U", "temporality", "-d", "postgres", "-c", f"DROP DATABASE IF EXISTS {name}"], capture_output=True, timeout=30)
    created = subprocess.run(["docker", "exec", POSTGRES_CONTAINER, "createdb", "-U", "temporality", name], capture_output=True, timeout=30)
    if created.returncode != 0:
        sys.exit(f"createdb {name} failed: {created.stderr.decode()[:300]}")


def clone_database(source, target):
    """Byte-identical substrate copy (pivot benchmark arm isolation): both warm
    arms C and D must see EXACTLY the post-A memory, not each other's runs."""
    subprocess.run(["docker", "exec", POSTGRES_CONTAINER, "psql", "-U", "temporality", "-d", "postgres", "-c", f"DROP DATABASE IF EXISTS {target}"], capture_output=True, timeout=30)
    created = subprocess.run(["docker", "exec", POSTGRES_CONTAINER, "createdb", "-U", "temporality", target], capture_output=True, timeout=30)
    if created.returncode != 0:
        sys.exit(f"createdb {target} failed: {created.stderr.decode()[:300]}")
    dump = subprocess.Popen(["docker", "exec", POSTGRES_CONTAINER, "pg_dump", "-U", "temporality", source], stdout=subprocess.PIPE)
    restore = subprocess.Popen(["docker", "exec", "-i", POSTGRES_CONTAINER, "psql", "-U", "temporality", "-d", target], stdin=dump.stdout, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    dump.stdout.close()
    _, stderr = restore.communicate()
    if restore.returncode != 0:
        sys.exit(f"clone {source} -> {target} failed: {(stderr or b'').decode()[:300]}")


def database_url(name):
    base = DATABASE_URL.rsplit("/", 1)[0]
    return f"{base}/{name}?sslmode=disable"


def spawn_runtime(port, database, extra_env=None):
    env = {**load_dotenv(), **os.environ}
    env.update({"DATABASE_URL": database_url(database), "HTTP_ADDR": f"127.0.0.1:{port}"})
    if extra_env:
        env.update(extra_env)
    if not env.get("TEMPORALITY_MODEL_BASE_URL") or not env.get("TEMPORALITY_MODEL_ID"):
        sys.exit("TEMPORALITY_MODEL_BASE_URL / TEMPORALITY_MODEL_ID are not set")
    probe = subprocess.run(["lsof", "-nP", f"-iTCP:{port}", "-sTCP:LISTEN"], capture_output=True, text=True)
    if probe.returncode == 0 and probe.stdout.strip():
        sys.exit(f"port {port} is already held: {probe.stdout.splitlines()[1] if len(probe.stdout.splitlines()) > 1 else probe.stdout.strip()}")
    process = subprocess.Popen([RUNTIME_BINARY], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, cwd=PROJECT_ROOT)
    base = f"http://127.0.0.1:{port}"
    for _ in range(100):
        try:
            if request("GET", "/healthz", base=base)[0] == 200:
                return process, base
        except Exception:
            time.sleep(0.1)
    stop(process)
    sys.exit(f"runtime did not become ready on {base} (database {database})")


def wait_execution(execution_id, base, attempts=900):
    state = {}
    for _ in range(attempts):
        _, state = request("GET", f"/v1/executions/{execution_id}", base=base)
        if state.get("status") in ("completed", "failed", "unavailable"):
            return state
        time.sleep(0.1)
    return state


def read_affordances(base):
    _, payload = request("GET", "/v1/affordances", base=base)
    definitions = [item for item in payload.get("definitions") or [] if item.get("id") in FRP_AFFORDANCES]
    missing = FRP_AFFORDANCES - {item["id"] for item in definitions}
    if missing:
        sys.exit(f"runtime did not serve canonical affordances: missing {sorted(missing)}")
    return sorted(definitions, key=lambda item: item["id"])


def register_world(base, world_id, workspace, version=1):
    world = {
        "world_id": world_id,
        "state_version": version,
        "resources": [{"id": "repo", "type": "git_repository", "path": workspace}],
        "capabilities": ["filesystem.read", "filesystem.write", "git.read", "process.execute"],
        "limits": WORLD_LIMITS,
    }
    status, payload = request_status("POST", "/v1/worlds", world, base=base)
    if status != 201:
        sys.exit(f"world registration failed: {status} {payload}")
    return world


def spawn_executor(episode_id, world_id, database):
    env = os.environ.copy()
    env.update({"DATABASE_URL": database_url(database), "EPISODE_ID": episode_id, "WORLD_ID": world_id})
    if "/usr/local/go/bin" not in env.get("PATH", ""):
        env["PATH"] = "/usr/local/go/bin:" + env.get("PATH", "")
    return subprocess.Popen([EXECUTOR_BINARY], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def check_workspace(workspace, expected_files, initial_sha):
    """Independent verdict: everything green, tests untouched since initial
    commit, and both task-specific files actually changed."""
    go = os.environ.get("TEMPORALITY_GO_BINARY", "go")
    details = {}
    test = run_tool([go, "test", "./..."], workspace, timeout=300)
    details["go_test_green"] = test["exit_code"] == 0
    details["go_test_stderr"] = snippet(test.get("stderr", "") or test.get("stdout", ""), 300)
    changed = run_tool(["git", "diff", "--name-only", initial_sha], workspace)
    files = [line for line in (changed.get("stdout") or "").splitlines() if line.strip()]
    details["changed_files"] = files
    details["tests_untouched"] = not any(name.endswith("_test.go") for name in files)
    details["bug_files_fixed"] = all(name in files for name in expected_files)
    details["ok"] = details["go_test_green"] and details["tests_untouched"] and details["bug_files_fixed"]
    return details["ok"], details


# ---------------------------------------------------------------- one episode run


def run_loop(args, base, boot, world_id, database, label, expected_files, initial_sha, workspace, world_memory_mode="all"):
    """The cognition loop shared by all arms; returns metrics including the
    pivot lifecycle counters (born/confirmed/refuted, focus changes, memory
    reuse, refuted-path revisits) plus a per-step timeline for post-hoc
    useful/harmful-memory analysis."""
    metrics = {
        "phase": label, "episode_id": boot["episode_id"], "workspace": workspace,
        "world_memory_mode": world_memory_mode,
        "steps": 0, "model_calls": 0, "retries": 0,
        "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
        "affordance_requests": 0, "read_file": 0, "list_files": 0, "inspect_repository": 0,
        "run_tests": 0, "writes": 0, "git_reads": 0,
        "repeated_read_paths": 0, "world_memory_items": [], "procedure_items": [],
        "prior_episode_refs": 0,
        # pivot lifecycle metrics
        "claims_born_candidate": 0, "claims_born_supported": 0, "claims_confirmed": 0,
        "claims_refuted": 0, "claims_superseded": 0, "focus_changes": 0,
        "refuted_revisits": 0, "claim_ops_on_memory": 0, "claims_echoing_memory": 0,
        "memory_claim_refs_seen": 0, "memory_delivered": False,
        "world_memory_states_last": {}, "failed_steps": 0, "degenerate": False,
    }
    executor = spawn_executor(boot["episode_id"], world_id, database)
    definitions = read_affordances(base)
    current_frame = boot["frame_id"]
    answer = None
    read_paths = {}
    memory_claim_ids = set()   # prior-episode claims delivered via world_memory
    memory_propositions = []   # [(state, proposition)] delivered in PRIOR steps
    claim_props = {}           # claim_id -> proposition (this episode)
    seen_claims = set()
    refuted_paths = []         # [(path, visible_from_step)] step 0 = prior episode
    timeline = []
    started = time.time()
    try:
        for step in range(1, args.max_steps + 1):
            say(f"  [{label}] step {step}/{args.max_steps} frame={current_frame[:8]}…")
            result = {}
            status = 0
            for attempt in range(1, 7):
                status, result = request_status("POST", "/v1/model-step", {
                    "frame_id": current_frame,
                    "objective_id": boot["objective_id"],
                    "budget_tokens": args.budget_tokens,
                    "definitions": definitions,
                    "world_id": world_id,
                }, base=base, timeout=300)
                if status == 201 or status in (400, 404) or attempt == 6:
                    break
                metrics["retries"] += 1
                say(f"  [{label}] retry {attempt}/6 http {status}: {str(result.get('error'))[:140]}")
                time.sleep(min(2 * attempt, 10))
            if status in (502, 504, 503):
                # Transient provider failures (empty emissions, gateway flake):
                # nothing was committed, the frame is unchanged — consume the
                # step and try again rather than aborting the phase.
                metrics["failed_steps"] = metrics.get("failed_steps", 0) + 1
                say(f"  [{label}] model-step stayed failed (http {status}); continuing")
                continue
            if status != 201:
                say(f"  [{label}] model-step failed: {status} {json.dumps(result, ensure_ascii=False)[:300]}")
                break
            metrics["model_calls"] += 1
            metrics["steps"] = step
            usage = result.get("model_usage") or {}
            metrics["prompt_tokens"] += usage.get("prompt_tokens") or 0
            metrics["completion_tokens"] += usage.get("completion_tokens") or 0
            metrics["total_tokens"] += usage.get("total_tokens") or 0
            step_entry = {"step": step, "reads": [], "actions": [], "born": [], "ops": [], "memory_states": {}}
            packet = result.get("render_packet") or {}
            states_now = {}
            for section in packet.get("sections") or []:
                if section.get("kind") == "world_memory":
                    metrics["world_memory_items"].append(len(section.get("items") or []))
                    for item in section.get("items") or []:
                        state = item.get("state") or "unknown"
                        states_now[state] = states_now.get(state, 0) + 1
                        if world_memory_mode == "confirmed" and state != "confirmed":
                            sys.exit(
                                f"[{label}] MODE VIOLATION: world_memory served state={state!r} "
                                f"in confirmed mode — arm C would silently run as D; aborting"
                            )
                        ref = item.get("ref") or ""
                        if ref.startswith("claim:"):
                            memory_claim_ids.add(ref.split(":", 1)[1])
                            metrics["memory_claim_refs_seen"] += 1
                        if state == "refuted":
                            for path in path_refs(item.get("proposition")):
                                refuted_paths.append((path, 0))  # prior episode: visible from step 0
                        memory_propositions.append((state, item.get("proposition")))
                    metrics["memory_delivered"] = metrics["memory_delivered"] or bool(section.get("items"))
                if section.get("kind") == "procedures":
                    metrics["procedure_items"].append(len(section.get("items") or []))
            if states_now:
                metrics["world_memory_states_last"] = states_now
                step_entry["memory_states"] = states_now
            emission = result.get("emission") or {}
            actions = emission.get("actions") or []
            if not actions:
                say(f"  [{label}] actions: none")
            for action in actions:
                metrics["affordance_requests"] += 1
                name = action["affordance"]
                args_json = action.get("args") or {}
                say(f"  [{label}] action {name} {json.dumps(args_json, ensure_ascii=False)[:110]}")
                step_entry["actions"].append({"name": name, "args": args_json})
                if name == "read_file":
                    metrics["read_file"] += 1
                    path = str(args_json.get("path") or "?").lstrip("./")
                    read_paths[path] = read_paths.get(path, 0) + 1
                    step_entry["reads"].append(path)
                    revisited = {
                        mentioned for mentioned, visible_from in refuted_paths
                        if step > visible_from and paths_match(path, mentioned)
                    }
                    if revisited:
                        metrics["refuted_revisits"] += len(revisited)
                        say(f"  [{label}] refuted-path revisit: {path} (matched {sorted(revisited)})")
                elif name == "run_tests":
                    metrics["run_tests"] += 1
                elif name in ("write_file", "patch_file", "create_file", "delete_file", "move_file"):
                    metrics["writes"] += 1
                elif name == "list_files":
                    metrics["list_files"] += 1
                elif name == "inspect_repository":
                    metrics["inspect_repository"] += 1
                elif name in ("git_status", "git_log"):
                    metrics["git_reads"] += 1
            # --- lifecycle events + claims from the committed step
            step_data = result.get("step") or {}
            step_claims = step_data.get("claims") or []
            born_ids = set()
            for claim in step_claims:
                claim_id = claim.get("claim_id")
                claim_props[claim_id] = claim.get("proposition") or ""
                if claim_id not in seen_claims:
                    born_ids.add(claim_id)
            for event in step_data.get("events") or []:
                event_type = event.get("type")
                payload = event.get("payload") or {}
                claim_id = payload.get("claim_id")
                if event_type == "claim.candidate":
                    metrics["claims_born_candidate"] += 1
                    step_entry["born"].append({"id": claim_id, "status": "candidate"})
                elif event_type == "claim.supported":
                    if claim_id in born_ids:
                        metrics["claims_born_supported"] += 1
                        step_entry["born"].append({"id": claim_id, "status": "supported"})
                    else:
                        metrics["claims_confirmed"] += 1
                        step_entry["ops"].append({"op": "confirm", "id": claim_id})
                elif event_type == "claim.refuted":
                    metrics["claims_refuted"] += 1
                    step_entry["ops"].append({"op": "refute", "id": claim_id})
                    proposition = claim_props.get(claim_id, "")
                    for path in path_refs(proposition):
                        refuted_paths.append((path, step))
                elif event_type == "claim.superseded":
                    metrics["claims_superseded"] += 1
                    step_entry["ops"].append({"op": "supersede", "id": claim_id})
                elif event_type == "focus.changed":
                    metrics["focus_changes"] += 1
            seen_claims |= born_ids
            # --- cross-episode memory use: ops targeting prior-episode claims
            for op in emission.get("claim_ops") or []:
                ref = op.get("claim") or ""
                if ref.startswith("claim:") and ref.split(":", 1)[1] in memory_claim_ids:
                    metrics["claim_ops_on_memory"] += 1
                    say(f"  [{label}] claim op on prior memory: {op.get('op')} {ref[:20]}…")
            # --- heuristic: new claims echoing delivered prior knowledge
            # (token overlap ≥ 0.6 with any world_memory proposition the model
            # has seen so far this episode, including this step's render)
            for claim in emission.get("claims") or []:
                proposition = claim.get("proposition") or ""
                if not proposition:
                    continue
                if any(prior and token_overlap(proposition, prior) >= 0.6 for _, prior in memory_propositions):
                    metrics["claims_echoing_memory"] += 1
            timeline.append(step_entry)
            for execution in step_data.get("executions") or []:
                final = wait_execution(execution["execution_id"], base)
                mark = "ok" if final.get("status") == "completed" else "FAILED"
                say(f"  [{label}] exec {execution['affordance_id']} {mark}")
                if final.get("status") != "completed":
                    error = final.get("error") or {}
                    diagnostics = error.get("diagnostics") or {}
                    tail = diagnostics.get("stderr") or error.get("message") or ""
                    if tail:
                        say(f"  [{label}] stderr {snippet(tail, 160)}")
            if step_data.get("executions"):
                request("POST", "/v1/projections/regions/rebuild", {"episode_id": boot["episode_id"], "branch_id": boot["branch_id"]}, base=base)
            if emission.get("completion"):
                answer = emission["completion"]
                say(f"  [{label}] completion: {snippet(answer, 240)}")
                current_frame = step_data["frame"]["frame_id"]
                break
            current_frame = step_data["frame"]["frame_id"]
            # --- degenerate guardrail: run_tests-only death spiral (no reads,
            # no writes — nothing is discovered, nothing is fixed, the loop
            # just burns ~70s/step until max_steps). Such an arm A poisons the
            # whole quadruple: durable memory + an unfixed fixture for task 2.
            exploration = (metrics["read_file"] + metrics["list_files"]
                           + metrics["inspect_repository"] + metrics["writes"])
            if step >= args.degenerate_after and exploration == 0 and metrics["run_tests"] >= step - 2:
                metrics["degenerate"] = True
                say(f"  [{label}] DEGENERATE: {metrics['run_tests']}x run_tests, zero reads/writes by step {step} — aborting episode early")
                break
    finally:
        stop(executor)
    metrics["repeated_read_paths"] = sum(count - 1 for count in read_paths.values() if count > 1)
    metrics["distinct_read_paths"] = len(read_paths)
    metrics["finished"] = answer is not None
    metrics["answer"] = answer or "(no completion within the step budget)"
    metrics["seconds"] = round(time.time() - started, 1)
    ok, details = check_workspace(workspace, expected_files, initial_sha)
    metrics["success"] = ok
    metrics["checker"] = details
    metrics["timeline"] = timeline
    return metrics


def run_fresh(args, base, database, workspace, world_id, label, expected_files, initial_sha, objective_text, world_memory_mode="all"):
    """Fresh substrate arm: world + bootstrap + loop (arms A and B)."""
    register_world(base, world_id, workspace)
    status, boot = request_status("POST", "/v1/bootstrap", {
        "world_id": world_id,
        "objective_text": objective_text,
        "budget_tokens": args.budget_tokens,
    }, base=base)
    if status != 201:
        sys.exit(f"[{label}] bootstrap failed: {status} {json.dumps(boot, ensure_ascii=False)[:300]}")
    summary = boot.get("summary") or {}
    say(f"  [{label}] bootstrap observations={summary.get('observations')} claims={summary.get('claims')} entities={summary.get('entities')}")
    metrics = run_loop(args, base, boot, world_id, database, label, expected_files, initial_sha, workspace, world_memory_mode)
    metrics["world_id"] = world_id
    metrics["objective"] = objective_text
    return metrics


def run_warm(args, base, database, workspace, world_id, label, expected_files, initial_sha, objective_text, world_memory_mode="all"):
    """Warm arm: SAME substrate and world, NEW episode, NO bootstrap ingestion,
    NO transcript. Prior knowledge arrives only via render (world_memory,
    procedures, entities); mode confirmed restricts it to facts (arm C)."""
    register_world(base, world_id, workspace, version=2)
    episode_id, branch_id, agent_id, objective_id, frame_id = (uuid.uuid4() for _ in range(5))
    status, payload = request_status("POST", "/v1/objectives", {
        "objective": {"objective_id": str(objective_id), "episode_id": str(episode_id), "text": objective_text, "success_conditions": []},
        "event": {"type": "episode.started", "payload": {"world_id": world_id, "world_version": 2}},
    }, base=base)
    if status != 201:
        sys.exit(f"[{label}] objective creation failed: {status} {payload}")
    status, payload = request_status("POST", "/v1/frames", {
        "frame": {
            "frame_id": str(frame_id), "agent_id": str(agent_id), "episode_id": str(episode_id),
            "branch_id": str(branch_id), "objective_id": str(objective_id),
            "focus": {"type": "query", "query": objective_text}, "working_set": [],
            "mode": "explore",
            "attention": {"policy": "balanced", "deliberate": True, "ambient": True, "max_candidates": 32},
            "budget": {"tokens": args.budget_tokens},
        },
        "event": {"type": "frame.created", "payload": {}},
    }, base=base)
    if status != 201:
        sys.exit(f"[{label}] frame creation failed: {status} {payload}")
    boot = {"episode_id": str(episode_id), "branch_id": str(branch_id), "objective_id": str(objective_id), "frame_id": str(frame_id)}
    metrics = run_loop(args, base, boot, world_id, database, label, expected_files, initial_sha, workspace, world_memory_mode)
    metrics["world_id"] = world_id
    metrics["objective"] = objective_text
    return metrics


def git_restore(workspace, sha):
    """Hard-reset an arm workspace between retry attempts so a degenerate run
    cannot poison the next attempt with its half-written state."""
    subprocess.run(["git", "-C", workspace, "reset", "-q", "--hard", sha], check=True)
    subprocess.run(["git", "-C", workspace, "clean", "-qfd"], check=True)


def git_commit(workspace, message):
    """Commit everything (possibly nothing: a failed agent phase must not abort
    the experiment — the world simply moves on without its fixes)."""
    subprocess.run(["git", "-C", workspace, "add", "-A"], check=True)
    subprocess.run(["git", "-C", workspace, "-c", "user.email=bench@temporality.dev", "-c", "user.name=Benchmark", "commit", "-q", "-m", message], capture_output=True)


def git_head(workspace):
    finished = subprocess.run(["git", "-C", workspace, "rev-parse", "HEAD"], capture_output=True, text=True, check=True)
    return finished.stdout.strip()


def run_arm_with_retries(label, attempt_fn, attempts, retry_on_failure):
    """Serial retry loop for one arm. A degenerate trajectory (run_tests death
    spiral) is retried for every arm; a failed checker verdict is retried only
    for the substrate builder A — its memory and fixture state seed every other
    arm, while a failed task-2 arm is a legitimate measurement, not noise.
    Each attempt_fn must restore clean state itself (DB + workspace)."""
    metrics = None
    for attempt in range(1, attempts + 1):
        metrics = attempt_fn(attempt)
        metrics["attempt"] = attempt
        if attempt >= attempts:
            break
        if metrics.get("degenerate"):
            say(f"  [{label}] attempt {attempt}/{attempts} degenerate — restoring clean state, retrying")
            continue
        if retry_on_failure and not metrics["success"]:
            say(f"  [{label}] attempt {attempt}/{attempts} checker failed — restoring clean state, retrying")
            continue
        break
    return metrics


def say_arm_result(run):
    say(f"  [{run['phase']}] attempt={run.get('attempt', 1)} success={run['success']} degenerate={run.get('degenerate', False)} "
        f"tokens={run['total_tokens']} steps={run['steps']} reads={run['read_file']} revisits={run.get('refuted_revisits', 0)} time={run['seconds']}s")


# ---------------------------------------------------------------- memory instrumentation


def world_memory_snapshot(database, world_id):
    """Post-hoc view of the durable memory the warm agent could draw on."""
    claims_total = psql(database, f"SELECT count(*) FROM claims WHERE claim_id IN (SELECT ce.claim_id FROM claim_evidence ce JOIN events ev ON ev.event_id=ce.evidence_event WHERE ev.payload->>'world_id'='{world_id}' UNION SELECT c.claim_id FROM claims c JOIN events ev ON ev.event_id=c.created_event WHERE ev.episode_id IN (SELECT DISTINCT episode_id FROM events WHERE payload->>'world_id'='{world_id}' AND episode_id IS NOT NULL))")
    claims_live = psql(database, f"SELECT count(*) FROM claims WHERE status IN ('candidate','supported') AND claim_id IN (SELECT ce.claim_id FROM claim_evidence ce JOIN events ev ON ev.event_id=ce.evidence_event WHERE ev.payload->>'world_id'='{world_id}' UNION SELECT c.claim_id FROM claims c JOIN events ev ON ev.event_id=c.created_event WHERE ev.episode_id IN (SELECT DISTINCT episode_id FROM events WHERE payload->>'world_id'='{world_id}' AND episode_id IS NOT NULL))")
    episodes = psql(database, f"SELECT count(DISTINCT episode_id) FROM events WHERE payload->>'world_id'='{world_id}' AND episode_id IS NOT NULL")
    procedures = psql(database, f"SELECT count(*) FROM procedures WHERE episode_id IN (SELECT DISTINCT episode_id FROM events WHERE payload->>'world_id'='{world_id}' AND episode_id IS NOT NULL)")
    return {"claims_total": int(claims_total or 0), "claims_live": int(claims_live or 0), "episodes": int(episodes or 0), "procedures": int(procedures or 0)}


# ---------------------------------------------------------------- experiment


def run_pair(args, pair_index, port):
    head(f"Pair {pair_index}: fixture scale={args.scale}")
    parent = tempfile.mkdtemp(prefix=f"temporality-pivot-p{pair_index}-")
    workspace = create_fixture(parent, args.scale)
    initial_sha = git_head(workspace)
    task2 = list(TASK2_FILES)

    # --- Arm A: fresh substrate, task 1. Builds the durable world memory.
    db_a = f"frp_pivot_p{pair_index}_a"

    def attempt_a(attempt):
        reset_database(db_a)
        git_restore(workspace, initial_sha)
        world_id = "pivot-" + uuid.uuid4().hex[:10]
        runtime, base = spawn_runtime(port, db_a)
        try:
            say(f"  [A-fresh#{attempt}] database={db_a} world={world_id}")
            metrics = run_fresh(args, base, db_a, workspace, world_id, "A-fresh",
                                ["internal/series/series.go", "internal/store/store.go"], initial_sha, OBJECTIVE)
            # Rebuild procedures so the warm arms can reuse episode A's experience.
            request("POST", "/v1/projections/procedures/rebuild", {"episode_id": metrics["episode_id"], "minimum_evidence": 1}, base=base)
            metrics["world_id"] = world_id
            metrics["durable_memory"] = world_memory_snapshot(db_a, world_id)
            say(f"  [A-fresh] durable memory: {json.dumps(metrics['durable_memory'])}")
            return metrics
        finally:
            stop(runtime)

    metrics_a = run_arm_with_retries("A-fresh", attempt_a, args.attempts, retry_on_failure=True)
    say_arm_result(metrics_a)
    world_id = metrics_a["world_id"]

    if args.stop_after_a:
        say(f"\n  [A-fresh verdict] success={metrics_a['success']} finished={metrics_a['finished']} run_tests={metrics_a['run_tests']} writes={metrics_a['writes']}")
        if not metrics_a["success"]:
            say(f"  [A-fresh checker]: {json.dumps(metrics_a['checker'], ensure_ascii=False)}")
        if not args.keep:
            shutil.rmtree(parent, ignore_errors=True)
        return {"A-fresh": metrics_a}

    if not metrics_a["success"]:
        # A is the substrate builder: task-2 arms on an unfixed fixture would
        # measure noise (4 bugs instead of 2), so the pair is aborted honestly.
        say(f"  [pair {pair_index}] ABORT: A-fresh unsuccessful after {metrics_a.get('attempt', 1)} attempt(s) — skipping task-2 arms")
        if not args.keep:
            shutil.rmtree(parent, ignore_errors=True)
        return {"A-fresh": metrics_a, "aborted": True}

    # Freeze task 1's result and introduce task 2's defects.
    git_commit(workspace, "task1: agent fixes")
    apply_delta(workspace)
    git_commit(workspace, "task2: introduce new defects")
    # Checker baseline for task-2 arms: the task-2 commit itself. Comparing
    # against the pre-task-1 sha would always flag the delta's own new test
    # file (internal/util/parse_test.go) as a touched test.
    task2_sha = git_head(workspace)
    say(f"  [pair] task-2 delta applied: {', '.join(TASK2_FILES)}")

    # Each task-2 arm gets a byte-identical workspace and its own substrate:
    # B a fresh DB, C and D clones of EXACTLY the post-A memory (so the warm
    # arms cannot see each other's runs).
    workspace_b = workspace + "-b"
    workspace_c = workspace + "-c"
    workspace_d = workspace + "-d"
    shutil.copytree(workspace, workspace_b)
    shutil.copytree(workspace, workspace_c)
    shutil.copytree(workspace, workspace_d)
    db_b = f"frp_pivot_p{pair_index}_b"
    db_c = f"frp_pivot_p{pair_index}_c"
    db_d = f"frp_pivot_p{pair_index}_d"
    port_b, port_c, port_d = port, port + 1, port + 2

    def arm_b(attempt):
        reset_database(db_b)
        git_restore(workspace_b, task2_sha)
        world_b = "pivot-" + uuid.uuid4().hex[:10]
        runtime_, base_ = spawn_runtime(port_b, db_b)
        try:
            say(f"  [B-cold#{attempt}] cold control substrate={db_b} world={world_b}")
            return run_fresh(args, base_, db_b, workspace_b, world_b, "B-cold", task2, task2_sha, OBJECTIVE)
        finally:
            stop(runtime_)

    def arm_c(attempt):
        clone_database(db_a, db_c)
        git_restore(workspace_c, task2_sha)
        runtime_, base_ = spawn_runtime(port_c, db_c, extra_env={"TEMPORALITY_WORLD_MEMORY_MODE": "confirmed"})
        try:
            say(f"  [C-warm-facts#{attempt}] substrate={db_c} world={world_id} (mode=confirmed)")
            return run_warm(args, base_, db_c, workspace_c, world_id, "C-warm-facts", task2, task2_sha, OBJECTIVE, world_memory_mode="confirmed")
        finally:
            stop(runtime_)

    def arm_d(attempt):
        clone_database(db_a, db_d)
        git_restore(workspace_d, task2_sha)
        runtime_, base_ = spawn_runtime(port_d, db_d)
        try:
            say(f"  [D-warm-inv#{attempt}] substrate={db_d} world={world_id} (mode=all)")
            return run_warm(args, base_, db_d, workspace_d, world_id, "D-warm-inv", task2, task2_sha, OBJECTIVE, world_memory_mode="all")
        finally:
            stop(runtime_)

    def run_arm_b():
        metrics = run_arm_with_retries("B-cold", arm_b, args.attempts, retry_on_failure=False)
        say_arm_result(metrics)
        return metrics

    def run_arm_c():
        metrics = run_arm_with_retries("C-warm-facts", arm_c, args.attempts, retry_on_failure=False)
        metrics["durable_memory"] = world_memory_snapshot(db_c, world_id)
        say_arm_result(metrics)
        return metrics

    def run_arm_d():
        metrics = run_arm_with_retries("D-warm-inv", arm_d, args.attempts, retry_on_failure=False)
        metrics["durable_memory"] = world_memory_snapshot(db_d, world_id)
        say_arm_result(metrics)
        return metrics

    # Task-2 arms are fully isolated (own DB, own workspace copy, own port), so
    # they run concurrently: a pair costs ~A + one arm instead of A + three.
    # Submission order alternates across pairs to spread provider drift; in
    # --serial-arms mode it is the actual execution order.
    jobs = [("B-cold", run_arm_b), ("C-warm-facts", run_arm_c), ("D-warm-inv", run_arm_d)]
    reverse = (pair_index % 2 == 1) if args.order == "bcd" else (pair_index % 2 == 0)
    if reverse:
        jobs.reverse()
    if args.serial_arms:
        metrics = {label: run_arm() for label, run_arm in jobs}
    else:
        with ThreadPoolExecutor(max_workers=3) as pool:
            futures = {label: pool.submit(run_arm) for label, run_arm in jobs}
            metrics = {label: future.result() for label, future in futures.items()}
    metrics["A-fresh"] = metrics_a

    if not args.keep:
        shutil.rmtree(parent, ignore_errors=True)
    return metrics


def median_or_none(values):
    values = [value for value in values if value is not None]
    return statistics.median(values) if values else None


ARM_LABELS = ("A-fresh", "B-cold", "C-warm-facts", "D-warm-inv")


def final_report(pairs, args, env):
    head("Results")
    rows = []
    for index, pair in enumerate(pairs, start=1):
        for label in ARM_LABELS:
            run = pair.get(label)
            if not run:
                continue
            rows.append({
                "pair": index, "phase": label, "success": run["success"], "steps": run["steps"],
                "total_tokens": run["total_tokens"], "prompt_tokens": run["prompt_tokens"],
                "completion_tokens": run["completion_tokens"], "seconds": run["seconds"],
                "read_file": run["read_file"], "repeated_read_paths": run.get("repeated_read_paths", 0),
                "affordance_requests": run["affordance_requests"], "run_tests": run.get("run_tests", 0),
                "writes": run.get("writes", 0),
                "born_c": run.get("claims_born_candidate", 0), "born_s": run.get("claims_born_supported", 0),
                "confirmed": run.get("claims_confirmed", 0), "refuted": run.get("claims_refuted", 0),
                "focus_chg": run.get("focus_changes", 0),
                "revisits": run.get("refuted_revisits", 0),
                "ops_on_mem": run.get("claim_ops_on_memory", 0),
                "echoes": run.get("claims_echoing_memory", 0),
                "mem_states": "+".join(f"{state[:4]}:{count}" for state, count in sorted((run.get("world_memory_states_last") or {}).items())) or "-",
                "degen": run.get("degenerate", False), "att": run.get("attempt", 1),
            })
    columns = ["pair", "phase", "success", "steps", "total_tokens", "seconds", "read_file", "revisits", "refuted", "ops_on_mem", "echoes", "mem_states", "degen", "att"]
    widths = {name: max(len(name), *(len(str(row.get(name))) for row in rows)) if rows else len(name) for name in columns}
    say("  ".join(name.ljust(widths[name]) for name in columns))
    for row in rows:
        say("  ".join(str(row.get(name)).ljust(widths[name]) for name in columns))

    def runs(label):
        return [pair[label] for pair in pairs if pair.get(label)]

    def summary(label):
        arm = runs(label)
        if not arm:
            return None
        return {
            "runs": len(arm),
            "success_rate": sum(1 for run in arm if run["success"]) / len(arm),
            "degenerate_rate": sum(1 for run in arm if run.get("degenerate")) / len(arm),
            "median_total_tokens": median_or_none([run["total_tokens"] for run in arm]),
            "median_prompt_tokens": median_or_none([run["prompt_tokens"] for run in arm]),
            "median_completion_tokens": median_or_none([run["completion_tokens"] for run in arm]),
            "median_seconds": median_or_none([run["seconds"] for run in arm]),
            "median_steps": median_or_none([run["steps"] for run in arm]),
            "median_read_file": median_or_none([run["read_file"] for run in arm]),
            "median_repeated_read_paths": median_or_none([run.get("repeated_read_paths", 0) for run in arm]),
            "median_run_tests": median_or_none([run.get("run_tests", 0) for run in arm]),
            "median_refuted_revisits": median_or_none([run.get("refuted_revisits", 0) for run in arm]),
            "median_claim_ops_on_memory": median_or_none([run.get("claim_ops_on_memory", 0) for run in arm]),
            "median_claims_echoing_memory": median_or_none([run.get("claims_echoing_memory", 0) for run in arm]),
        }

    overall = {label: summary(label) for label in ARM_LABELS}
    overall["pairs_aborted"] = sum(1 for pair in pairs if pair.get("aborted"))

    def ratio(numerator_arm, denominator_arm, field="median_total_tokens"):
        left, right = overall.get(numerator_arm), overall.get(denominator_arm)
        if not left or not right:
            return None
        if not left.get(field) or not right.get(field):
            return None
        return round(left[field] / right[field], 3)

    comparisons = {
        # pivot TZ §16: the MAIN comparison — value of the investigation story
        # (D) over mere facts (C).
        "D_over_C_tokens": ratio("D-warm-inv", "C-warm-facts"),
        "D_over_C_steps": ratio("D-warm-inv", "C-warm-facts", "median_steps"),
        "D_over_C_reads": ratio("D-warm-inv", "C-warm-facts", "median_read_file"),
        "D_over_C_revisits": ratio("D-warm-inv", "C-warm-facts", "median_refuted_revisits"),
        # warm-vs-cold gate inherited from the pre-pivot benchmark TZ.
        "D_over_B_tokens": ratio("D-warm-inv", "B-cold"),
        "D_over_B_gate_0.7": (lambda tokens: tokens is not None and tokens <= 0.7)(ratio("D-warm-inv", "B-cold")),
        "C_over_B_tokens": ratio("C-warm-facts", "B-cold"),
    }
    overall["comparisons"] = comparisons
    say(json.dumps(overall, indent=2, ensure_ascii=False))
    return {"rows": rows, "summary": overall, "objective": OBJECTIVE, "model": env.get("TEMPORALITY_MODEL_ID"), "args": vars(args)}


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--pairs", type=int, default=3, help="independent A/B/C/D quadruples (default 3)")
    parser.add_argument("--scale", type=int, default=8, help="decoy packages in the fixture (default 8)")
    parser.add_argument("--max-steps", type=int, default=12, help="model step budget per episode (default 12)")
    parser.add_argument("--budget-tokens", type=int, default=16000, help="render budget (default 16000)")
    parser.add_argument("--port", type=int, default=18088, help="local runtime port; task-2 arms use port..port+2 (default 18088)")
    parser.add_argument("--degenerate-after", type=int, default=6, help="abort an episode early if by this step it has only run_tests and zero reads/writes (default 6)")
    parser.add_argument("--attempts", type=int, default=2, help="max attempts per arm with clean-state restore between them (default 2)")
    parser.add_argument("--serial-arms", action="store_true", help="run task-2 arms sequentially instead of in parallel (provider rate-limit fallback)")
    parser.add_argument("--stop-after-a", action="store_true", help="validate Episode A stability only (pivot TZ §22 Этап 6 precondition)")
    parser.add_argument("--order", choices=["bcd", "dcb"], default="bcd", help="task-2 arm order (alternate across independent invocations to spread drift)")
    parser.add_argument("--keep", action="store_true", help="keep workspaces and databases")
    parser.add_argument("--dump", default=None, help="metrics JSON path (default benchmarks/memory-benchmark-<ts>.json)")
    args = parser.parse_args()
    global BASE, PROJECT_ROOT
    PROJECT_ROOT = os.getcwd()

    env = env_or_fail()
    if shutil.which("git") is None:
        sys.exit("git is required")
    if not os.path.exists(RUNTIME_BINARY) or not os.path.exists(EXECUTOR_BINARY):
        sys.exit(f"binaries not found: {RUNTIME_BINARY} / {EXECUTOR_BINARY}\nrun: make build")
    if compose_executor_running():
        sys.exit("compose executor is running and will steal executions: run `docker compose stop executor` first")
    go = os.environ.get("TEMPORALITY_GO_BINARY", "go")
    if shutil.which(go) is None and os.path.exists("/usr/local/go/bin/go"):
        os.environ["PATH"] = "/usr/local/go/bin:" + os.environ.get("PATH", "")
    if shutil.which(go) is None:
        sys.exit("go toolchain not found")

    head("0. Preconditions")
    say(f"model     : {env['TEMPORALITY_MODEL_ID']} (max_output_tokens={env.get('TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS')})")
    say(f"pairs     : {args.pairs} x (A fresh -> B cold + C warm-facts + D warm-investigation{' | serial' if args.serial_arms else ' | parallel'}), scale={args.scale}, max_steps={args.max_steps}, attempts={args.attempts}")

    dump = args.dump
    if dump is None:
        os.makedirs("benchmarks", exist_ok=True)
        dump = time.strftime("benchmarks/memory-benchmark-%Y%m%d-%H%M%S.json")

    pairs = []
    for index in range(1, args.pairs + 1):
        pairs.append(run_pair(args, index, args.port))
        # Incremental dump: an interrupted run must not lose its metrics (the
        # first 42-minute attempt left nothing but databases behind).
        with open(dump, "w") as handle:
            json.dump({"pairs": pairs, "partial": True}, handle, ensure_ascii=False, indent=2)
        say(f"  partial metrics after pair {index}: {dump}")

    report = final_report(pairs, args, env)
    with open(dump, "w") as handle:
        json.dump({"pairs": pairs, "report": report}, handle, ensure_ascii=False, indent=2)
    say(f"\nmetrics   : {dump}")


if __name__ == "__main__":
    main()
