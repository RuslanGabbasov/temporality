package sidecar

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/temporality-project/temporality/aml/coding"
	"github.com/temporality-project/temporality/aml/harness"
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/aml/memory"
)

// Full lifecycle over HTTP with the coding world: session 1 extracts
// experience; session 2 gets hints injected, reuses them and validates.
func TestSidecarCodingLifecycle(t *testing.T) {
	store := memory.NewMemStore()
	server := NewServer(store, WorldCoding, coding.Subsystems, nil)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	task := coding.Tasks3A()[0] // analysis freq
	ctx := context.Background()

	runSession := func(session string) {
		client := NewClient(httpServer.URL, session, "main", coding.RepoName, "main")
		hints := client.TaskStart(ctx, session, task.ID, task.Text)
		if len(hints) == 0 {
			t.Logf("session %s: no hints at start (expected for first session)", session)
		}
		call := llm.ToolCall{ID: "c1", Name: "read_file", Args: map[string]any{"path": "analysis/freq.go"}}
		client.BeforeToolCall(ctx, session, call)
		client.AfterToolCall(ctx, session, call, harness.ToolResult{OK: true, Status: 0, Text: "func TokenFrequency..."})
		testCall := llm.ToolCall{ID: "c2", Name: "shell", Args: map[string]any{"command": "go test ./analysis/..."}}
		client.BeforeToolCall(ctx, session, testCall)
		client.AfterToolCall(ctx, session, testCall, harness.ToolResult{OK: true, Status: 0, Text: "ok"})
		client.TaskEnd(ctx, session, task.ID, true, "analysis/freq.go TokenFrequency go test ./analysis/...")
		stats, ok := client.Stats()
		if !ok {
			t.Fatal("stats must be ready after task end")
		}
		t.Logf("session %s stats: %+v", session, stats)
	}

	runSession("s1")
	assets, _ := store.ListAssets(ctx)
	if len(assets) == 0 {
		t.Fatal("session 1 must extract experience")
	}

	runSession("s2")
	assets, _ = store.ListAssets(ctx)
	var testCmd *memory.Asset
	for _, a := range assets {
		if a.Kind == memory.KindTestCommand {
			testCmd = a
		}
	}
	if testCmd == nil {
		t.Fatal("test-command asset must exist")
	}
	if testCmd.Confidence != memory.WhatWorkedMature1 {
		t.Fatalf("test-command must mature to %v after reuse+merge, got %v", memory.WhatWorkedMature1, testCmd.Confidence)
	}

	// Session 3: the environment evolved (guard commit); the remembered
	// command now fails with test-fail and must be contradicted and weakened.
	client := NewClient(httpServer.URL, "s3", "main", coding.RepoName, "main")
	client.TaskStart(ctx, "s3", task.ID, task.Text)
	testCall := llm.ToolCall{ID: "c1", Name: "shell", Args: map[string]any{"command": "go test ./analysis/..."}}
	client.BeforeToolCall(ctx, "s3", testCall)
	client.AfterToolCall(ctx, "s3", testCall, harness.ToolResult{OK: false, Status: 1, Text: "analysis: refusing to run tests\nFAIL\tgithub.com/blevesearch/bleve/v2/analysis", Cause: memory.CauseTestFail})
	client.TaskEnd(ctx, "s3", task.ID, true, "...")
	stats, _ := client.Stats()
	if stats.Contradicted == 0 {
		t.Fatal("the stale test command must be contradicted after evolution")
	}
	assets, _ = store.ListAssets(ctx)
	for _, a := range assets {
		if a.Kind == memory.KindTestCommand && a.Confidence >= 0.40 {
			t.Fatalf("contradicted test command must be weakened below baseline: %+v", a)
		}
	}
}

// Cross-environment isolation (3C in miniature): a memory confirmed on main
// must not be recalled for a legacy task.
func TestSidecarEnvironmentIsolation(t *testing.T) {
	store := memory.NewMemStore()
	server := NewServer(store, WorldCoding, coding.Subsystems, nil)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	task := coding.Tasks3A()[0]
	ctx := context.Background()

	// Build main memory.
	main := NewClient(httpServer.URL, "m1", "main", coding.RepoName, "main")
	main.TaskStart(ctx, "m1", task.ID, task.Text)
	call := llm.ToolCall{ID: "c1", Name: "shell", Args: map[string]any{"command": "go test ./analysis/..."}}
	main.AfterToolCall(ctx, "m1", call, harness.ToolResult{OK: true, Status: 0, Text: "ok"})
	main.TaskEnd(ctx, "m1", task.ID, true, "...")

	// A legacy task must not receive main's memory.
	legacy := NewClient(httpServer.URL, "l1", "legacy", coding.RepoName, "legacy")
	hints := legacy.TaskStart(ctx, "l1", task.ID, task.Text)
	for _, h := range hints {
		t.Fatalf("cross-environment leak: legacy task received main memory: %s", h.Text)
	}
}
