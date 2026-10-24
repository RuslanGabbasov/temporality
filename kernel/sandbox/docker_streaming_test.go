package sandbox

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// Live console streaming depends on the runner delivering output chunks as
// the command produces them — if docker CLI or the pipe layer buffers until
// process exit, the whole console feature degrades to "everything at once".
// This test pins the incrementality: a slow command must produce several
// onChunk arrivals spread over its runtime, not one burst at the end.
func TestDockerOnOutputStreamsIncrementally(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		if _, err2 := os.Stat(os.ExpandEnv("${HOME}/.docker/run/docker.sock")); err2 != nil {
			t.Skip("docker socket is not available")
		}
	}
	if out, err := exec.Command("docker", "image", "inspect", "temporality-sandbox:latest").CombinedOutput(); err != nil {
		t.Skipf("temporality-sandbox:latest is not built: %s", string(out[:min(len(out), 120)]))
	}
	d := &Docker{Root: t.TempDir(), Binary: "docker", Image: "temporality-sandbox:latest", Memory: "512m", CPUs: "1", PIDs: 100}
	workspace := d.Root + "/projects/demo"
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var arrivals []time.Time
	var chunks []string
	req := Request{
		Workspace:      workspace,
		Command:        []string{"sh", "-c", "for i in 1 2 3 4; do echo tick-$i; sleep 1; done; echo done-final"},
		TimeoutSeconds: 30,
	}
	req.OnOutput = func(stream string, chunk []byte) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		chunks = append(chunks, string(chunk))
		mu.Unlock()
	}
	start := time.Now()
	result, err := d.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(arrivals) == 0 {
		t.Fatalf("no onChunk arrivals at all V2; output=%q exit=%d", result.Output, result.ExitCode)
	}
	// The command takes ~4s (3 sleeps). If streaming worked, the first chunk
	// landed well before the process exited; if it buffered, every arrival
	// sits within ~300ms of the end.
	span := arrivals[len(arrivals)-1].Sub(arrivals[0])
	t.Logf("chunks=%d span=%v total=%v output=%q", len(arrivals), span, time.Since(start), result.Output)
	if span < 1500*time.Millisecond {
		t.Fatalf("onChunk arrivals are bunched at the end (span %v) — the runner buffers until exit", span)
	}
}
