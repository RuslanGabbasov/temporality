package agent

// Real-daemon integration: the retried Background Activity attaches to the
// process attempt 1 started (docs/agent-async-operations.md). The marks file
// proves the command ran exactly once — attach observes, it never re-executes
// (a retried yarn install would corrupt node_modules and burn the registry).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/sandbox"
)

func newBackgroundIntegrationSandbox(t *testing.T) *sandbox.Docker {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("real-daemon test: docker CLI unavailable: %v", err)
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("real-daemon test: docker daemon unavailable: %s", strings.TrimSpace(string(out)))
	}
	image := ""
	for _, candidate := range []string{os.Getenv("SANDBOX_TEST_IMAGE"), os.Getenv("KERNEL_SANDBOX_IMAGE")} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if out, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", candidate).CombinedOutput(); err == nil && strings.TrimSpace(string(out)) != "" {
			image = candidate
			break
		}
	}
	if image == "" {
		if out, err := exec.Command("docker", "images", "--format", "{{.Repository}}:{{.Tag}}").CombinedOutput(); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.HasPrefix(line, "temporality-sandbox") {
					image = line
					break
				}
			}
		}
	}
	if image == "" {
		t.Skip("real-daemon test: no local sandbox image (set SANDBOX_TEST_IMAGE)")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "work"), 0o777); err != nil {
		t.Fatal(err)
	}
	return &sandbox.Docker{Root: root, Image: image, Binary: "docker", Memory: "256m", CPUs: "1", PIDs: 128, ScratchSize: "64m", TmpSize: "64m"}
}

// The retry semantics (docs/agent-async-operations.md): attempt 1 of the
// Background Activity starts the process; every later attempt attaches to it.
// One execution, two resolvers, one recorded result.
func TestRetriedBackgroundActivityAttachesWithoutRerunning(t *testing.T) {
	runner := newBackgroundIntegrationSandbox(t)
	key := "proj/run-attach"
	t.Cleanup(func() { runner.TeardownRun(key) })

	activities := &Activities{Events: &capturedOutbox{}, Sandbox: runner, Console: newTestConsole(1<<20, 100)}
	request := ToolRequest{
		RunID: "run-attach", OperationID: "op-attach", Project: "proj",
		WorkspacePath: filepath.Join(runner.Root, "work"),
		Name:          "run_command",
		Arguments: map[string]any{
			"command":    []any{"sh", "-c", "echo started >> /scratch/marks; sleep 3; echo done >> /scratch/marks; exit 5"},
			"background": true, "timeout_sec": float64(60),
		},
	}

	// Attempt 1's body: the real RunTool detaches the command and watches it.
	type outcome struct {
		result ToolResult
		err    error
	}
	first := make(chan outcome, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		res, err := activities.RunTool(ctx, request)
		first <- outcome{res, err}
	}()

	marksPath := filepath.Join(runner.RunScratchDir(key), "marks")
	deadline := time.Now().Add(15 * time.Second)
	for {
		if raw, err := os.ReadFile(marksPath); err == nil && strings.Contains(string(raw), "started") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background command never started")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The retried attempt's body: attach observes the same operation and
	// resolves with its recorded code — no second execution.
	attached, err := activities.attachBackgroundOperation(t.Context(), request, 60)
	if err != nil {
		t.Fatalf("attach must resolve the running operation: %v", err)
	}
	if attached.ExitCode == nil || *attached.ExitCode != 5 {
		t.Fatalf("attach must report the recorded exit code, got %+v", attached)
	}

	select {
	case out := <-first:
		if out.err != nil {
			t.Fatalf("attempt 1: %v", out.err)
		}
		if out.result.ExitCode == nil || *out.result.ExitCode != 5 {
			t.Fatalf("attempt 1 must report the same code, got %+v", out.result)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("attempt 1 never resolved")
	}

	raw, err := os.ReadFile(marksPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "started") != 1 {
		t.Fatalf("the command must run exactly once — attach never re-executes, marks:\n%s", raw)
	}
}
