package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/execution/worker"
	"github.com/temporality-project/temporality/frp/runtime/httpapi"
	"github.com/temporality-project/temporality/frp/substrate/memory"
	"github.com/temporality-project/temporality/frp/world"
)

// TestWriteEffectEndpoint drives the full M12 flow over HTTP: register a
// world that grants filesystem.write, create a write_file execution, let the
// executor worker process it, and verify the physical change plus the
// canonical world.effect event entered the substrate.
func TestWriteEffectEndpoint(t *testing.T) {
	store := memory.New()
	handler := httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	root := t.TempDir()
	register := map[string]any{
		"world_id":      "write-world",
		"state_version": 1,
		"resources":     []map[string]any{{"id": "workspace", "type": "filesystem", "path": root}},
		"capabilities":  []string{"filesystem.read", "filesystem.write"},
		"limits":        map[string]any{"max_read_bytes": 4096, "max_entries": 100, "timeout_sec": 10},
	}
	body, _ := json.Marshal(register)
	if created := serve(handler, http.MethodPost, "/v1/worlds", body); created.Code != http.StatusCreated {
		t.Fatalf("register world: %s", created.Body.String())
	}
	// A capability the world never granted must be rejected at intent time.
	denied := map[string]any{
		"definition": map[string]any{
			"id": "run_command", "execution_mode": "deterministic", "input_schema": map[string]any{},
			"capabilities": []string{"process.execute"},
			"limits":       map[string]any{"timeout_sec": 30, "cpu": 1, "memory_mb": 128, "disk_mb": 64},
			"planner":      map[string]any{}, "failure_policy": map[string]any{"retry_transient": false, "allow_strategy_change": false, "max_retries": 0},
		},
		"episode_id": "ep-write", "world_id": "write-world",
		"arguments": map[string]any{"command": "echo"},
	}
	deniedBody, _ := json.Marshal(denied)
	if response := serve(handler, http.MethodPost, "/v1/executions", deniedBody); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ungranted write capability accepted: %s", response.Body.String())
	}
	create := map[string]any{
		"definition": map[string]any{
			"id": "write_file", "execution_mode": "deterministic", "input_schema": map[string]any{},
			"capabilities": []string{"filesystem.write"},
			"limits":       map[string]any{"timeout_sec": 30, "cpu": 1, "memory_mb": 128, "disk_mb": 64},
			"planner":      map[string]any{}, "failure_policy": map[string]any{"retry_transient": false, "allow_strategy_change": false, "max_retries": 0},
		},
		"episode_id": "ep-write", "branch_id": "br-write", "world_id": "write-world",
		"arguments": map[string]any{"path": "agent-note.md", "content": "# written by agent"},
	}
	createBody, _ := json.Marshal(create)
	response := serve(handler, http.MethodPost, "/v1/executions", createBody)
	if response.Code != http.StatusCreated {
		t.Fatalf("create execution: %s", response.Body.String())
	}
	var created struct {
		Execution execution.Execution `json:"execution"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Execution.WorldID != "write-world" || created.Execution.WorldVersion != 1 {
		t.Fatalf("execution not bound to world: %#v", created.Execution)
	}
	// Run the executor worker in-process against the same store.
	bound := worldFromHTTP(handler, "write-world")
	w := worker.Worker{Store: store, Workflows: world.StandardWorkflows(), Adapter: world.NewAdapter(bound), NewID: func() string { return time.Now().Format("150405.000000000") }, Now: time.Now}
	if _, err := w.RunOnce(context.Background(), "ep-write"); err != nil {
		t.Fatal(err)
	}
	final := serve(handler, http.MethodGet, "/v1/executions/"+created.Execution.ExecutionID, nil)
	if final.Code != http.StatusOK {
		t.Fatalf("get execution: %s", final.Body.String())
	}
	var value execution.Execution
	if err := json.Unmarshal(final.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Status != execution.StatusCompleted {
		t.Fatalf("execution status=%s error=%#v", value.Status, value.Error)
	}
	raw, readErr := os.ReadFile(filepath.Join(root, "agent-note.md"))
	if readErr != nil || string(raw) != "# written by agent" {
		t.Fatalf("effect did not reach the world: %q %v", raw, readErr)
	}
	events := serve(handler, http.MethodGet, "/v1/events?episode_id=ep-write&limit=50", nil)
	if events.Code != http.StatusOK {
		t.Fatalf("list events: %s", events.Body.String())
	}
	var list struct {
		Events []struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		} `json:"events"`
	}
	if err := json.Unmarshal(events.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	effectSeen := false
	for _, event := range list.Events {
		if event.Type != world.EventWorldEffect {
			continue
		}
		effectSeen = true
		if event.Payload["effect_type"] != world.EffectFileWritten {
			t.Fatalf("unexpected effect_type: %#v", event.Payload)
		}
		if event.Payload["execution_id"] != created.Execution.ExecutionID {
			t.Fatalf("effect not linked to execution: %#v", event.Payload)
		}
	}
	if !effectSeen {
		t.Fatal("world.effect event was not committed")
	}
}

func worldFromHTTP(handler http.Handler, id string) world.World {
	response := serve(handler, http.MethodGet, "/v1/worlds/"+id, nil)
	var value world.World
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		panic(err)
	}
	return value
}
