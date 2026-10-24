package sandbox

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// Live console streaming debug: a slow command must deliver onChunk
// incrementally. Prints arrivals straight to stdout (visible under -v).
func TestDebugChunkArrivals(t *testing.T) {
	d := &Docker{Root: t.TempDir(), Binary: "docker", Image: "temporality-sandbox:latest", Memory: "512m", CPUs: "1", PIDs: 100}
	workspace := d.Root + "/projects/demo"
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var mu sync.Mutex
	arrivals := 0
	req := Request{
		Workspace:      workspace,
		Command:        []string{"sh", "-c", "for i in 1 2 3 4; do echo tick-$i; sleep 1; done; echo done-final"},
		TimeoutSeconds: 30,
	}
	req.OnOutput = func(stream string, chunk []byte) {
		mu.Lock()
		at := time.Since(start)
		arrivals++
		n := len(chunk)
		mu.Unlock()
		fmt.Printf("CHUNK #%d at %v (%d bytes): %q\n", arrivals, at.Round(time.Millisecond), n, string(chunk))
	}
	result, err := d.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	fmt.Printf("END: arrivals=%d total=%v output=%q\n", arrivals, time.Since(start).Round(time.Millisecond), result.Output)
	if arrivals == 0 {
		t.Fatal("onChunk never fired")
	}
}
