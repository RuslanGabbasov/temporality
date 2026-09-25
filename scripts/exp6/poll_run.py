#!/usr/bin/env python3
"""Poll an agent-kernel run until it reaches a terminal status."""
import json
import sys
import time
import urllib.request

BASE = "http://localhost:8090"


def fetch(path: str) -> dict:
    with urllib.request.urlopen(BASE + path, timeout=30) as response:
        return json.load(response)


def main() -> None:
    run_id, project = sys.argv[1], sys.argv[2]
    timeout_s = int(sys.argv[3]) if len(sys.argv) > 3 else 600
    deadline = time.time() + timeout_s
    last = ""
    while time.time() < deadline:
        try:
            state = fetch(f"/v1/agent/runs/{run_id}?project={project}")
        except Exception as failure:  # noqa: BLE001 - transient poll errors are retryable
            print(f"poll error: {failure}", flush=True)
            time.sleep(5)
            continue
        status = state.get("status", "")
        if status != last:
            print(f"[{time.strftime('%H:%M:%S')}] status: {status}", flush=True)
            last = status
        if status.upper() in ("COMPLETED", "FAILED"):
            result = state.get("result", {})
            print(json.dumps({"run_id": run_id, "status": status, **result}, indent=2)[:4000])
            return
        time.sleep(5)
    print(f"TIMEOUT after {timeout_s}s (last status: {last})")
    sys.exit(1)


if __name__ == "__main__":
    main()
