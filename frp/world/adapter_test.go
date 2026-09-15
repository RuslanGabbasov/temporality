package world

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/execution"
)

func adapterWorld(t *testing.T) (World, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	value := World{Protocol: "frp", Version: "0.3", WorldID: "adapter-test", StateVersion: 2, Resources: []Resource{{ID: "workspace", Type: ResourceFilesystem, Path: root}}, Capabilities: []string{"filesystem.read", "git.read", "filesystem.observe"}, Limits: Limits{MaxReadBytes: 16, MaxEntries: 2, TimeoutSec: 5}}
	return value, root
}

func executionErrorClass(err error) (execution.ErrorClass, bool) {
	var normalized *execution.ExecutionError
	if errors.As(err, &normalized) {
		return normalized.Class, true
	}
	return "", false
}

func TestAdapterStatAndList(t *testing.T) {
	value, root := adapterWorld(t)
	adapter := NewAdapter(value)
	result, err := adapter.Observe(context.Background(), execution.Step{Capability: "filesystem.read", Operation: "stat", Input: map[string]any{"path": "src"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservationType != ObservationStat || result.Output["directory"] != true {
		t.Fatalf("unexpected stat result: %#v", result)
	}
	if want := "filesystem:" + root; result.Resource != want {
		t.Fatalf("resource %q want %q", result.Resource, want)
	}
	result, err = adapter.Observe(context.Background(), execution.Step{Capability: "filesystem.read", Operation: "list_dir", Input: map[string]any{"path": "."}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservationType != ObservationDirectoryListing || result.Output["entries"].(int) != 1 {
		t.Fatalf("unexpected listing: %#v", result)
	}
}

func TestAdapterListTruncates(t *testing.T) {
	value, root := adapterWorld(t)
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(root, string(rune('a'+i))+".txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	adapter := NewAdapter(value)
	result, err := adapter.Observe(context.Background(), execution.Step{Capability: "filesystem.read", Operation: "list_dir", Input: map[string]any{"path": "."}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.Output["entries"].(int) != 2 {
		t.Fatalf("limits not enforced: %#v", result)
	}
}

func TestAdapterReadFileTruncates(t *testing.T) {
	value, root := adapterWorld(t)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(strings.Repeat("a", 50)), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(value)
	result, err := adapter.Observe(context.Background(), execution.Step{Capability: "filesystem.read", Operation: "read_file", Input: map[string]any{"path": "big.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.Output["size"].(int) != 16 {
		t.Fatalf("read limits not enforced: %#v", result)
	}
}

func TestAdapterRejectsEscape(t *testing.T) {
	value, _ := adapterWorld(t)
	adapter := NewAdapter(value)
	_, err := adapter.Observe(context.Background(), execution.Step{Capability: "filesystem.read", Operation: "read_file", Input: map[string]any{"path": "../../etc/hosts"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("escape not denied: %v", err)
	}
	_, err = adapter.Observe(context.Background(), execution.Step{Capability: "filesystem.read", Operation: "read_file", Input: map[string]any{"path": "/etc/hosts"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("absolute escape not denied: %v", err)
	}
}

func TestAdapterRejectsUngrantedCapability(t *testing.T) {
	value, _ := adapterWorld(t)
	adapter := NewAdapter(value)
	_, err := adapter.Observe(context.Background(), execution.Step{Capability: "http.read", Operation: "get", Input: map[string]any{"url": "https://example.com"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("ungranted capability not denied: %v", err)
	}
}

func TestAdapterEnvironment(t *testing.T) {
	value, _ := adapterWorld(t)
	adapter := NewAdapter(value)
	result, err := adapter.Observe(context.Background(), execution.Step{Capability: "filesystem.observe", Operation: "inspect", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	names := result.Output["env_var_names"].([]string)
	for _, name := range names {
		if strings.ContainsRune(name, '=') {
			t.Fatalf("environment leaked values: %q", name)
		}
	}
	if result.ObservationType != ObservationEnvironment {
		t.Fatalf("unexpected observation: %#v", result)
	}
}

func TestAdapterGitObservations(t *testing.T) {
	repo := buildTestRepository(t)
	value := World{Protocol: "frp", Version: "0.3", WorldID: "git-world", StateVersion: 1, Resources: []Resource{{ID: "repo", Type: ResourceGitRepository, Path: repo}}, Capabilities: []string{"git.read"}, Limits: Limits{MaxReadBytes: 1024, MaxEntries: 100, TimeoutSec: 5}}
	adapter := NewAdapter(value)
	result, err := adapter.Observe(context.Background(), execution.Step{Capability: "git.read", Operation: "status", Input: map[string]any{"path": "."}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservationType != ObservationGitStatus {
		t.Fatalf("unexpected observation: %#v", result)
	}
	status := result.Output["status"].(Status)
	if status.Branch != "main" || status.Staged != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	result, err = adapter.Observe(context.Background(), execution.Step{Capability: "git.read", Operation: "log", Input: map[string]any{"path": ".", "limit": float64(5)}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservationType != ObservationGitLog || result.Truncated {
		t.Fatalf("unexpected log: %#v", result)
	}
}

func TestAdapterHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))
	defer server.Close()
	value := World{Protocol: "frp", Version: "0.3", WorldID: "http-world", StateVersion: 1, Resources: []Resource{{ID: "api", Type: ResourceHTTPEndpoint, Endpoint: server.URL}}, Capabilities: []string{"http.read"}, Limits: Limits{MaxReadBytes: 1024, MaxEntries: 10, TimeoutSec: 5}}
	adapter := NewAdapter(value)
	result, err := adapter.Observe(context.Background(), execution.Step{Capability: "http.read", Operation: "get", Input: map[string]any{"url": server.URL + "/health"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservationType != ObservationHTTPResponse || result.Output["body"].(string) != "pong" {
		t.Fatalf("unexpected http result: %#v", result)
	}
	_, err = adapter.Observe(context.Background(), execution.Step{Capability: "http.read", Operation: "get", Input: map[string]any{"url": "https://evil.example.com/health"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("undeclared endpoint allowed: %v", err)
	}
}

func TestAdapterExecuteSatisfiesWorkerContract(t *testing.T) {
	value, root := adapterWorld(t)
	adapter := NewAdapter(value)
	output, err := adapter.Execute(context.Background(), execution.Step{Capability: "filesystem.read", Operation: "stat", Input: map[string]any{"path": "."}})
	if err != nil || output["exists"] != true {
		t.Fatalf("worker contract broken: %#v %v", output, err)
	}
	_ = root
}

func fixedTime() time.Time { return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) }

func TestObservationEvent(t *testing.T) {
	value, _ := adapterWorld(t)
	event, err := ObservationEvent(value, "exec-1", "list_files", Observation{Resource: "filesystem:/tmp/x", ObservationType: ObservationDirectoryListing, Payload: map[string]any{"entries": 3}}, "event-1", fixedTime(), "episode-1", "branch-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != EventWorldObservation || event.Payload["world_id"] != value.WorldID || event.Payload["execution_id"] != "exec-1" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if _, ok := event.Payload["intent_world_version"]; ok {
		t.Fatal("matching intent version must not be recorded")
	}
	event, err = ObservationEvent(value, "exec-1", "list_files", Observation{Resource: "filesystem:/tmp/x", ObservationType: ObservationDirectoryListing, Payload: map[string]any{}}, "event-2", fixedTime(), "episode-1", "branch-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if event.Payload["intent_world_version"] != 1 {
		t.Fatalf("diverged intent version not recorded: %#v", event.Payload)
	}
	if _, err = ObservationEvent(value, "exec-1", "list_files", Observation{Resource: "", ObservationType: "", Payload: map[string]any{}}, "event-3", fixedTime(), "episode-1", "branch-1", 0); err == nil {
		t.Fatal("invalid observation accepted")
	}
}
