#!/usr/bin/env python3
"""Benchmark: FRP Frame Runtime vs classic tool-calling harness (State 2 check).

Both harnesses get the SAME model, the SAME objective, the SAME repository with
two hidden bugs and several traps, and the SAME information reach over tools.
The only difference is the paradigm:

  FRP      render packet (budget-capped) -> CognitiveEmission -> affordances
  classic  full-history chat with tool calling (accumulating transcript)

The fixture repository (module example.com/ledger) contains:
  * bug 1: int32 accumulator in internal/series breaks internal/calc large-value
    totals (distant coupling: failing test lives in another package);
  * bug 2: internal/store.Remove writes a tombstone that Get/Len ignore (visible
    only via a full `go test ./...` run);
  * trap: `make test` runs only ./internal/calc (README claims it is the full
    suite), so store failures stay hidden until an explicit ./... run;
  * trap: ARCHITECTURE.md lies about the accumulator being lossless 64-bit and
    the store being stateless;
  * red herring: internal/util/format.go looks overflow-suspicious but is
    correct and its tests pass.

Winner criteria (checker): `go test ./...` green, no *_test.go modified,
internal/store/store.go changed, >=2 non-test files changed.

Preconditions:
  * PostgreSQL shared with the stack (docker compose up -d postgres)
  * `docker compose stop executor` (a compose executor steals executions)
  * make build (runtime + executor binaries)
  * model credentials in .env or environment (TEMPORALITY_MODEL_*)

Usage:
  python3 scripts/benchmark.py                 # both sides, 30-step cap
  python3 scripts/benchmark.py --side frp      # FRP only
  python3 scripts/benchmark.py --side classic  # classic only
  python3 scripts/benchmark.py --max-steps 5   # short mechanical debug run
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

# World limits mirrored by the classic tool executor: read/output caps and the
# process timeout both sides honor.
WORLD_LIMITS = {"max_read_bytes": 65536, "max_entries": 1000, "timeout_sec": 60}
CAP_READ = 65536
PROCESS_TIMEOUT_SEC = 60

FRP_AFFORDANCES = {
    "inspect_repository", "list_files", "read_file", "git_status", "git_log",
    "run_tests", "reproduce_issue", "run_command",
    "write_file", "patch_file", "create_file", "delete_file", "move_file",
}

OBJECTIVE = (
    "В этом Git-репозитории (Go-проект) есть падающие тесты. Найди все причины и исправь "
    "исходный код так, чтобы `go test ./...` проходил полностью. Изменять файлы `*_test.go` "
    "запрещено. Проверяй исправления запуском тестов. Закончи с итоговым ответом, когда "
    "`go test ./...` зелёный."
)

CLASSIC_SYSTEM = (
    "You are a software engineering agent working inside a Git repository workspace. "
    "Use the provided tools to inspect files, run commands, and edit code. Verify every "
    "fix by running the tests. Never modify *_test.go files. When the task is complete, "
    "reply with a short final summary in the same language as the task."
)


# ---------------------------------------------------------------- shared plumbing


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
    say(f"\n=== {text} " + "=" * max(0, 70 - len(text)))


def load_dotenv(path=".env"):
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


def env_or_fail():
    env = {**load_dotenv(), **os.environ}
    if not env.get("TEMPORALITY_MODEL_BASE_URL") or not env.get("TEMPORALITY_MODEL_ID"):
        sys.exit("TEMPORALITY_MODEL_BASE_URL / TEMPORALITY_MODEL_ID are not set (neither in .env nor in the environment)")
    return env


def stop(process):
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


def spawn_runtime(port):
    env = {**load_dotenv(), **os.environ}
    env.update({"DATABASE_URL": DATABASE_URL, "HTTP_ADDR": f"127.0.0.1:{port}"})
    if not env.get("TEMPORALITY_MODEL_BASE_URL") or not env.get("TEMPORALITY_MODEL_ID"):
        sys.exit("TEMPORALITY_MODEL_BASE_URL / TEMPORALITY_MODEL_ID are not set (neither in .env nor in the environment)")
    probe = subprocess.run(["lsof", "-nP", f"-iTCP:{port}", "-sTCP:LISTEN"], capture_output=True, text=True)
    if probe.returncode == 0 and probe.stdout.strip():
        sys.exit(f"port {port} is already held by another process (a stale runtime?): {probe.stdout.splitlines()[1] if len(probe.stdout.splitlines()) > 1 else probe.stdout.strip()}\nfree it or pass --port")
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


def wait_execution(execution_id, attempts=900):
    for _ in range(attempts):
        _, state = request("GET", f"/v1/executions/{execution_id}")
        if state["status"] in ("completed", "failed", "unavailable"):
            return state
        time.sleep(0.1)
    return state


def snippet(text, width=100):
    line = " ".join(str(text).split())
    return line if len(line) <= width else line[: width - 1] + "…"


# ---------------------------------------------------------------- fixture


FIXTURE_FILES = {
    "go.mod": "module example.com/ledger\n\ngo 1.23\n",
    "Makefile": ".PHONY: test build\n\nbuild:\n\tgo build ./...\n\ntest:\n\tgo test ./internal/calc\n",
    "README.md": (
        "# ledger\n\nCompact in-memory ledger for exposure reporting.\n\n"
        "## Layout\n\n- `cmd/ledger` — CLI entry point.\n- `internal/calc` — arithmetic helpers over positions.\n"
        "- `internal/series` — numeric accumulation helpers.\n- `internal/store` — in-memory record store.\n"
        "- `internal/util` — formatting utilities.\n\n"
        "## Testing\n\n`make test` runs the full test suite. Use it before every commit.\n"
    ),
    "ARCHITECTURE.md": (
        "# Architecture notes\n\nThe packages are intentionally small and independent.\n\n"
        "- `internal/series` provides lossless 64-bit accumulation; it never narrows values (design note L-14).\n"
        "- `internal/store` is a plain stateless map wrapper: every operation is O(1) and no state survives between calls.\n"
        "- `internal/calc` composes the helpers above and holds no logic of its own.\n"
    ),
    "cmd/ledger/main.go": (
        "// Command ledger prints a net exposure summary.\npackage main\n\nimport (\n\t\"fmt\"\n\n"
        "\t\"example.com/ledger/internal/calc\"\n\t\"example.com/ledger/internal/store\"\n)\n\n"
        "func main() {\n\ts := store.New()\n\ts.Put(\"spot\", \"netting-group-a\")\n"
        "\tlong := []int64{150, 200}\n\tshort := []int64{50}\n"
        "\tfmt.Println(\"records:\", s.Len(), \"net:\", calc.Net(long, short))\n}\n"
    ),
    "internal/series/series.go": (
        "// Package series provides numeric accumulation helpers for ledgers.\npackage series\n\n"
        "// Accumulate sums the values using a compact fixed-width accumulator.\n//\n"
        "// The 32-bit width keeps the accumulator cache-resident (design note L-14);\n"
        "// inputs are expected to stay within the narrow range documented there.\n"
        "func Accumulate(values []int64) int64 {\n\tvar total int32\n"
        "\tfor _, value := range values {\n\t\ttotal += int32(value)\n\t}\n\treturn int64(total)\n}\n\n"
        "// Mean returns the arithmetic mean, or 0 for an empty slice.\n"
        "func Mean(values []int64) float64 {\n\tif len(values) == 0 {\n\t\treturn 0\n\t}\n"
        "\treturn float64(Accumulate(values)) / float64(len(values))\n}\n"
    ),
    "internal/series/series_test.go": (
        "package series\n\nimport \"testing\"\n\n"
        "func TestAccumulateSmall(t *testing.T) {\n"
        "\tif got := Accumulate([]int64{1, 2, 3}); got != 6 {\n\t\tt.Fatalf(\"Accumulate = %d, want 6\", got)\n\t}\n}\n\n"
        "func TestMean(t *testing.T) {\n\tif got := Mean([]int64{2, 4}); got != 3 {\n\t\tt.Fatalf(\"Mean = %v, want 3\", got)\n\t}\n}\n"
    ),
    "internal/calc/calc.go": (
        "// Package calc composes ledger arithmetic.\npackage calc\n\n"
        "import \"example.com/ledger/internal/series\"\n\n"
        "// Total sums the position values.\nfunc Total(values []int64) int64 {\n\treturn series.Accumulate(values)\n}\n\n"
        "// Net returns the signed total of long and short books.\n"
        "func Net(long, short []int64) int64 {\n\treturn series.Accumulate(long) - series.Accumulate(short)\n}\n"
    ),
    "internal/calc/calc_test.go": (
        "package calc\n\nimport \"testing\"\n\n"
        "func TestNet(t *testing.T) {\n\tif got := Net([]int64{10, 5}, []int64{3}); got != 12 {\n"
        "\t\tt.Fatalf(\"Net = %d, want 12\", got)\n\t}\n}\n\n"
        "func TestTotalHandlesLargeValues(t *testing.T) {\n"
        "\tgot := Total([]int64{2_000_000_000, 2_000_000_000, 2_000_000_000})\n"
        "\tif got != 6_000_000_000 {\n"
        "\t\tt.Fatalf(\"Total = %d, want 6000000000 (int64 accumulation must not overflow)\", got)\n\t}\n}\n"
    ),
    "internal/store/store.go": (
        "// Package store implements the in-memory record store.\npackage store\n\n"
        "// Store keeps records keyed by id.\ntype Store struct {\n"
        "\trecords map[string]string\n\tremoved map[string]bool\n}\n\n"
        "// New returns an empty Store.\n"
        "func New() *Store {\n\treturn &Store{records: map[string]string{}, removed: map[string]bool{}}\n}\n\n"
        "// Put stores a record.\nfunc (s *Store) Put(id, record string) {\n\ts.records[id] = record\n}\n\n"
        "// Get returns a record by id.\n"
        "func (s *Store) Get(id string) (string, bool) {\n\trecord, ok := s.records[id]\n\treturn record, ok\n}\n\n"
        "// Remove soft-deletes a record so it stops matching queries.\n"
        "func (s *Store) Remove(id string) {\n\ts.removed[id] = true\n}\n\n"
        "// Len returns the number of live records.\nfunc (s *Store) Len() int {\n\treturn len(s.records)\n}\n"
    ),
    "internal/store/store_test.go": (
        "package store\n\nimport \"testing\"\n\n"
        "func TestPutGet(t *testing.T) {\n\ts := New()\n\ts.Put(\"a\", \"alpha\")\n"
        "\tgot, ok := s.Get(\"a\")\n\tif !ok || got != \"alpha\" {\n\t\tt.Fatalf(\"Get = %q,%v want alpha,true\", got, ok)\n\t}\n}\n\n"
        "func TestRemove(t *testing.T) {\n\ts := New()\n\ts.Put(\"a\", \"alpha\")\n\ts.Remove(\"a\")\n"
        "\tif _, ok := s.Get(\"a\"); ok {\n\t\tt.Fatal(\"Get after Remove: record still visible, want removed\")\n\t}\n"
        "\tif s.Len() != 0 {\n\t\tt.Fatalf(\"Len = %d, want 0 after Remove\", s.Len())\n\t}\n}\n"
    ),
    "internal/util/format.go": (
        "// Package util holds small formatting helpers.\npackage util\n\nimport \"encoding/hex\"\n\n"
        "// FormatID packs an int64 id into a fixed 16-char hex string (big-endian).\n//\n"
        "// The shift chain below is intentional: it round-trips negative ids through\n"
        "// the unsigned domain without branching. Do not \"simplify\" it.\n"
        "func FormatID(id int64) string {\n\tu := uint64(id)\n\tbuf := [8]byte{}\n"
        "\tfor i := 7; i >= 0; i-- {\n\t\tbuf[i] = byte(u & 0xff)\n\t\tu >>= 8\n\t}\n"
        "\treturn hex.EncodeToString(buf[:])\n}\n"
    ),
    "internal/util/format_test.go": (
        "package util\n\nimport \"testing\"\n\n"
        "func TestFormatID(t *testing.T) {\n\tif got := FormatID(1); got != \"0000000000000001\" {\n"
        "\t\tt.Fatalf(\"FormatID(1) = %s\", got)\n\t}\n"
        "\tif got := FormatID(-1); got != \"ffffffffffffffff\" {\n\t\tt.Fatalf(\"FormatID(-1) = %s\", got)\n\t}\n}\n"
    ),
}


def decoy_package(index):
    """Deterministic plausible decoy: a self-contained report builder package.
    Its tests pass; it exists to add honest search/reading bulk to the repo."""
    source = (
        f"// Package reports holds per-desk exposure report builders.\npackage reports\n\n"
        f"import \"fmt\"\n\n"
        f"// Desk{index}Summary aggregates desk-{index} positions.\n"
        f"type Desk{index}Summary struct {{\n"
        f"\tBook    string\n\tNet     int64\n\tCount   int\n}}\n\n"
        f"// BuildDesk{index} folds raw positions into a desk summary.\n"
        f"func BuildDesk{index}(book string, positions []int64) Desk{index}Summary {{\n"
        f"\tsummary := Desk{index}Summary{{Book: book}}\n"
        f"\tfor _, position := range positions {{\n"
        f"\t\tsummary.Net += position * {index + 1}\n"
        f"\t\tsummary.Count++\n\t}}\n"
        f"\treturn summary\n}}\n\n"
        f"// RenderDesk{index} formats a desk summary for the daily digest.\n"
        f"func RenderDesk{index}(summary Desk{index}Summary) string {{\n"
        f"\treturn fmt.Sprintf(\"desk-{index} book=%s net=%d count=%d\", summary.Book, summary.Net, summary.Count)\n}}\n"
    )
    test = (
        f"package reports\n\nimport \"testing\"\n\n"
        f"func TestBuildDesk{index}(t *testing.T) {{\n"
        f"\tsummary := BuildDesk{index}(\"book-a\", []int64{{10, 20, {index}}})\n"
        f"\twant := int64(0)\n"
        f"\tfor _, position := range []int64{{10, 20, {index}}} {{\n"
        f"\t\twant += position * {index + 1}\n\t}}\n"
        f"\tif summary.Net != want {{\n"
        f"\t\tt.Fatalf(\"desk-{index} Net = %d, want %d\", summary.Net, want)\n"
        f"\t}}\n"
        f"\tif summary.Count != 3 {{\n"
        f"\t\tt.Fatalf(\"desk-{index} Count = %d, want 3\", summary.Count)\n"
        f"\t}}\n}}\n\n"
        f"func TestRenderDesk{index}(t *testing.T) {{\n"
        f"\tline := RenderDesk{index}(Desk{index}Summary{{Book: \"book-b\", Net: 7, Count: 1}})\n"
        f"\twant := fmt.Sprintf(\"desk-{index} book=book-b net=7 count=1\")\n"
        f"\tif line != want {{\n"
        f"\t\tt.Fatalf(\"desk-{index} render = %q, want %q\", line, want)\n"
        f"\t}}\n}}\n"
    )
    test = test.replace('import "testing"', 'import ("fmt"\n\t"testing")', 1)
    return f"internal/reports/desk{index}.go", source, f"internal/reports/desk{index}_test.go", test


def create_fixture(parent, scale=0):
    """Materialize the trapped repository and commit it. Deterministic."""
    workspace = tempfile.mkdtemp(prefix="temporality-bench-", dir=parent)
    files = dict(FIXTURE_FILES)
    if scale > 0:
        files[f"internal/reports/doc.go"] = "// Package reports holds per-desk exposure report builders.\npackage reports\n"
    for index in range(1, scale + 1):
        source_path, source, test_path, test = decoy_package(index)
        files[source_path] = source
        files[test_path] = test
    for relative, content in files.items():
        target = os.path.join(workspace, relative)
        os.makedirs(os.path.dirname(target), exist_ok=True)
        with open(target, "w") as handle:
            handle.write(content)
    subprocess.run(["git", "-c", "init.defaultBranch=main", "init", "-q", workspace], check=True)
    subprocess.run(["git", "-C", workspace, "add", "."], check=True)
    subprocess.run(["git", "-C", workspace, "-c", "user.email=bench@temporality.dev", "-c", "user.name=Benchmark", "commit", "-q", "-m", "initial: ledger"], check=True)
    return workspace


def run_tool(command, cwd, timeout=PROCESS_TIMEOUT_SEC):
    """Bounded subprocess mirroring the executor's process effect."""
    try:
        finished = subprocess.run(command, cwd=cwd, capture_output=True, text=True, timeout=timeout)
        stdout = finished.stdout[:CAP_READ]
        stderr = finished.stderr[:CAP_READ]
        return {"command": command[0], "exit_code": finished.returncode, "stdout": stdout, "stderr": stderr,
                "truncated": len(finished.stdout) > CAP_READ or len(finished.stderr) > CAP_READ}
    except subprocess.TimeoutExpired as error:
        stdout = ((error.stdout or b"").decode(errors="replace"))[:CAP_READ]
        stderr = ((error.stderr or b"").decode(errors="replace"))[:CAP_READ]
        return {"command": command[0], "exit_code": None, "error": f"command {command[0]!r} exceeded timeout ({timeout}s)", "stdout": stdout, "stderr": stderr}


def check_workspace(workspace):
    """Independent verdict: is the repository actually fixed, honestly?"""
    go = os.environ.get("TEMPORALITY_GO_BINARY", "go")
    details = {}
    test = run_tool([go, "test", "./..."], workspace, timeout=300)
    details["go_test_green"] = test["exit_code"] == 0
    details["go_test_stderr"] = snippet(test.get("stderr", "") or test.get("stdout", ""), 300)
    changed = run_tool(["git", "diff", "--name-only", "HEAD"], workspace)
    files = [line for line in (changed.get("stdout") or "").splitlines() if line.strip()]
    details["changed_files"] = files
    details["tests_untouched"] = not any(name.endswith("_test.go") for name in files)
    details["store_fixed"] = "internal/store/store.go" in files
    details["second_fix_present"] = len([n for n in files if not n.endswith("_test.go")]) >= 2
    details["ok"] = details["go_test_green"] and details["tests_untouched"] and details["store_fixed"] and details["second_fix_present"]
    return details["ok"], details


# ---------------------------------------------------------------- classic harness tools


def resolve(workspace, raw):
    if not raw or not raw.strip():
        raise ValueError("path is required")
    relative = os.path.normpath(raw.strip())
    if relative.startswith("..") or os.path.isabs(relative):
        raise ValueError(f"path {raw!r} escapes the workspace root")
    return os.path.join(workspace, relative)


CLASSIC_TOOLS = [
    {"type": "function", "function": {
        "name": "read_file",
        "description": "Read a text file from the workspace.",
        "parameters": {"type": "object", "required": ["path"], "properties": {
            "path": {"type": "string", "description": "path relative to the workspace root, e.g. ."}}}}},
    {"type": "function", "function": {
        "name": "list_files",
        "description": "List one directory level (name, directory flag, size), sorted by name.",
        "parameters": {"type": "object", "required": ["path"], "properties": {
            "path": {"type": "string", "description": "directory path relative to the workspace root, e.g. ."}}}}},
    {"type": "function", "function": {
        "name": "git_status",
        "description": "Show the git working tree status (branch, staged, untracked).",
        "parameters": {"type": "object", "required": ["path"], "properties": {
            "path": {"type": "string", "description": "repository path relative to the workspace root"}}}}},
    {"type": "function", "function": {
        "name": "git_log",
        "description": "Show recent first-parent commits (sha, author, summary).",
        "parameters": {"type": "object", "required": ["path"], "properties": {
            "path": {"type": "string", "description": "repository path relative to the workspace root"},
            "limit": {"type": "number", "description": "max commits (default 20)"}}}}},
    {"type": "function", "function": {
        "name": "run_command",
        "description": "Run a bounded command in the workspace (e.g. go test ./..., make test). 60s timeout, 64KB output cap per stream.",
        "parameters": {"type": "object", "required": ["command"], "properties": {
            "command": {"type": "string", "description": "executable name, e.g. go"},
            "args": {"type": "array", "items": {"type": "string"}, "description": "command arguments"},
            "cwd": {"type": "string", "description": "working directory relative to the workspace root"}}}}},
    {"type": "function", "function": {
        "name": "write_file",
        "description": "Write full file content (creates or overwrites).",
        "parameters": {"type": "object", "required": ["path"], "properties": {
            "path": {"type": "string", "description": "path relative to the workspace root"},
            "content": {"type": "string", "description": "full file content to write (overwrites)"}}}}},
    {"type": "function", "function": {
        "name": "patch_file",
        "description": "Replace a substring in a file. Fails if the substring is not present.",
        "parameters": {"type": "object", "required": ["path", "find"], "properties": {
            "path": {"type": "string", "description": "path relative to the workspace root"},
            "find": {"type": "string", "description": "exact substring to replace"},
            "replace": {"type": "string", "description": "replacement substring (default empty)"},
            "all": {"type": "boolean", "description": "replace every occurrence (default false)"}}}}},
    {"type": "function", "function": {
        "name": "create_file",
        "description": "Create a new file; fails if the file already exists.",
        "parameters": {"type": "object", "required": ["path"], "properties": {
            "path": {"type": "string", "description": "path relative to the workspace root"},
            "content": {"type": "string", "description": "file content; fails if the file already exists"}}}}},
    {"type": "function", "function": {
        "name": "delete_file",
        "description": "Delete a file (or empty directory).",
        "parameters": {"type": "object", "required": ["path"], "properties": {
            "path": {"type": "string", "description": "path relative to the workspace root"}}}}},
    {"type": "function", "function": {
        "name": "move_file",
        "description": "Rename/move a file inside the workspace.",
        "parameters": {"type": "object", "required": ["path", "target"], "properties": {
            "path": {"type": "string", "description": "source path relative to the workspace root"},
            "target": {"type": "string", "description": "destination path relative to the workspace root"}}}}},
]


def classic_tool_call(workspace, name, args):
    try:
        if name == "read_file":
            target = resolve(workspace, args.get("path"))
            with open(target) as handle:
                raw = handle.read()
            return {"path": target, "size": len(raw), "content": raw[:CAP_READ], "truncated": len(raw) > CAP_READ}
        if name == "list_files":
            target = resolve(workspace, args.get("path"))
            entries = sorted(os.listdir(target))[:1000]
            items = []
            for entry in entries:
                full = os.path.join(target, entry)
                items.append({"name": entry, "directory": os.path.isdir(full),
                              "size": None if os.path.isdir(full) else os.path.getsize(full)})
            return {"path": target, "entries": len(items), "items": items}
        if name == "git_status":
            target = resolve(workspace, args.get("path"))
            branch = run_tool(["git", "rev-parse", "--abbrev-ref", "HEAD"], target)
            porcelain = run_tool(["git", "status", "--porcelain"], target)
            lines = (porcelain.get("stdout") or "").splitlines()
            staged = sum(1 for line in lines if line[:1] not in (" ", "?", ""))
            untracked = sum(1 for line in lines if line.startswith("??"))
            return {"path": target, "status": {"branch": (branch.get("stdout") or "").strip(), "staged": staged, "untracked": untracked}}
        if name == "git_log":
            target = resolve(workspace, args.get("path"))
            limit = int(args.get("limit") or 20)
            log = run_tool(["git", "log", f"-{limit}", "--pretty=format:%H|%an|%s"], target)
            commits = [{"sha": line.split("|")[0], "author": line.split("|")[1], "summary": line.split("|", 2)[2]}
                       for line in (log.get("stdout") or "").splitlines() if "|" in line]
            return {"path": target, "commits": commits, "complete": len(commits) < limit}
        if name == "run_command":
            command = [str(args.get("command"))] + [str(value) for value in (args.get("args") or [])]
            cwd = resolve(workspace, args.get("cwd") or ".")
            result = run_tool(command, cwd)
            if result.get("exit_code") not in (0, None):
                result["error"] = f"command {args.get('command')!r} failed with exit code {result['exit_code']}"
            return result
        if name == "write_file":
            target = resolve(workspace, args.get("path"))
            os.makedirs(os.path.dirname(target) or workspace, exist_ok=True)
            existed = os.path.exists(target)
            with open(target, "w") as handle:
                handle.write(str(args.get("content") or ""))
            return {"path": target, "bytes": len(str(args.get("content") or "")), "overwritten": existed}
        if name == "patch_file":
            target = resolve(workspace, args.get("path"))
            find = str(args.get("find") or "")
            replace = str(args.get("replace") or "")
            if not find:
                return {"error": "find is required"}
            with open(target) as handle:
                raw = handle.read()
            if find not in raw:
                return {"error": "find pattern not present in file"}
            count = raw.count(find)
            if args.get("all"):
                raw = raw.replace(find, replace)
            else:
                raw = raw.replace(find, replace, 1)
                count = 1
            with open(target, "w") as handle:
                handle.write(raw)
            return {"path": target, "replacements": count}
        if name == "create_file":
            target = resolve(workspace, args.get("path"))
            if os.path.exists(target):
                return {"error": f"file already exists: {target}"}
            os.makedirs(os.path.dirname(target) or workspace, exist_ok=True)
            with open(target, "x") as handle:
                handle.write(str(args.get("content") or ""))
            return {"path": target, "bytes": len(str(args.get("content") or ""))}
        if name == "delete_file":
            target = resolve(workspace, args.get("path"))
            os.remove(target)
            return {"path": target, "deleted": True}
        if name == "move_file":
            source = resolve(workspace, args.get("path"))
            destination = resolve(workspace, args.get("target"))
            os.makedirs(os.path.dirname(destination) or workspace, exist_ok=True)
            os.rename(source, destination)
            return {"path": source, "target": destination}
        return {"error": f"unknown tool {name}"}
    except Exception as error:  # tool errors are results, not crashes
        return {"error": str(error)}


def parse_duration_ms(raw, default=180000):
    """Parse Go-style durations (180s, 3m0s, 2h, 500ms) into milliseconds."""
    import re

    text = str(raw or "").strip()
    if not text:
        return default
    total = 0.0
    matched = False
    for value, unit in re.findall(r"([0-9.]+)(ms|s|m|h)", text):
        matched = True
        total += float(value) * {"ms": 1, "s": 1000, "m": 60000, "h": 3600000}[unit]
    return int(total) if matched else default


def classic_chat(env, body):
    base = env["TEMPORALITY_MODEL_BASE_URL"].rstrip("/")
    payload = json.dumps(body).encode()
    req = urllib.request.Request(base + "/chat/completions", data=payload, method="POST",
                                 headers={"content-type": "application/json", "authorization": "Bearer " + env.get("TEMPORALITY_MODEL_API_KEY", "")})
    timeout_ms = parse_duration_ms(env.get("TEMPORALITY_MODEL_TIMEOUT"))
    with urllib.request.urlopen(req, timeout=timeout_ms / 1000 + 5) as response:
        return json.load(response)


def run_classic(args, env, workspace):
    head("classic harness: full-history tool calling")
    messages = [
        {"role": "system", "content": CLASSIC_SYSTEM},
        {"role": "user", "content": OBJECTIVE},
    ]
    metrics = {"side": "classic", "steps": 0, "model_calls": 0, "retries": 0,
               "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0, "workspace": workspace}
    started = time.time()
    answer = None
    max_tokens = int(env.get("TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS") or 16384)
    for step in range(1, args.max_steps + 1):
        say(f"\n--- round {step}/{args.max_steps}")
        body = {
            "model": env["TEMPORALITY_MODEL_ID"],
            "messages": messages,
            "tools": CLASSIC_TOOLS,
            "tool_choice": "auto",
            "temperature": float(env.get("TEMPORALITY_MODEL_TEMPERATURE") or 0),
            "max_tokens": max_tokens,
        }
        reasoning = (env.get("TEMPORALITY_MODEL_REASONING") or "").strip()
        if reasoning == "off":
            body["reasoning"] = {"enabled": False}
        elif reasoning == "exclude":
            body["reasoning"] = {"exclude": True}
        elif reasoning in ("low", "medium", "high"):
            body["reasoning"] = {"effort": reasoning}
        response = None
        for attempt in range(1, 5):
            try:
                response = classic_chat(env, body)
                break
            except urllib.error.HTTPError as error:
                detail = error.read().decode(errors="replace")[:200]
                if error.code not in (429, 500, 502, 503, 504) or attempt == 4:
                    say(f"classic failed: HTTP {error.code} {detail}")
                    response = None
                    break
                metrics["retries"] += 1
                say(f"retry     : provider HTTP {error.code} (attempt {attempt}/4)")
                time.sleep(3)
            except Exception as error:
                if attempt == 4:
                    say(f"classic failed: {error}")
                    break
                metrics["retries"] += 1
                say(f"retry     : {error} (attempt {attempt}/4)")
                time.sleep(3)
        if response is None:
            break
        metrics["model_calls"] += 1
        metrics["steps"] = step
        usage = response.get("usage") or {}
        metrics["prompt_tokens"] += usage.get("prompt_tokens") or 0
        metrics["completion_tokens"] += usage.get("completion_tokens") or 0
        metrics["total_tokens"] += usage.get("total_tokens") or 0
        message = (response.get("choices") or [{}])[0].get("message") or {}
        tool_calls = message.get("tool_calls") or []
        if message.get("content"):
            say(f"assistant: {snippet(message['content'])}")
        if not tool_calls:
            answer = message.get("content") or ""
            say("final     : no more tool calls — done")
            break
        messages.append(message)
        for call in tool_calls:
            function = call.get("function") or {}
            name = function.get("name") or ""
            try:
                call_args = json.loads(function.get("arguments") or "{}")
            except json.JSONDecodeError:
                call_args = {}
            result = classic_tool_call(workspace, name, call_args)
            say(f"tool      : {name} {json.dumps(call_args, ensure_ascii=False)[:140]}")
            say(f"result    : {snippet(json.dumps(result, ensure_ascii=False), 160)}")
            messages.append({"role": "tool", "tool_call_id": call.get("id"), "name": name,
                             "content": json.dumps(result, ensure_ascii=False)})
    metrics["finished"] = answer is not None
    metrics["answer"] = answer or "(no final answer within the round budget)"
    metrics["seconds"] = round(time.time() - started, 1)
    ok, details = check_workspace(workspace)
    metrics["success"] = ok
    metrics["checker"] = details
    return metrics


# ---------------------------------------------------------------- FRP runner


def read_affordances():
    _, payload = request("GET", "/v1/affordances")
    definitions = [item for item in payload.get("definitions") or [] if item.get("id") in FRP_AFFORDANCES]
    missing = FRP_AFFORDANCES - {item["id"] for item in definitions}
    if missing:
        sys.exit(f"runtime did not serve canonical affordances: missing {sorted(missing)}")
    return sorted(definitions, key=lambda item: item["id"])


def run_frp(args, workspace):
    head("FRP runtime: render -> emission -> affordance -> execution")
    metrics = {"side": "frp", "steps": 0, "model_calls": 0, "retries": 0,
               "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0, "workspace": workspace}
    world_id = "bench-" + uuid.uuid4().hex[:10]
    world = {
        "world_id": world_id,
        "state_version": 1,
        "resources": [{"id": "repo", "type": "git_repository", "path": workspace}],
        "capabilities": ["filesystem.read", "filesystem.write", "git.read", "process.execute"],
        "limits": WORLD_LIMITS,
    }
    status, registered = request_status("POST", "/v1/worlds", world)
    if status != 201:
        sys.exit(f"world registration failed: {status} {registered}")
    say(f"world     : {world_id} capabilities={world['capabilities']}")

    status, boot = request_status("POST", "/v1/bootstrap", {
        "world_id": world_id,
        "objective_text": OBJECTIVE,
        "budget_tokens": args.budget_tokens,
    })
    if status != 201:
        sys.exit(f"bootstrap failed: {status} {boot}")
    summary = boot["summary"]
    say(f"episode   : {boot['episode_id']}")
    say(f"bootstrap : observations={summary['observations']} claims={summary['claims']} regions={summary['regions']} entities={summary['entities']} relations={summary['relations']}")

    executor_env = os.environ.copy()
    executor_env.update({"DATABASE_URL": DATABASE_URL, "EPISODE_ID": boot["episode_id"], "WORLD_ID": world_id})
    if "/usr/local/go/bin" not in executor_env.get("PATH", ""):
        executor_env["PATH"] = "/usr/local/go/bin:" + executor_env.get("PATH", "")
    executor = subprocess.Popen([EXECUTOR_BINARY], env=executor_env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    definitions = read_affordances()
    current_frame = boot["frame_id"]
    answer = None
    started = time.time()
    try:
        for step in range(1, args.max_steps + 1):
            say(f"\n--- step {step}/{args.max_steps} frame={current_frame[:8]}…")
            result = {}
            status = 0
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
                metrics["retries"] += 1
                say(f"retry     : model-step rejected (attempt {attempt}/4, http {status}): {str(result.get('error'))[:160]}")
                time.sleep(2)
            if status != 201:
                say(f"model-step failed: {status} {json.dumps(result, ensure_ascii=False)[:500]}")
                break
            metrics["model_calls"] += 1
            metrics["steps"] = step
            usage = result.get("model_usage") or {}
            metrics["prompt_tokens"] += usage.get("prompt_tokens") or 0
            metrics["completion_tokens"] += usage.get("completion_tokens") or 0
            metrics["total_tokens"] += usage.get("total_tokens") or 0
            emission = result["emission"]
            for claim in emission.get("claims") or []:
                say(f"claim     : {snippet(claim['proposition'])} (conf={claim.get('confidence')})")
            actions = emission.get("actions") or []
            executions = result["step"].get("executions") or []
            if not actions:
                say("actions   : none this step")
            for action in actions:
                say(f"action    : {action['affordance']} {json.dumps(action.get('args') or {}, ensure_ascii=False)[:140]}")
            for execution in executions:
                final = wait_execution(execution["execution_id"])
                mark = "ok" if final["status"] == "completed" else "FAILED"
                say(f"executor  : {execution['affordance_id']} {mark}")
                if final["status"] != "completed":
                    error = final.get("error") or {}
                    diagnostics = error.get("diagnostics") or {}
                    tail = diagnostics.get("stderr") or error.get("message") or ""
                    if tail:
                        say(f"  stderr  : {snippet(tail, 200)}")
            if executions:
                request("POST", "/v1/projections/regions/rebuild", {"episode_id": boot["episode_id"], "branch_id": boot["branch_id"]})
            if emission.get("completion"):
                answer = emission["completion"]
                say(f"completion: {snippet(answer, 300)}")
                current_frame = result["step"]["frame"]["frame_id"]
                break
            current_frame = result["step"]["frame"]["frame_id"]
    finally:
        stop(executor)
    metrics["finished"] = answer is not None
    metrics["answer"] = answer or "(no completion within the step budget)"
    metrics["seconds"] = round(time.time() - started, 1)
    metrics["episode_id"] = boot["episode_id"]
    ok, details = check_workspace(workspace)
    metrics["success"] = ok
    metrics["checker"] = details
    return metrics


# ---------------------------------------------------------------- report


def report(results):
    head("Comparison")
    columns = ["side", "success", "finished", "steps", "model_calls", "retries", "prompt_tokens", "completion_tokens", "total_tokens", "seconds"]
    widths = {name: max(len(name), *(len(str(result.get(name))) for result in results)) if results else len(name) for name in columns}
    say("  ".join(name.ljust(widths[name]) for name in columns))
    for result in results:
        say("  ".join(str(result.get(name)).ljust(widths[name]) for name in columns))
    for result in results:
        say(f"\n[{result['side']}] checker: green={result['checker']['go_test_green']} tests_untouched={result['checker']['tests_untouched']} "
            f"store_fixed={result['checker']['store_fixed']} changed={result['checker']['changed_files']}")
        say(f"[{result['side']}] answer: {snippet(result['answer'], 400)}")
        say(f"[{result['side']}] workspace: {result['workspace']}")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--side", choices=["both", "frp", "classic"], default="both")
    parser.add_argument("--scale", type=int, default=0, help="extra decoy report packages in the fixture (default 0; try 8-12 for a long-horizon run)")
    parser.add_argument("--max-steps", type=int, default=30, help="model round/step budget per side (default 30)")
    parser.add_argument("--budget-tokens", type=int, default=16000, help="FRP render budget (default 16000)")
    parser.add_argument("--port", type=int, default=18087, help="local runtime port (default 18087)")
    parser.add_argument("--keep", action="store_true", help="keep workspaces and skip cleanup")
    parser.add_argument("--dump", default=None, help="write metrics JSON here (default benchmarks/benchmark-<ts>.json)")
    args = parser.parse_args()
    global BASE

    env = env_or_fail()
    if shutil.which("git") is None:
        sys.exit("git is required")

    head("0. Preconditions")
    if not os.path.exists(RUNTIME_BINARY) or not os.path.exists(EXECUTOR_BINARY):
        sys.exit(f"binaries not found: {RUNTIME_BINARY} / {EXECUTOR_BINARY}\nrun: make build")
    if compose_executor_running():
        sys.exit("compose executor is running and will steal executions: run `docker compose stop executor` first")
    go = os.environ.get("TEMPORALITY_GO_BINARY", "go")
    if shutil.which(go) is None and os.path.exists("/usr/local/go/bin/go"):
        os.environ["PATH"] = "/usr/local/go/bin:" + os.environ.get("PATH", "")
    if shutil.which(go) is None:
        sys.exit("go toolchain not found")
    say(f"model     : {env['TEMPORALITY_MODEL_ID']} (max_output_tokens={env.get('TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS')})")

    runtime_process = None
    results = []
    parent = tempfile.mkdtemp(prefix="temporality-bench-parent-")
    try:
        runtime_process, BASE = spawn_runtime(args.port)
        _, config = request_status("GET", "/v1/model/config")
        if not config.get("configured"):
            sys.exit(f"model is not configured on {BASE}: {config.get('error')}")
        say(f"runtime   : {BASE} (local)")

        if args.side in ("both", "frp"):
            workspace = create_fixture(parent, args.scale)
            results.append(run_frp(args, workspace))
        if args.side in ("both", "classic"):
            workspace = create_fixture(parent, args.scale)
            results.append(run_classic(args, env, workspace))

        report(results)
    finally:
        if runtime_process is not None:
            stop(runtime_process)
        if not args.keep:
            shutil.rmtree(parent, ignore_errors=True)

    dump = args.dump
    if dump is None:
        os.makedirs("benchmarks", exist_ok=True)
        dump = time.strftime("benchmarks/benchmark-%Y%m%d-%H%M%S.json")
    with open(dump, "w") as handle:
        json.dump({"objective": OBJECTIVE, "model": env["TEMPORALITY_MODEL_ID"], "max_steps": args.max_steps, "scale": args.scale, "results": results}, handle, ensure_ascii=False, indent=2)
    say(f"\nmetrics   : {dump}")


if __name__ == "__main__":
    main()
