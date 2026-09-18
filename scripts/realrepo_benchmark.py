#!/usr/bin/env python3
"""FRP Real-Repository Memory Experiment (TZ: «Эксперимент Temporality — память
агента на реальном репозитории»).

Fixture: blevesearch/bleve v2.6.1 (048761396d42661336db8caa0bed1e98cf2aeaa6),
804 files, pure Go, full suite green offline (~90s). Bugs are committed into the
workspace (a plain `git status` must NOT reveal the injection points), symptoms
are failing existing tests; the agent is never told the bug location. Tests are
off-limits for the agent; the independent checker is `go test ./...` green plus
tests untouched plus the expected source file actually changed.

Tasks (scripts/realrepo/patches/*.patch, all round-trip validated):

  T1 local        bug1: inverted match cost in LevenshteinDistance
                  symptom: TestLevenshteinDistance (search)
  T2 cross-module bug2: sign-magnitude fixup removed in numeric.Float64ToInt64
                  symptoms: TestSortabledFloat64ToInt64 (numeric) AND
                  TestBytesRead (root package) — cause far from one symptom
  T3 temporal     refactor3 (levenshtein.go -> edit_distance.go, functions
                  renamed EditDistance*) + bug3 (early-exit off-by-one in
                  EditDistanceMaxReuseSlice)
                  symptoms: TestFuzzySearch, TestFuzzyMultiPhraseSearch
                  (search/searcher). Episode-1 knowledge about
                  search/levenshtein.go / LevenshteinDistance is now stale:
                  the honest probe of stale-memory handling.

Chain (warm memory ONLY via the штатный render path, no transcripts):

  E1 = T1 cold on the canonical substrate S1 (mode=all; empty memory anyway)
       -> procedures rebuild -> S1 frozen
  T2: cold (fresh DB) ∥ facts (clone S1, TEMPORALITY_WORLD_MEMORY_MODE=confirmed)
      ∥ inv (clone S1, mode=all)  -> S2 = inv DB after procedures rebuild
  T3: cold (fresh DB) ∥ facts (clone S2, confirmed) ∥ inv (clone S2, all)

Repo state per task is deterministic: BASE + task patches, committed; each arm
gets a byte-identical workspace clone and its own DB; arms run in parallel on
separate ports. Same world_id across the warm chain, state_version = task
index, so the runtime marks prior-world claims stale where relevant.

Preconditions: compose postgres up; compose executor STOPPED; make build;
model credentials in .env. Usage:

  python3 scripts/realrepo_benchmark.py --stop-after-e1     # validate chain seed
  python3 scripts/realrepo_benchmark.py                    # full experiment
  python3 scripts/realrepo_benchmark.py --serial-arms      # provider choking?
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
    DATABASE_URL,
    EXECUTOR_BINARY,
    FRP_AFFORDANCES,
    RUNTIME_BINARY,
    compose_executor_running,
    env_or_fail,
    load_dotenv,
    run_tool,
    say,
    snippet,
    stop,
)

POSTGRES_CONTAINER = os.environ.get("TEMPORALITY_POSTGRES_CONTAINER", "temporality-postgres-1")
PATCH_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "realrepo", "patches")

DEFAULT_SOURCE = os.path.expanduser("~/Projects/temporality-bench/repos/bleve")
BASE_SHA = "048761396d42661336db8caa0bed1e98cf2aeaa6"
WORLD_ID = "bleve-261"

# go test ./... on bleve: ~90-160s cold. 300s process timeout; 128KB reads
# cover every source file; entries cap leaves room for recursive listings.
REAL_LIMITS = {"max_read_bytes": 131072, "max_entries": 4000, "timeout_sec": 300}

TASKS = [
    {
        "id": "T1",
        "patches": ["bug1.patch"],
        "world_version": 1,
        "expected_files": ["search/levenshtein.go"],
        "objective": (
            "В Go-репозитории падает тест TestLevenshteinDistance "
            "(пакет github.com/blevesearch/bleve/v2/search, файл search/levenshtein_test.go). "
            "Найди причину падения и исправь код. Изменять файлы *_test.go запрещено. "
            "Критерий успеха: go test ./... проходит полностью."
        ),
    },
    {
        "id": "T2",
        "patches": ["bug2.patch"],
        "world_version": 2,
        "expected_files": ["numeric/float.go"],
        "objective": (
            "В Go-репозитории падают тесты: TestSortabledFloat64ToInt64 "
            "(пакет github.com/blevesearch/bleve/v2/numeric) и TestBytesRead "
            "(корневой пакет github.com/blevesearch/bleve/v2). "
            "Найди первопричину и исправь код. Изменять файлы *_test.go запрещено. "
            "Критерий успеха: go test ./... проходит полностью."
        ),
    },
    {
        "id": "T3",
        "patches": ["refactor3.patch", "bug3.patch"],
        "world_version": 3,
        "expected_files": ["search/edit_distance.go"],
        "objective": (
            "В Go-репозитории падают тесты TestFuzzySearch и TestFuzzyMultiPhraseSearch "
            "(пакет github.com/blevesearch/bleve/v2/search/searcher). "
            "Найди причину падения и исправь код. Изменять файлы *_test.go запрещено. "
            "Критерий успеха: go test ./... проходит полностью."
        ),
    },
]

# Stale-memory probes for T3: the refactor renamed search/levenshtein.go and
# the LevenshteinDistance* functions; prior-episode knowledge naming them is
# now stale. We count reads of the dead file and propositions naming the dead
# symbol (the test file legitimately keeps the "levenshtein" name).
STALE_FILE = "search/levenshtein.go"
STALE_SYMBOL = "levenshteindistance"

GO_PATH_RE = re.compile(r"[\w./\-]+\.go\b")
WORD_RE = re.compile(r"\w+")


def path_refs(text):
    return {match.group(0).lstrip("./") for match in GO_PATH_RE.finditer(text or "")}


def paths_match(read_path, mentioned):
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
    finished = subprocess.run(
        ["docker", "exec", POSTGRES_CONTAINER, "psql", "-U", "temporality", "-d", database, "-At", "-c", sql],
        capture_output=True, text=True, timeout=30,
    )
    if finished.returncode != 0:
        say(f"psql failed: {finished.stderr.strip()[:200]}")
        return ""
    return finished.stdout.strip()


def reset_database(name):
    # FORCE kills lingering runtime/executor backends from a just-stopped
    # attempt; without it a failed DROP silently keeps the old world version
    # and the next registration dies with "not newer than stored state".
    dropped = subprocess.run(["docker", "exec", POSTGRES_CONTAINER, "psql", "-U", "temporality", "-d", "postgres", "-c", f"DROP DATABASE IF EXISTS {name} WITH (FORCE)"], capture_output=True, timeout=30)
    if dropped.returncode != 0:
        sys.exit(f"drop {name} failed: {dropped.stderr.decode()[:300]}")
    created = subprocess.run(["docker", "exec", POSTGRES_CONTAINER, "createdb", "-U", "temporality", name], capture_output=True, timeout=30)
    if created.returncode != 0:
        sys.exit(f"createdb {name} failed: {created.stderr.decode()[:300]}")


def clone_database(source, target):
    dropped = subprocess.run(["docker", "exec", POSTGRES_CONTAINER, "psql", "-U", "temporality", "-d", "postgres", "-c", f"DROP DATABASE IF EXISTS {target} WITH (FORCE)"], capture_output=True, timeout=30)
    if dropped.returncode != 0:
        sys.exit(f"drop {target} failed: {dropped.stderr.decode()[:300]}")
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
        sys.exit(f"port {port} is already held")
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


def wait_execution(execution_id, base, attempts=3000):
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


def register_world(base, world_id, workspace, version):
    world = {
        "world_id": world_id,
        "state_version": version,
        "resources": [{"id": "repo", "type": "git_repository", "path": workspace}],
        "capabilities": ["filesystem.read", "filesystem.write", "git.read", "process.execute"],
        "limits": REAL_LIMITS,
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


# ---------------------------------------------------------------- fixture


def prepare_workspace(source, task, parent):
    """Byte-identical task state, committed: `git status` must not leak the
    injection points, and the checker has a fixed initial sha to diff against."""
    workspace = os.path.join(parent, f"ws-{task['id'].lower()}-{uuid.uuid4().hex[:6]}")
    finished = subprocess.run(["git", "clone", "--quiet", source, workspace], capture_output=True, text=True)
    if finished.returncode != 0:
        sys.exit(f"clone failed: {finished.stderr[:300]}")
    for command in (
        ["git", "-C", workspace, "checkout", "--quiet", BASE_SHA],
        ["git", "-C", workspace, "reset", "--hard", "-q", BASE_SHA],
        ["git", "-C", workspace, "clean", "-qfd"],
    ):
        subprocess.run(command, check=True, capture_output=True)
    for patch in task["patches"]:
        applied = subprocess.run(["git", "-C", workspace, "apply", os.path.join(PATCH_DIR, patch)], capture_output=True, text=True)
        if applied.returncode != 0:
            sys.exit(f"patch {patch} failed: {applied.stderr[:300]}")
    subprocess.run(["git", "-C", workspace, "add", "-A"], check=True, capture_output=True)
    subprocess.run(["git", "-C", workspace, "-c", "user.email=bench@temporality.dev", "-c", "user.name=Benchmark",
                    "commit", "-q", "-m", "chore: workspace snapshot"], capture_output=True)
    sha = subprocess.run(["git", "-C", workspace, "rev-parse", "HEAD"], capture_output=True, text=True, check=True)
    return workspace, sha.stdout.strip()


def git_restore(workspace, sha):
    subprocess.run(["git", "-C", workspace, "reset", "-q", "--hard", sha], check=True)
    subprocess.run(["git", "-C", workspace, "clean", "-qfd"], check=True)


def check_workspace(workspace, expected_files, initial_sha):
    go = os.environ.get("TEMPORALITY_GO_BINARY", "go")
    details = {}
    test = run_tool([go, "test", "./..."], workspace, timeout=600)
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


def run_loop(args, base, boot, world_id, database, label, task, initial_sha, workspace, world_memory_mode="all"):
    metrics = {
        "phase": label, "task": task["id"], "episode_id": boot["episode_id"], "workspace": workspace,
        "world_memory_mode": world_memory_mode,
        "steps": 0, "model_calls": 0, "retries": 0,
        "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
        "affordance_requests": 0, "read_file": 0, "list_files": 0, "inspect_repository": 0,
        "run_tests": 0, "writes": 0, "git_reads": 0, "other_commands": 0,
        "repeated_read_paths": 0, "world_memory_items": [], "procedure_items": [],
        "claims_born_candidate": 0, "claims_confirmed": 0, "claims_refuted": 0,
        "claims_superseded": 0, "focus_changes": 0,
        "refuted_revisits": 0, "claim_ops_on_memory": 0, "claims_echoing_memory": 0,
        "memory_claim_refs_seen": 0, "memory_delivered": False,
        "world_memory_states_last": {}, "failed_steps": 0, "degenerate": False, "timeout": False, "auth_failed": False,
        "stale_file_reads": 0, "stale_symbol_claims": 0,
    }
    executor = spawn_executor(boot["episode_id"], world_id, database)
    definitions = read_affordances(base)
    current_frame = boot["frame_id"]
    answer = None
    read_paths = {}
    memory_claim_ids = set()
    memory_propositions = []
    claim_props = {}
    seen_claims = set()
    refuted_paths = []
    timeline = []
    started = time.time()
    deadline = started + args.time_limit
    try:
        for step in range(1, args.max_steps + 1):
            if time.time() >= deadline:
                metrics["timeout"] = True
                say(f"  [{label}] TIME LIMIT: {args.time_limit}s reached before step {step} — aborting episode")
                break
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
            error_text = str(result.get("error") or "")
            if status != 201 and ("HTTP 401" in error_text or "Invalid API Key" in error_text or "Unauthorized" in error_text):
                metrics["auth_failed"] = True
                say(f"  [{label}] FATAL: provider rejected credentials (401) — aborting episode")
                break
            if status in (502, 504, 503):
                metrics["failed_steps"] += 1
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
                            sys.exit(f"[{label}] MODE VIOLATION: world_memory served state={state!r} in confirmed mode — aborting")
                        ref = item.get("ref") or ""
                        if ref.startswith("claim:"):
                            memory_claim_ids.add(ref.split(":", 1)[1])
                            metrics["memory_claim_refs_seen"] += 1
                        if state == "refuted":
                            for path in path_refs(item.get("proposition")):
                                refuted_paths.append((path, 0))
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
                    if task["id"] == "T3" and (path == STALE_FILE or path.endswith("/" + STALE_FILE)):
                        metrics["stale_file_reads"] += 1
                        say(f"  [{label}] stale-file read attempt: {path}")
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
                elif name in ("run_command", "reproduce_issue"):
                    metrics["other_commands"] += 1
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
            for op in emission.get("claim_ops") or []:
                ref = op.get("claim") or ""
                if ref.startswith("claim:") and ref.split(":", 1)[1] in memory_claim_ids:
                    metrics["claim_ops_on_memory"] += 1
                    say(f"  [{label}] claim op on prior memory: {op.get('op')} {ref[:20]}…")
            for claim in emission.get("claims") or []:
                proposition = claim.get("proposition") or ""
                if not proposition:
                    continue
                if task["id"] == "T3" and STALE_SYMBOL in proposition.replace(" ", "").lower():
                    metrics["stale_symbol_claims"] += 1
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
    metrics["read_paths"] = read_paths
    metrics["finished"] = answer is not None
    metrics["answer"] = answer or "(no completion within the step budget)"
    metrics["seconds"] = round(time.time() - started, 1)
    # Discovery-work proxy: everything up to and including the step of the LAST
    # write (the fix); verification work is what happens after it.
    last_write_step = 0
    for entry in timeline:
        if any(action["name"] in ("write_file", "patch_file", "create_file", "delete_file", "move_file") for action in entry["actions"]):
            last_write_step = entry["step"]
    metrics["last_write_step"] = last_write_step
    metrics["discovery_actions"] = sum(len(entry["actions"]) for entry in timeline if entry["step"] <= last_write_step)
    metrics["discovery_reads"] = sum(len(entry["reads"]) for entry in timeline if entry["step"] <= last_write_step)
    ok, details = check_workspace(workspace, task["expected_files"], initial_sha)
    metrics["success"] = ok
    metrics["checker"] = details
    metrics["timeline"] = timeline
    return metrics


def run_cold(args, base, database, workspace, world_id, label, task, initial_sha):
    # A fresh substrate sees this world for the FIRST time, and SaveWorld
    # requires the initial registration to be state_version=1 (no rows →
    # version must be 1). The task index lives on in the warm chain's clones.
    register_world(base, world_id, workspace, 1)
    status, boot = request_status("POST", "/v1/bootstrap", {
        "world_id": world_id,
        "objective_text": task["objective"],
        "budget_tokens": args.budget_tokens,
    }, base=base)
    if status != 201:
        sys.exit(f"[{label}] bootstrap failed: {status} {json.dumps(boot, ensure_ascii=False)[:300]}")
    summary = boot.get("summary") or {}
    say(f"  [{label}] bootstrap observations={summary.get('observations')} claims={summary.get('claims')} entities={summary.get('entities')}")
    metrics = run_loop(args, base, boot, world_id, database, label, task, initial_sha, workspace, world_memory_mode="all")
    metrics["world_id"] = world_id
    metrics["objective"] = task["objective"]
    return metrics


def run_warm(args, base, database, workspace, world_id, label, task, initial_sha, world_memory_mode="all"):
    register_world(base, world_id, workspace, task["world_version"])
    episode_id, branch_id, agent_id, objective_id, frame_id = (uuid.uuid4() for _ in range(5))
    status, payload = request_status("POST", "/v1/objectives", {
        "objective": {"objective_id": str(objective_id), "episode_id": str(episode_id), "text": task["objective"], "success_conditions": []},
        "event": {"type": "episode.started", "payload": {"world_id": world_id, "world_version": task["world_version"]}},
    }, base=base)
    if status != 201:
        sys.exit(f"[{label}] objective creation failed: {status} {payload}")
    status, payload = request_status("POST", "/v1/frames", {
        "frame": {
            "frame_id": str(frame_id), "agent_id": str(agent_id), "episode_id": str(episode_id),
            "branch_id": str(branch_id), "objective_id": str(objective_id),
            "focus": {"type": "query", "query": task["objective"]}, "working_set": [],
            "mode": "explore",
            "attention": {"policy": "balanced", "deliberate": True, "ambient": True, "max_candidates": 32},
            "budget": {"tokens": args.budget_tokens},
        },
        "event": {"type": "frame.created", "payload": {}},
    }, base=base)
    if status != 201:
        sys.exit(f"[{label}] frame creation failed: {status} {payload}")
    boot = {"episode_id": str(episode_id), "branch_id": str(branch_id), "objective_id": str(objective_id), "frame_id": str(frame_id)}
    metrics = run_loop(args, base, boot, world_id, database, label, task, initial_sha, workspace, world_memory_mode)
    metrics["world_id"] = world_id
    metrics["objective"] = task["objective"]
    return metrics


def run_arm_with_retries(label, attempt_fn, attempts, retry_on_failure=False):
    metrics = None
    for attempt in range(1, attempts + 1):
        metrics = attempt_fn(attempt)
        metrics["attempt"] = attempt
        if metrics.get("auth_failed"):
            say(f"  [{label}] attempt {attempt}/{attempts} aborted: invalid credentials — not retrying")
            break
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


def world_memory_snapshot(database, world_id):
    claims_total = psql(database, f"SELECT count(*) FROM claims WHERE claim_id IN (SELECT ce.claim_id FROM claim_evidence ce JOIN events ev ON ev.event_id=ce.evidence_event WHERE ev.payload->>'world_id'='{world_id}' UNION SELECT c.claim_id FROM claims c JOIN events ev ON ev.event_id=c.created_event WHERE ev.episode_id IN (SELECT DISTINCT episode_id FROM events WHERE payload->>'world_id'='{world_id}' AND episode_id IS NOT NULL))")
    claims_live = psql(database, f"SELECT count(*) FROM claims WHERE status IN ('candidate','supported') AND claim_id IN (SELECT ce.claim_id FROM claim_evidence ce JOIN events ev ON ev.event_id=ce.evidence_event WHERE ev.payload->>'world_id'='{world_id}' UNION SELECT c.claim_id FROM claims c JOIN events ev ON ev.event_id=c.created_event WHERE ev.episode_id IN (SELECT DISTINCT episode_id FROM events WHERE payload->>'world_id'='{world_id}' AND episode_id IS NOT NULL))")
    episodes = psql(database, f"SELECT count(DISTINCT episode_id) FROM events WHERE payload->>'world_id'='{world_id}' AND episode_id IS NOT NULL")
    procedures = psql(database, f"SELECT count(*) FROM procedures WHERE episode_id IN (SELECT DISTINCT episode_id FROM events WHERE payload->>'world_id'='{world_id}' AND episode_id IS NOT NULL)")
    return {"claims_total": int(claims_total or 0), "claims_live": int(claims_live or 0), "episodes": int(episodes or 0), "procedures": int(procedures or 0)}


# ---------------------------------------------------------------- experiment


def run_task(args, task, source_db, port, parent):
    """One task: three arms in parallel (cold: fresh DB; facts/inv: clones of
    the canonical substrate built by the PREVIOUS task). The inv arm's database
    (after a procedures rebuild) becomes the canonical substrate for the next
    task, so warm memory accumulates along the richest line without leaking
    runs between arms."""
    head(f"{task['id']}: patches={task['patches']} (world {WORLD_ID} v{task['world_version']})")
    workspaces = {}
    initial_shas = {}
    for arm in ("cold", "facts", "inv"):
        workspace, sha = prepare_workspace(args.source, task, parent)
        workspaces[arm] = workspace
        initial_shas[arm] = sha
    db_cold = f"frp_real_{task['id'].lower()}_cold"
    db_facts = f"frp_real_{task['id'].lower()}_facts"
    db_inv = f"frp_real_{task['id'].lower()}_inv"

    def arm_cold(attempt):
        reset_database(db_cold)
        git_restore(workspaces["cold"], initial_shas["cold"])
        runtime_, base_ = spawn_runtime(port, db_cold)
        try:
            say(f"  [cold#{attempt}] fresh substrate={db_cold}")
            metrics = run_cold(args, base_, db_cold, workspaces["cold"], WORLD_ID, f"{task['id']}-cold", task, initial_shas["cold"])
            if source_db is None:
                # Episode-1 seed: the cold run IS the canonical substrate
                # builder, so its experience must be distilled into procedures
                # for every later warm arm.
                request("POST", "/v1/projections/procedures/rebuild", {"episode_id": metrics["episode_id"], "minimum_evidence": 1}, base=base_)
            return metrics
        finally:
            stop(runtime_)

    def arm_facts(attempt):
        if source_db:
            clone_database(source_db, db_facts)
        else:
            reset_database(db_facts)
        git_restore(workspaces["facts"], initial_shas["facts"])
        runtime_, base_ = spawn_runtime(port + 1, db_facts, extra_env={"TEMPORALITY_WORLD_MEMORY_MODE": "confirmed"})
        try:
            say(f"  [facts#{attempt}] substrate={db_facts} clone-of={source_db} (mode=confirmed)")
            return run_warm(args, base_, db_facts, workspaces["facts"], WORLD_ID, f"{task['id']}-facts", task, initial_shas["facts"], world_memory_mode="confirmed")
        finally:
            stop(runtime_)

    def arm_inv(attempt):
        if source_db:
            clone_database(source_db, db_inv)
        else:
            reset_database(db_inv)
        git_restore(workspaces["inv"], initial_shas["inv"])
        runtime_, base_ = spawn_runtime(port + 2, db_inv)
        try:
            say(f"  [inv#{attempt}] substrate={db_inv} clone-of={source_db} (mode=all)")
            metrics = run_warm(args, base_, db_inv, workspaces["inv"], WORLD_ID, f"{task['id']}-inv", task, initial_shas["inv"], world_memory_mode="all")
            request("POST", "/v1/projections/procedures/rebuild", {"episode_id": metrics["episode_id"], "minimum_evidence": 1}, base=base_)
            return metrics
        finally:
            stop(runtime_)

    def run_arm(label, fn, retry_on_failure):
        try:
            metrics = run_arm_with_retries(label, fn, args.attempts, retry_on_failure=retry_on_failure)
        except SystemExit as fatal:
            # A fatal plumbing error in ONE arm must not kill sibling arms.
            say(f"  [{label}] FATAL: {fatal}")
            metrics = {"phase": label, "task": task["id"], "success": False, "fatal": str(fatal),
                       "steps": 0, "total_tokens": 0, "prompt_tokens": 0, "completion_tokens": 0, "seconds": 0, "read_file": 0,
                       "affordance_requests": 0, "run_tests": 0, "writes": 0, "repeated_read_paths": 0,
                       "claims_born_candidate": 0, "claims_confirmed": 0, "claims_refuted": 0,
                       "claim_ops_on_memory": 0, "claims_echoing_memory": 0, "memory_claim_refs_seen": 0,
                       "stale_file_reads": 0, "stale_symbol_claims": 0, "world_memory_states_last": {},
                       "world_memory_items": [], "procedure_items": [], "degenerate": False, "timeout": False}
        metrics["durable_memory"] = world_memory_snapshot(label_to_db[label], WORLD_ID)
        say_arm_result(metrics)
        return metrics

    label_to_db = {f"{task['id']}-cold": db_cold, f"{task['id']}-facts": db_facts, f"{task['id']}-inv": db_inv}
    if source_db is None:
        # Episode 1 (task 1 of the chain): memory is empty, so facts/inv arms
        # would be byte-identical cold runs — pure token burn. Run ONE cold
        # seed episode; its DB becomes the canonical substrate for task 2.
        metrics = {f"{task['id']}-cold": run_arm(f"{task['id']}-cold", arm_cold, True)}
        return metrics, db_cold
    jobs = [
        (f"{task['id']}-cold", lambda: run_arm(f"{task['id']}-cold", arm_cold, False)),
        (f"{task['id']}-facts", lambda: run_arm(f"{task['id']}-facts", arm_facts, False)),
        (f"{task['id']}-inv", lambda: run_arm(f"{task['id']}-inv", arm_inv, True)),
    ]
    jobs = [(label, fn) for label, fn in jobs if label.rsplit("-", 1)[-1] in args.arms]
    if args.serial_arms:
        metrics = {label: run_arm_fn() for label, run_arm_fn in jobs}
    else:
        with ThreadPoolExecutor(max_workers=len(jobs) or 1) as pool:
            futures = {label: pool.submit(run_arm_fn) for label, run_arm_fn in jobs}
            metrics = {label: future.result() for label, future in futures.items()}
    # Canonical substrate for the NEXT task = the inv arm's DB (its memory is
    # the richest line; facts arms are measurements, not builders).
    return metrics, db_inv


ARM_LABELS = ("cold", "facts", "inv")


def final_report(tasks_metrics, args, env):
    head("Results")
    rows = []
    for task_id, arms in tasks_metrics.items():
        for label in ARM_LABELS:
            run = arms.get(f"{task_id}-{label}")
            if not run:
                continue
            rows.append({
                "task": task_id, "phase": label, "success": run["success"], "steps": run["steps"],
                "total_tokens": run["total_tokens"], "prompt_tokens": run["prompt_tokens"],
                "completion_tokens": run["completion_tokens"], "seconds": run["seconds"],
                "read_file": run["read_file"], "distinct_read_paths": run.get("distinct_read_paths", 0),
                "repeated_read_paths": run.get("repeated_read_paths", 0),
                "affordance_requests": run["affordance_requests"], "run_tests": run.get("run_tests", 0),
                "writes": run.get("writes", 0), "discovery_actions": run.get("discovery_actions", 0),
                "born_c": run.get("claims_born_candidate", 0),
                "confirmed": run.get("claims_confirmed", 0), "refuted": run.get("claims_refuted", 0),
                "revisits": run.get("refuted_revisits", 0),
                "ops_on_mem": run.get("claim_ops_on_memory", 0),
                "echoes": run.get("claims_echoing_memory", 0),
                "stale_reads": run.get("stale_file_reads", 0), "stale_claims": run.get("stale_symbol_claims", 0),
                "mem_states": "+".join(f"{state[:4]}:{count}" for state, count in sorted((run.get("world_memory_states_last") or {}).items())) or "-",
                "degen": run.get("degenerate", False), "att": run.get("attempt", 1), "tmo": run.get("timeout", False),
            })
    columns = ["task", "phase", "success", "steps", "total_tokens", "seconds", "read_file", "distinct_read_paths",
               "repeated_read_paths", "run_tests", "writes", "discovery_actions", "refuted", "revisits", "ops_on_mem",
               "echoes", "stale_reads", "stale_claims", "mem_states", "degen", "att", "tmo"]
    widths = {name: max(len(name), *(len(str(row.get(name))) for row in rows)) if rows else len(name) for name in columns}
    say("  ".join(name.ljust(widths[name]) for name in columns))
    for row in rows:
        say("  ".join(str(row.get(name)).ljust(widths[name]) for name in columns))

    overall = {}
    for task_id, arms in tasks_metrics.items():
        task_summary = {}
        for label in ARM_LABELS:
            run = arms.get(f"{task_id}-{label}")
            if not run:
                continue
            task_summary[label] = {
                "success": run["success"],
                "total_tokens": run["total_tokens"],
                "prompt_tokens": run["prompt_tokens"],
                "completion_tokens": run["completion_tokens"],
                "seconds": run["seconds"],
                "steps": run["steps"],
                "read_file": run["read_file"],
                "distinct_read_paths": run.get("distinct_read_paths", 0),
                "repeated_read_paths": run.get("repeated_read_paths", 0),
                "run_tests": run.get("run_tests", 0),
                "discovery_actions": run.get("discovery_actions", 0),
                "memory_items_seen": run.get("memory_claim_refs_seen", 0),
                "durable_memory": run.get("durable_memory"),
            }

        def ratio(arm, base="cold", field="total_tokens"):
            left, right = task_summary.get(arm), task_summary.get(base)
            if not left or not right or not right.get(field):
                return None
            return round(left[field] / right[field], 3)

        task_summary["facts_over_cold_tokens"] = ratio("facts")
        task_summary["inv_over_cold_tokens"] = ratio("inv")
        task_summary["inv_over_facts_tokens"] = ratio("inv", "facts")
        overall[task_id] = task_summary
    say(json.dumps(overall, indent=2, ensure_ascii=False))
    return {"rows": rows, "summary": overall, "model": env.get("TEMPORALITY_MODEL_ID"), "args": vars(args)}


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--source", default=DEFAULT_SOURCE, help=f"local clone of the pinned repo (default {DEFAULT_SOURCE})")
    parser.add_argument("--tasks", default="T1,T2,T3", help="task ids to run (default T1,T2,T3)")
    parser.add_argument("--max-steps", type=int, default=16, help="model step budget per episode (default 16)")
    parser.add_argument("--budget-tokens", type=int, default=16000, help="render budget (default 16000)")
    parser.add_argument("--port", type=int, default=18100, help="runtime port; task arms use port..port+2 (default 18100)")
    parser.add_argument("--degenerate-after", type=int, default=6, help="abort if by this step only run_tests happened (default 6)")
    parser.add_argument("--time-limit", type=int, default=1500, help="wall-clock seconds per episode (default 1500)")
    parser.add_argument("--attempts", type=int, default=2, help="max attempts per arm with clean-state restore (default 2)")
    parser.add_argument("--serial-arms", action="store_true", help="run arms sequentially instead of in parallel")
    parser.add_argument("--arms", default="cold,facts,inv", help="arm subset for comparison tasks (default cold,facts,inv)")
    parser.add_argument("--stop-after-e1", action="store_true", help="run only Episode 1 (T1 cold chain seed) and stop")
    parser.add_argument("--canonical-db", default=None, help="resume: substrate DB built by a previous run's chain seed")
    parser.add_argument("--keep", action="store_true", help="keep workspaces and databases")
    parser.add_argument("--dump", default=None, help="metrics JSON path (default benchmarks/realrepo-<ts>.json)")
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
    if not os.path.isdir(args.source):
        sys.exit(f"source repo not found: {args.source} (clone bleve and checkout {BASE_SHA})")
    head_sha = subprocess.run(["git", "-C", args.source, "rev-parse", "HEAD"], capture_output=True, text=True)
    if head_sha.stdout.strip() != BASE_SHA:
        sys.exit(f"source repo is not at the pinned commit {BASE_SHA}: it is at {head_sha.stdout.strip()[:12]}")
    go = os.environ.get("TEMPORALITY_GO_BINARY", "go")
    if shutil.which(go) is None and os.path.exists("/usr/local/go/bin/go"):
        os.environ["PATH"] = "/usr/local/go/bin:" + os.environ.get("PATH", "")
    if shutil.which(go) is None:
        sys.exit("go toolchain not found")

    head("0. Preconditions")
    say(f"model     : {env['TEMPORALITY_MODEL_ID']} (max_output_tokens={env.get('TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS')})")
    say(f"source    : {args.source} @ {BASE_SHA[:12]}")
    say(f"tasks     : {args.tasks}, max_steps={args.max_steps}, attempts={args.attempts}, time_limit={args.time_limit}s/episode")
    say(f"limits    : {REAL_LIMITS}")

    dump = args.dump
    if dump is None:
        os.makedirs("benchmarks", exist_ok=True)
        dump = time.strftime("benchmarks/realrepo-%Y%m%d-%H%M%S.json")

    selected = [task for task in TASKS if task["id"] in args.tasks.split(",")]
    if not selected:
        sys.exit(f"no tasks matched {args.tasks}")

    parent = tempfile.mkdtemp(prefix="temporality-realrepo-")
    tasks_metrics = {}
    canonical_db = args.canonical_db
    try:
        for index, task in enumerate(selected):
            metrics, canonical_db = run_task(args, task, canonical_db, args.port, parent)
            tasks_metrics[task["id"]] = metrics
            with open(dump, "w") as handle:
                json.dump({"tasks": tasks_metrics, "partial": True, "canonical_db": canonical_db}, handle, ensure_ascii=False, indent=2)
            say(f"  partial metrics after {task['id']}: {dump}")
            if any(run.get("auth_failed") for run in metrics.values()):
                say("\nFATAL: provider rejected credentials — aborting remaining tasks")
                break
            if args.stop_after_e1 and index == 0:
                say("\n--stop-after-e1: chain seed validated, stopping")
                break
        report = final_report(tasks_metrics, args, env)
        with open(dump, "w") as handle:
            json.dump({"tasks": tasks_metrics, "report": report, "canonical_db": canonical_db}, handle, ensure_ascii=False, indent=2)
        say(f"\nmetrics   : {dump}")
    finally:
        if not args.keep:
            shutil.rmtree(parent, ignore_errors=True)


if __name__ == "__main__":
    main()
