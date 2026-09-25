#!/usr/bin/env python3
"""Experiment 8 run driver: reset the sandbox workspace, start a run, poll it.

Usage:
  run_series.py <v1|v2|v3> <spec-file>

Spec file: one run per line, `run_id|prompt[|max_turns]`. The workspace is
reset from examples/experiment8/lighthouse-<version> before each run so every
run starts pristine and reuse must come from knowledge, not leftover files.
"""
import json
import shutil
import subprocess
import sys
import time
import urllib.request

KERNEL = "http://localhost:8090"
ROOT = "/Users/ruslan/Projects/temporality"
SANDBOX = f"{ROOT}/.sandbox/lighthouse"
FIXTURE = f"{ROOT}/examples/experiment8/lighthouse-{sys.argv[1]}"
WORKSPACE = SANDBOX
MAX_TURNS = 12


def reset_workspace() -> None:
    shutil.rmtree(SANDBOX, ignore_errors=True)
    shutil.copytree(FIXTURE, SANDBOX)


def start(run_id: str, prompt: str, max_turns: int) -> None:
    body = json.dumps({
        "run_id": run_id,
        "project": "lighthouse",
        "prompt": prompt,
        "max_turns": max_turns,
        "workspace_path": WORKSPACE,
    }).encode()
    request = urllib.request.Request(f"{KERNEL}/v1/agent/runs", data=body, headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(request, timeout=30) as response:
        print(json.load(response), flush=True)


def poll(run_id: str, timeout_s: int = 900) -> dict:
    deadline = time.time() + timeout_s
    last = ""
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(f"{KERNEL}/v1/agent/runs/{run_id}?project=lighthouse", timeout=30) as response:
                state = json.load(response)
        except Exception as failure:  # noqa: BLE001 - transient poll errors are retryable
            print(f"poll error: {failure}", flush=True)
            time.sleep(5)
            continue
        if state.get("status", "") != last:
            print(f"[{time.strftime('%H:%M:%S')}] {run_id}: {state.get('status')}", flush=True)
            last = state.get("status", "")
        if last.upper() in ("COMPLETED", "FAILED"):
            return state
        time.sleep(5)
    print(f"TIMEOUT waiting for {run_id}")
    sys.exit(1)


def main() -> None:
    with open(sys.argv[2], encoding="utf-8") as handle:
        specs = [line.rstrip("\n").split("|") for line in handle if line.strip() and not line.startswith("#")]
    for spec in specs:
        run_id, prompt = spec[0], spec[1]
        max_turns = int(spec[2]) if len(spec) > 2 and spec[2].strip() else MAX_TURNS
        reset_workspace()
        print(f"=== {run_id} (workspace reset from lighthouse-{sys.argv[1]}, max_turns={max_turns}) ===", flush=True)
        start(run_id, prompt, max_turns)
        state = poll(run_id)
        result = state.get("result", {})
        print(f"--- {run_id}: {state.get('status')} turns={result.get('turns')} answer={str(result.get('answer'))[:600]}", flush=True)
        for line in subprocess.run([sys.executable, f"{ROOT}/scripts/exp6/run_events.py", "lighthouse", run_id], capture_output=True, text=True).stdout.splitlines():
            if "knowledge.proposed" in line or "model.failed" in line or "run.failed" in line:
                print("    ", line.strip(), flush=True)


if __name__ == "__main__":
    main()
