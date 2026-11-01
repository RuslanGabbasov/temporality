package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The background wrapper protocol (docs/agent-async-operations.md): the shell
// writes its own pid, runs the ORIGINAL argv via "$@" (no re-parsing), then
// records the exit code — kill needs the pid, attach-after-restart needs the
// code, and output keeps streaming through the normal docker exec pipe.
func TestOpShellWrapper(t *testing.T) {
	argv := OpShellWrapper("op-123", []string{"bash", "-lc", "yarn install"})
	if argv[0] != "/bin/sh" || argv[1] != "-c" {
		t.Fatalf("wrapper must run via /bin/sh -c, got %v", argv[:2])
	}
	script := argv[2]
	if !strings.Contains(script, "echo $$ > /scratch/.ops/op-123.pid") {
		t.Fatalf("wrapper must record the pid, got %q", script)
	}
	if !strings.Contains(script, `"$@"`) {
		t.Fatalf("wrapper must invoke the command via \"$@\", got %q", script)
	}
	if !strings.Contains(script, "echo $code > /scratch/.ops/op-123.code") {
		t.Fatalf("wrapper must record the exit code, got %q", script)
	}
	// The original argv rides as the wrapper's parameters.
	if len(argv) < 5 || argv[3] != "sh" || argv[4] != "bash" || argv[6] != "yarn install" {
		t.Fatalf("original argv must trail the wrapper (arg0=sh), got %v", argv)
	}
}

func TestOpMarkerAndTokenSanitize(t *testing.T) {
	if got := OpMarkerEnv("op-1"); got != "__TEMPORALITY_OP=op-1" {
		t.Fatalf("marker = %q", got)
	}
	if got := sanitizeOpToken("../../etc/passwd"); strings.ContainsAny(got, "/.") {
		t.Fatalf("token must be path-safe, got %q", got)
	}
}

// The exit code and the start time live on the host bind mount: a FINISHED
// operation reports its recorded result even when the container is gone —
// a reaped or lost container must never turn a recorded exit code into
// "unknown" (docs/agent-async-operations.md).
func TestOperationStateReadsRecordedExitCodeFromHostScratch(t *testing.T) {
	d := &Docker{Root: t.TempDir()}
	key := "proj/run-1"
	ops := filepath.Join(d.runScratchDir(key), ".ops")
	if err := os.MkdirAll(ops, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ops, "op-1"+opPidSuffix), []byte("42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ops, "op-1"+opCodeSuffix), []byte("7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := d.OperationState(key, "op-1")
	if err != nil {
		t.Fatalf("recorded state needs no container: %v", err)
	}
	if !state.Finished || state.ExitCode != 7 {
		t.Fatalf("the recorded exit code must win, got %+v", state)
	}
	if state.StartedAt.IsZero() {
		t.Fatalf("the pid marker dates the operation start, got %+v", state)
	}
}

// The durable "something may still be running" signal: a pid marker without
// its exit-code twin means an unfinished operation — the reaper and the
// teardown keep such containers alive (docs/agent-async-operations.md).
func TestScratchHasUnfinishedOps(t *testing.T) {
	d := &Docker{Root: t.TempDir()}
	key := "proj/run-2"
	ops := filepath.Join(d.runScratchDir(key), ".ops")
	if err := os.MkdirAll(ops, 0o777); err != nil {
		t.Fatal(err)
	}
	if d.scratchHasUnfinishedOps(key) {
		t.Fatal("no markers — nothing unfinished")
	}
	if err := os.WriteFile(filepath.Join(ops, "op-a"+opPidSuffix), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !d.scratchHasUnfinishedOps(key) {
		t.Fatal("a pid without its code means an in-flight operation")
	}
	if err := os.WriteFile(filepath.Join(ops, "op-a"+opCodeSuffix), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d.scratchHasUnfinishedOps(key) {
		t.Fatal("a recorded exit code means the operation is done")
	}
	if d.scratchHasUnfinishedOps("proj/other-run") {
		t.Fatal("markers are per-run")
	}
}

// The orphan reaper's activity verdict: only the keep-alive sleeper chain
// reads as idle. Anything else — a running command, a background wrapper —
// keeps the container, and an unparseable listing keeps it too.
func TestKeepAliveProcessesOnly(t *testing.T) {
	idle := "PID USER TIME COMMAND\n" +
		"3652402 root 0:00 /sbin/docker-init -- docker-entrypoint.sh sleep infinity\n" +
		"3652416 root 0:00 sleep infinity\n"
	busy := idle + "3652480 root 0:01 node build.js\n"
	if !keepAliveProcessesOnly(idle) {
		t.Fatal("the sleeper chain alone is idle")
	}
	if keepAliveProcessesOnly(busy) {
		t.Fatal("a process beyond the sleeper chain is work")
	}
	if keepAliveProcessesOnly("") {
		t.Fatal("an unparseable listing must keep the container")
	}
	if keepAliveProcessesOnly("PID USER TIME COMMAND\n") {
		t.Fatal("a header-only listing is unparseable — keep the container")
	}
}
