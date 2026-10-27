package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The container-per-run lifecycle is pinned with the same fake docker CLI the
// one-shot tests use: every invocation appends its argv to $SANDBOX_TEST_LOG,
// and the tests assert on which docker subcommands ran — not on the daemon.

func writeReuseRunner(t *testing.T, script string) (*Docker, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "docker.log")
	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SANDBOX_TEST_LOG\"\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOX_TEST_LOG", logPath)
	// idleTTL stays zero for directly-constructed runners: no reaper goroutine
	// can race the log assertions.
	runner := &Docker{Root: root, Image: "sandbox@sha256:" + strings.Repeat("a", 64), Binary: binary, Memory: "1g", CPUs: "2", PIDs: 128, UID: os.Getuid(), GID: os.Getgid()}
	return runner, logPath
}

func reuseLog(t *testing.T, logPath string) string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func countLogLines(log, prefix string) int {
	count := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

// runContainerNames extracts the --name of every `docker run` the log holds.
func runContainerNames(log string) []string {
	var names []string
	for _, line := range strings.Split(log, "\n") {
		if !strings.HasPrefix(line, "run ") {
			continue
		}
		fields := strings.Fields(line)
		for i, field := range fields {
			if field == "--name" && i+1 < len(fields) {
				names = append(names, fields[i+1])
			}
		}
	}
	return names
}

func reuseRequest(workspace, key string, command ...string) Request {
	return Request{Workspace: workspace, Command: command, ScratchKey: key}
}

// (а) Two commands of one run: exactly one `run -d` creates the keep-alive
// container, both commands exec into it, nothing is removed in between.
func TestReuseKeepsOneContainerAcrossCommandsOfOneRun(t *testing.T) {
	runner, logPath := writeReuseRunner(t, "case \"$1\" in\nrun) echo cid;;\nexec) echo exec-output;;\nrm) exit 0;;\nesac\nexit 0\n")
	request := reuseRequest(filepath.Join(runner.Root, "repo"), "proj/run-1", "printf", "ok")
	for i := 0; i < 2; i++ {
		result, err := runner.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("execute #%d: %v", i+1, err)
		}
		if !strings.Contains(result.Output, "exec-output") {
			t.Fatalf("execute #%d must run through exec, got %q", i+1, result.Output)
		}
	}
	log := reuseLog(t, logPath)
	if got := countLogLines(log, "run -d "); got != 1 {
		t.Fatalf("run -d invocations = %d, want 1\n%s", got, log)
	}
	if got := countLogLines(log, "exec "); got != 2 {
		t.Fatalf("exec invocations = %d, want 2\n%s", got, log)
	}
	if got := countLogLines(log, "rm "); got != 0 {
		t.Fatalf("no container may be removed between commands of one run, got %d\n%s", got, log)
	}
	// The execs went into the very container the run created.
	names := runContainerNames(log)
	if len(names) != 1 {
		t.Fatalf("expected one container name, got %v", names)
	}
	execLines := 0
	for _, line := range strings.Split(log, "\n") {
		if !strings.HasPrefix(line, "exec ") {
			continue
		}
		execLines++
		if !strings.Contains(line, names[0]) {
			t.Fatalf("exec must target the run container %q: %s", names[0], line)
		}
		// What used to be run flags and is per-command must be restated here.
		if !strings.Contains(line, "--workdir /workspace") || !strings.Contains(line, "--user") {
			t.Fatalf("exec must carry workdir and user: %s", line)
		}
	}
	if execLines != 2 {
		t.Fatalf("exec lines = %d, want 2", execLines)
	}
}

// (б) Different runs never share a container.
func TestReuseSeparatesContainersPerRun(t *testing.T) {
	runner, logPath := writeReuseRunner(t, "case \"$1\" in\nrun) echo cid;;\nexec) echo exec-output;;\nrm) exit 0;;\nesac\nexit 0\n")
	workspace := filepath.Join(runner.Root, "repo")
	for _, key := range []string{"proj/run-1", "proj/run-2"} {
		if _, err := runner.Execute(context.Background(), reuseRequest(workspace, key, "printf", "ok")); err != nil {
			t.Fatal(err)
		}
	}
	log := reuseLog(t, logPath)
	names := runContainerNames(log)
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("two runs must get two distinct containers, got %v\n%s", names, log)
	}
	if got := countLogLines(log, "rm "); got != 0 {
		t.Fatalf("live run containers must not be removed, got %d rm calls\n%s", got, log)
	}
}

// (в) A timed-out exec cannot be killed alone (docker exec has no kill
// switch): the container is stopped and evicted, and the next command
// recreates from scratch.
func TestReuseTimeoutStopsAndEvicts(t *testing.T) {
	runner, logPath := writeReuseRunner(t, "case \"$1\" in\nrun) echo cid;;\nexec) exec sleep 3;;\nrm) exit 0;;\nesac\nexit 0\n")
	workspace := filepath.Join(runner.Root, "repo")
	request := reuseRequest(workspace, "proj/run-1", "sleep", "3")
	request.TimeoutSeconds = 1
	_, err := runner.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "exceeded 1 second timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	log := reuseLog(t, logPath)
	firstNames := runContainerNames(log)
	if len(firstNames) != 1 {
		t.Fatalf("expected one container before the timeout, got %v", firstNames)
	}
	if !strings.Contains(log, "rm --force "+firstNames[0]) {
		t.Fatalf("timed-out container must be rm --force'd\n%s", log)
	}
	if len(runner.runs) != 0 {
		t.Fatalf("timed-out container must leave the registry, got %v", runner.runs)
	}
	// The next command of the run recreates: a fresh `run -d`, new name.
	if _, err := runner.Execute(context.Background(), request); err == nil {
		t.Fatal("second command also runs into the scripted sleep and must time out")
	}
	log = reuseLog(t, logPath)
	names := runContainerNames(log)
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("command after a timeout must recreate the container, got %v\n%s", names, log)
	}
}

// (г) An exec against a container killed outside our control: daemon refusal
// text, one inspect confirms the death, then exactly one recreate-and-retry.
func TestReuseRecreatesDeadContainerOnce(t *testing.T) {
	done := filepath.Join(t.TempDir(), "exec-done")
	runner, logPath := writeReuseRunner(t, "case \"$1\" in\nrun) exit 0;;\nexec) if [ ! -f \"$SANDBOX_EXEC_DONE\" ]; then touch \"$SANDBOX_EXEC_DONE\"; echo 'Error response from daemon: Container deadbeef is not running'; exit 125; fi; echo recovered; exit 0;;\ninspect) echo 'Error response from daemon: No such container: deadbeef'; exit 1;;\nrm) exit 0;;\nesac\nexit 0\n")
	t.Setenv("SANDBOX_EXEC_DONE", done)
	workspace := filepath.Join(runner.Root, "repo")
	result, err := runner.Execute(context.Background(), reuseRequest(workspace, "proj/run-1", "printf", "ok"))
	if err != nil {
		t.Fatalf("recreate-and-retry must recover transparently: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Output, "recovered") {
		t.Fatalf("retry result = %#v", result)
	}
	log := reuseLog(t, logPath)
	names := runContainerNames(log)
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("dead container must be recreated exactly once, got %v\n%s", names, log)
	}
	if countLogLines(log, "rm ") == 0 {
		t.Fatalf("dead container must be evicted before the recreate\n%s", log)
	}
	if got := countLogLines(log, "inspect "); got != 1 {
		t.Fatalf("exactly one inspect must confirm the death, got %d\n%s", got, log)
	}
}

// A command that merely PRINTS the daemon refusal phrasing (a grep over logs,
// say) must not be rerun: the inspect confirm sees a healthy container.
func TestReusePrintedRefusalTextIsNotRerun(t *testing.T) {
	runner, logPath := writeReuseRunner(t, "case \"$1\" in\nrun) echo cid;;\nexec) echo 'ERROR: no such container in the logs'; exit 1;;\ninspect) echo true;;\nrm) exit 0;;\nesac\nexit 0\n")
	workspace := filepath.Join(runner.Root, "repo")
	result, err := runner.Execute(context.Background(), reuseRequest(workspace, "proj/run-1", "grep", "is not running"))
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 || !strings.Contains(result.Output, "no such container") {
		t.Fatalf("command failure must surface as a result: %#v", result)
	}
	log := reuseLog(t, logPath)
	if got := countLogLines(log, "run -d "); got != 1 {
		t.Fatalf("a printed refusal phrase must not trigger a recreate, run -d calls = %d\n%s", got, log)
	}
	if got := countLogLines(log, "exec "); got != 1 {
		t.Fatalf("command must not be rerun, exec calls = %d\n%s", got, log)
	}
}

// A refused `docker run -d` (missing image etc.) keeps the one-shot error
// shape: daemon text as a failed command result, not an Execute error.
func TestReuseCreateFailureKeepsResultShape(t *testing.T) {
	runner, logPath := writeReuseRunner(t, "case \"$1\" in\nrun) echo 'docker: Error response from daemon: No such image: sandbox@sha256:a' >&2; exit 125;;\nesac\nexit 0\n")
	workspace := filepath.Join(runner.Root, "repo")
	result, err := runner.Execute(context.Background(), reuseRequest(workspace, "proj/run-1", "printf", "ok"))
	if err != nil {
		t.Fatalf("daemon refusal must ride as a result, got error: %v", err)
	}
	if result.ExitCode != 125 || !strings.Contains(result.Output, "No such image") {
		t.Fatalf("creation refusal must surface with daemon text and exit code: %#v", result)
	}
	if countLogLines(reuseLog(t, logPath), "exec ") != 0 {
		t.Fatal("no exec may run against a container that never existed")
	}
}

// Injected env (secrets) is per-command: it rides on exec --env and never
// bakes into the container the whole run shares.
func TestReuseSecretsRideExecNotRun(t *testing.T) {
	runner, logPath := writeReuseRunner(t, "case \"$1\" in\nrun) echo cid;;\nexec) echo exec-output;;\nrm) exit 0;;\nesac\nexit 0\n")
	workspace := filepath.Join(runner.Root, "repo")
	request := reuseRequest(workspace, "proj/run-1", "printf", "ok")
	request.Env = []string{"SECRET_TOKEN=shh"}
	if _, err := runner.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	log := reuseLog(t, logPath)
	for _, line := range strings.Split(log, "\n") {
		if !strings.HasPrefix(line, "run ") {
			continue
		}
		if strings.Contains(line, "SECRET_TOKEN") {
			t.Fatalf("per-command secrets must not bake into the container: %s", line)
		}
		if !strings.Contains(line, "--env HOME=/scratch") {
			t.Fatalf("the baked HOME/TMPDIR env must stay on the run: %s", line)
		}
	}
	if got := countLogLines(log, "exec "); got != 1 {
		t.Fatalf("exec calls = %d, want 1", got)
	}
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "exec ") && !strings.Contains(line, "--env SECRET_TOKEN=shh") {
			t.Fatalf("exec must carry the per-command env: %s", line)
		}
	}
}

// A run that changes shape mid-way (writable build step, then read-only
// review step) must not exec into a container created for another shape.
func TestReuseFingerprintFlipRecreates(t *testing.T) {
	runner, logPath := writeReuseRunner(t, "case \"$1\" in\nrun) echo cid;;\nexec) echo exec-output;;\nrm) exit 0;;\nesac\nexit 0\n")
	workspace := filepath.Join(runner.Root, "repo")
	if _, err := runner.Execute(context.Background(), reuseRequest(workspace, "proj/run-1", "go", "build")); err != nil {
		t.Fatal(err)
	}
	readOnly := reuseRequest(workspace, "proj/run-1", "go", "vet")
	readOnly.ReadOnly = true
	if _, err := runner.Execute(context.Background(), readOnly); err != nil {
		t.Fatal(err)
	}
	log := reuseLog(t, logPath)
	names := runContainerNames(log)
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("a shape flip must recreate the container, got %v\n%s", names, log)
	}
	if !strings.Contains(log, "rm --force "+names[0]) {
		t.Fatalf("the stale container must be stopped: %s", log)
	}
}

// TeardownRun is the explicit cleanup: it stops the container, empties the
// registry slot, and the next command starts a fresh container.
func TestReuseTeardownRunDropsContainer(t *testing.T) {
	runner, logPath := writeReuseRunner(t, "case \"$1\" in\nrun) echo cid;;\nexec) echo exec-output;;\nrm) exit 0;;\nesac\nexit 0\n")
	workspace := filepath.Join(runner.Root, "repo")
	if _, err := runner.Execute(context.Background(), reuseRequest(workspace, "proj/run-1", "printf", "ok")); err != nil {
		t.Fatal(err)
	}
	// An unknown key is a no-op.
	runner.TeardownRun("proj/unknown")
	if len(runner.runs) != 1 {
		t.Fatalf("unknown key must not touch the registry: %v", runner.runs)
	}
	runner.TeardownRun("proj/run-1")
	if len(runner.runs) != 0 {
		t.Fatalf("teardown must empty the registry slot: %v", runner.runs)
	}
	log := reuseLog(t, logPath)
	names := runContainerNames(log)
	if !strings.Contains(log, "rm --force "+names[0]) {
		t.Fatalf("teardown must stop the run container: %s", log)
	}
	if _, err := runner.Execute(context.Background(), reuseRequest(workspace, "proj/run-1", "printf", "ok")); err != nil {
		t.Fatal(err)
	}
	names = runContainerNames(reuseLog(t, logPath))
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("command after teardown must recreate, got %v", names)
	}
}

// (д) The reaper's decision as a pure selection: only stale idle entries are
// picked; fresh ones and busy ones stay.
func TestIdleContainersPicksOnlyStaleIdle(t *testing.T) {
	runner := &Docker{idleTTL: 15 * time.Minute}
	now := time.Now()
	runner.runs = map[string]*runContainer{
		"proj/stale":  {key: "proj/stale", name: "c-stale", lastUsed: now.Add(-time.Hour)},
		"proj/fresh":  {key: "proj/fresh", name: "c-fresh", lastUsed: now.Add(-time.Minute)},
		"proj/busy":   {key: "proj/busy", name: "c-busy", lastUsed: now.Add(-2 * time.Hour), busy: 1},
		"proj/border": {key: "proj/border", name: "c-border", lastUsed: now.Add(-15 * time.Minute)},
	}
	var keys []string
	for _, rc := range runner.idleContainers(now) {
		keys = append(keys, rc.key)
	}
	slices.Sort(keys)
	want := []string{"proj/stale"}
	if !slices.Equal(keys, want) {
		t.Fatalf("idle picks = %v, want %v (fresh stays, busy stays, exact TTL boundary stays)", keys, want)
	}
	if len(runner.runs) != 3 {
		t.Fatalf("picked entries must leave the registry, got %v", runner.runs)
	}
}

// The reaper's action: an aged container is rm --force'd and the slot empties.
func TestReaperRemovesIdleContainer(t *testing.T) {
	runner, logPath := writeReuseRunner(t, "case \"$1\" in\nrun) echo cid;;\nexec) echo exec-output;;\nrm) exit 0;;\nesac\nexit 0\n")
	workspace := filepath.Join(runner.Root, "repo")
	if _, err := runner.Execute(context.Background(), reuseRequest(workspace, "proj/run-1", "printf", "ok")); err != nil {
		t.Fatal(err)
	}
	runner.runMu.Lock()
	for _, rc := range runner.runs {
		rc.lastUsed = time.Now().Add(-time.Hour)
	}
	runner.runMu.Unlock()
	runner.reapIdle()
	log := reuseLog(t, logPath)
	names := runContainerNames(log)
	if len(names) != 1 || !strings.Contains(log, "rm --force "+names[0]) {
		t.Fatalf("reaper must stop the aged container: %s", log)
	}
	if len(runner.runs) != 0 {
		t.Fatalf("reaper must empty the registry: %v", runner.runs)
	}
}
