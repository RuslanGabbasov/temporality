package sandbox

// Real-daemon integration tests for Background Operations Recovery
// (docs/agent-async-operations.md): recovery after a kernel restart, attach
// without re-execution, the orphan reaper protecting active work, kill parity
// and honest lost/timeout outcomes. They skip when no docker daemon or no
// local sandbox image is available — CI has neither.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newIntegrationDocker returns a runner against the REAL docker daemon using
// a locally available sandbox image. Skips when the daemon or the image is
// unavailable.
func newIntegrationDocker(t *testing.T) *Docker {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("real-daemon test: docker CLI unavailable: %v", err)
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("real-daemon test: docker daemon unavailable: %s", strings.TrimSpace(string(out)))
	}
	image := integrationImage(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "work"), 0o777); err != nil {
		t.Fatal(err)
	}
	return &Docker{Root: root, Image: image, Binary: "docker", Memory: "256m", CPUs: "1", PIDs: 128, ScratchSize: "64m", TmpSize: "64m"}
}

// integrationImage picks the sandbox image for real-daemon tests:
// SANDBOX_TEST_IMAGE, then KERNEL_SANDBOX_IMAGE, then the onboarding-built
// temporality-sandbox* image.
func integrationImage(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{os.Getenv("SANDBOX_TEST_IMAGE"), os.Getenv("KERNEL_SANDBOX_IMAGE")} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if out, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", candidate).CombinedOutput(); err == nil && strings.TrimSpace(string(out)) != "" {
			return candidate
		}
	}
	if out, err := exec.Command("docker", "images", "--format", "{{.Repository}}:{{.Tag}}").CombinedOutput(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasPrefix(line, "temporality-sandbox") {
				return line
			}
		}
	}
	t.Skip("real-daemon test: no local sandbox image (set SANDBOX_TEST_IMAGE)")
	return ""
}

// backgroundRequest builds a detached request exactly the way RunTool does:
// the bookkeeping wrapper, the operation marker and the run-scoped scratch.
// The key is unique per test — the docker daemon is shared, and two
// containers claiming one label would make recovery ambiguous.
func backgroundRequest(t *testing.T, d *Docker, opID string, command ...string) Request {
	return Request{
		Workspace:      filepath.Join(d.Root, "work"),
		Command:        OpShellWrapper(opID, command),
		TimeoutSeconds: 60,
		Background:     true,
		ScratchKey:     "proj/run-" + strings.ToLower(t.Name()),
		Env:            []string{OpMarkerEnv(opID)},
	}
}

// startOperation detaches a command and waits for the wrapper's pid marker:
// the command is really running when the helper returns. The channel carries
// the executing side's outcome.
func startOperation(t *testing.T, d *Docker, opID string, command ...string) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_, err := d.Execute(ctx, backgroundRequest(t, d, opID, command...))
		done <- err
	}()
	deadline := time.Now().Add(15 * time.Second)
	for !d.scratchHasUnfinishedOps(backgroundRequest(t, d, opID).ScratchKey) {
		if time.Now().After(deadline) {
			t.Fatal("the background operation never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return done
}

func readScratchFile(t *testing.T, d *Docker, key, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.runScratchDir(key), name))
	if err != nil {
		t.Fatalf("read scratch %s: %v", name, err)
	}
	return string(raw)
}

func hasKey(refs []runContainerRef, key string) bool {
	for _, ref := range refs {
		if ref.key == key {
			return true
		}
	}
	return false
}

// ТЗ matrix «Перезапуск kernel при работающей background-команде»: the
// command keeps running, the "restarted" kernel (a fresh runner over the same
// root — the in-memory registry is empty) regains access through the
// container's durable labels, and the result is the RECORDED exit code. The
// marks file proves the command ran exactly once: recovery observes, it never
// re-executes.
func TestBackgroundOpSurvivesRunnerRestart(t *testing.T) {
	d1 := newIntegrationDocker(t)
	key := backgroundRequest(t, d1, "op").ScratchKey
	done := startOperation(t, d1, "op-restart", "/bin/sh", "-c", "echo started >> /scratch/marks; sleep 15; echo done >> /scratch/marks; exit 7")
	t.Cleanup(func() { d1.TeardownRun(key) })

	d2 := &Docker{Root: d1.Root, Image: d1.Image, Binary: d1.Binary, Memory: d1.Memory, CPUs: d1.CPUs, PIDs: d1.PIDs, ScratchSize: d1.ScratchSize, TmpSize: d1.TmpSize}

	// The command must still be running — polled, because a loaded daemon
	// answers probes slowly.
	var state OperationState
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, err = d2.OperationState(key, "op-restart")
		if err != nil {
			t.Fatalf("the restarted kernel must reach the running operation: %v", err)
		}
		if state.Running {
			if state.StartedAt.IsZero() {
				t.Fatalf("the operation start must be recoverable from the markers, got %+v", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the command must still be running, got %+v", state)
		}
		time.Sleep(200 * time.Millisecond)
	}

	deadline = time.Now().Add(30 * time.Second)
	for {
		state, err = d2.OperationState(key, "op-restart")
		if err != nil {
			t.Fatalf("state probe: %v", err)
		}
		if state.Finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the operation never recorded its exit code")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if state.ExitCode != 7 {
		t.Fatalf("the recorded exit code must win, got %+v", state)
	}
	if err := <-done; err != nil {
		t.Fatalf("the executing side: %v", err)
	}
	marks := readScratchFile(t, d1, key, "marks")
	if strings.Count(marks, "started") != 1 || strings.Count(marks, "done") != 1 {
		t.Fatalf("the command must run exactly once across the restart, marks:\n%s", marks)
	}
}

// ТЗ matrix «Запуск reaper после рестарта kernel»: the orphan reaper must not
// remove a container hosting a running background operation — absence from
// the in-memory registry is not the verdict. And once the work is gone, the
// same reaper still removes the abandoned container.
func TestReaperKeepsActiveWorkAndRemovesAbandoned(t *testing.T) {
	d1 := newIntegrationDocker(t)
	key := backgroundRequest(t, d1, "op").ScratchKey
	done := startOperation(t, d1, "op-reaper", "/bin/sh", "-c", "sleep 10")
	t.Cleanup(func() { d1.TeardownRun(key) })

	// A "restarted kernel" with a hyper-aggressive reaper: the age gate
	// passes for every container after a millisecond.
	d2 := &Docker{Root: d1.Root, Image: d1.Image, Binary: d1.Binary, Memory: d1.Memory, CPUs: d1.CPUs, PIDs: d1.PIDs}
	d2.idleTTL = time.Millisecond

	// RunningFor reads in seconds — give the age gate a parseable value.
	time.Sleep(2100 * time.Millisecond)
	d2.reapIdle()
	refs, err := d2.listRunContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasKey(refs, key) {
		t.Fatal("the reaper must keep a container that hosts a running background operation")
	}

	if err := <-done; err != nil {
		t.Fatalf("the executing side: %v", err)
	}
	// The operation recorded its result and nothing runs: a truly abandoned
	// container must still be removable.
	deadline := time.Now().Add(30 * time.Second)
	for {
		d2.reapIdle()
		refs, err = d2.listRunContainers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !hasKey(refs, key) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the abandoned container was never reaped")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ТЗ matrix «cancellation»: kill_operation must stay reachable after a kernel
// restart (durable identity), and a deliberate kill reads as a finished (137)
// operation — never as lost/unknown.
func TestKillOperationAfterRestartRecordsExitCode(t *testing.T) {
	d1 := newIntegrationDocker(t)
	key := backgroundRequest(t, d1, "op").ScratchKey
	done := startOperation(t, d1, "op-kill", "/bin/sh", "-c", "sleep 30")
	t.Cleanup(func() { d1.TeardownRun(key) })

	d2 := &Docker{Root: d1.Root, Image: d1.Image, Binary: d1.Binary, Memory: d1.Memory, CPUs: d1.CPUs, PIDs: d1.PIDs}
	killed, err := d2.KillOperation(key, "op-kill")
	if err != nil || !killed {
		t.Fatalf("the restarted kernel must reach the process tree: killed=%v err=%v", killed, err)
	}
	state, err := d2.OperationState(key, "op-kill")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Finished || state.ExitCode != 137 {
		t.Fatalf("a deliberate kill records 137 — never lost/unknown, got %+v", state)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the executing side: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the killed operation never resolved")
	}
}

// ТЗ matrix «Потеря контейнера или Docker daemon»: the probe must surface an
// error — never a fabricated result the workflow could book as success.
func TestLostContainerReportsNoFalseSuccess(t *testing.T) {
	d1 := newIntegrationDocker(t)
	key := backgroundRequest(t, d1, "op").ScratchKey
	done := startOperation(t, d1, "op-lost", "/bin/sh", "-c", "sleep 30")
	t.Cleanup(func() { d1.TeardownRun(key) })

	refs, err := d1.listRunContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	name := ""
	for _, ref := range refs {
		if ref.key == key {
			name = ref.name
		}
	}
	if name == "" {
		t.Fatal("the operation's container must exist")
	}
	if out, err := exec.Command("docker", "rm", "--force", name).CombinedOutput(); err != nil {
		t.Fatalf("rm: %s", out)
	}

	state, err := d1.OperationState(key, "op-lost")
	if err == nil {
		t.Fatalf("a lost container must surface as an error, got %+v", state)
	}
	if state.Finished {
		t.Fatalf("no exit code may be invented for a lost operation, got %+v", state)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the executing side never resolved after the container died")
	}
}

// ТЗ «сохранение корректного поведения при timeout»: the budget kill stays a
// typed timeout error, and the command is never re-run after it.
func TestBackgroundCommandTimeoutIsTyped(t *testing.T) {
	d := newIntegrationDocker(t)
	key := backgroundRequest(t, d, "op").ScratchKey
	req := backgroundRequest(t, d, "op-timeout", "/bin/sh", "-c", "echo started >> /scratch/marks; sleep 30")
	req.TimeoutSeconds = 2
	t.Cleanup(func() { d.TeardownRun(key) })

	_, err := d.Execute(context.Background(), req)
	var timeoutErr *CommandTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("a timed-out command must report CommandTimeoutError, got %v", err)
	}
	marks := readScratchFile(t, d, key, "marks")
	if strings.Count(marks, "started") != 1 {
		t.Fatalf("the command must not be re-run after its timeout, marks:\n%s", marks)
	}
}
