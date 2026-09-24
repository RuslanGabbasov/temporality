package world

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/execution"
)

func writeWorld(t *testing.T) (World, string) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	value := World{
		Protocol: "frp", Version: "0.3", WorldID: "effect-test", StateVersion: 2,
		Resources:    []Resource{{ID: "workspace", Type: ResourceFilesystem, Path: root}},
		Capabilities: []string{"filesystem.read", "filesystem.write", "process.execute", "git.read", "git.write", "http.read", "http.write"},
		Limits:       Limits{MaxReadBytes: 4096, MaxEntries: 100, TimeoutSec: 5},
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	return value, root
}

func TestEffectWriteAndCreateFile(t *testing.T) {
	value, root := writeWorld(t)
	adapter := NewAdapter(value)
	result, err := adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "write_file", Input: map[string]any{"path": "notes/a.txt", "content": "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectType != EffectFileWritten || result.ObservationType != "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	raw, readErr := os.ReadFile(filepath.Join(root, "notes", "a.txt"))
	if readErr != nil || string(raw) != "hello" {
		t.Fatalf("file not written: %q %v", raw, readErr)
	}
	if result.Output["overwritten"] != false {
		t.Fatalf("expected create semantics: %#v", result.Output)
	}
	result, err = adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "write_file", Input: map[string]any{"path": "notes/a.txt", "content": "changed"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["overwritten"] != true {
		t.Fatalf("expected overwrite semantics: %#v", result.Output)
	}
	_, err = adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "create_file", Input: map[string]any{"path": "notes/a.txt", "content": "x"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorInvalidResult {
		t.Fatalf("create_file did not refuse existing file: %v", err)
	}
}

func TestEffectPatchFile(t *testing.T) {
	value, root := writeWorld(t)
	target := filepath.Join(root, "config.txt")
	if err := os.WriteFile(target, []byte("alpha beta alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(value)
	result, err := adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "patch_file", Input: map[string]any{"path": "config.txt", "find": "alpha", "replace": "gamma"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectType != EffectFilePatched || result.Output["replacements"].(int) != 1 {
		t.Fatalf("unexpected patch result: %#v", result)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "gamma beta alpha" {
		t.Fatalf("first occurrence not replaced: %q", raw)
	}
	result, err = adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "patch_file", Input: map[string]any{"path": "config.txt", "find": "alpha", "replace": "delta", "all": true}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["replacements"].(int) != 1 {
		t.Fatalf("expected single remaining replacement: %#v", result)
	}
	_, err = adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "patch_file", Input: map[string]any{"path": "config.txt", "find": "missing", "replace": "x"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorInvalidResult {
		t.Fatalf("absent pattern not rejected: %v", err)
	}
}

func TestEffectMoveAndDelete(t *testing.T) {
	value, root := writeWorld(t)
	source := filepath.Join(root, "a.txt")
	if err := os.WriteFile(source, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(value)
	result, err := adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "move_file", Input: map[string]any{"path": "a.txt", "target": "nested/b.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectType != EffectFileMoved {
		t.Fatalf("unexpected move result: %#v", result)
	}
	if _, statErr := os.Stat(source); !os.IsNotExist(statErr) {
		t.Fatal("source still exists after move")
	}
	if raw, readErr := os.ReadFile(filepath.Join(root, "nested", "b.txt")); readErr != nil || string(raw) != "data" {
		t.Fatalf("move target missing: %v", readErr)
	}
	result, err = adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "delete_file", Input: map[string]any{"path": "nested/b.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectType != EffectFileDeleted {
		t.Fatalf("unexpected delete result: %#v", result)
	}
	_, err = adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "delete_file", Input: map[string]any{"path": "."}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("root deletion not denied: %v", err)
	}
}

func TestEffectCreateDir(t *testing.T) {
	value, root := writeWorld(t)
	adapter := NewAdapter(value)
	result, err := adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "create_dir", Input: map[string]any{"path": "deep/nested/dir"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectType != EffectDirCreated {
		t.Fatalf("unexpected dir result: %#v", result)
	}
	if info, statErr := os.Stat(filepath.Join(root, "deep", "nested", "dir")); statErr != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", statErr)
	}
}

func TestEffectWriteRejectsEscapeAndReadOnly(t *testing.T) {
	value, _ := writeWorld(t)
	readOnly := t.TempDir() // deliberately outside the writable workspace root
	value.Resources = append(value.Resources, Resource{ID: "frozen", Type: ResourceFilesystem, Path: readOnly, ReadOnly: true})
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(value)
	_, err := adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "write_file", Input: map[string]any{"path": "../../etc/hosts", "content": "x"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("escape not denied: %v", err)
	}
	_, err = adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "write_file", Input: map[string]any{"path": readOnly + "/x.txt", "content": "x"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("read-only resource write not denied: %v", err)
	}
}

func TestEffectWriteRejectsOversizedContent(t *testing.T) {
	value, _ := writeWorld(t)
	adapter := NewAdapter(value)
	_, err := adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "write_file", Input: map[string]any{"path": "big.txt", "content": strings.Repeat("x", 4097)}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorResourceExhausted {
		t.Fatalf("oversized content accepted: %v", err)
	}
}

func TestEffectRejectsUngrantedWriteCapability(t *testing.T) {
	value, _ := writeWorld(t)
	value.Capabilities = []string{"filesystem.read"}
	adapter := NewAdapter(value)
	_, err := adapter.Effect(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "write_file", Input: map[string]any{"path": "a.txt", "content": "x"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("ungranted write capability not denied: %v", err)
	}
}

func TestExecuteRoutesWriteStepsToEffect(t *testing.T) {
	value, root := writeWorld(t)
	adapter := NewAdapter(value)
	output, err := adapter.Execute(context.Background(), execution.Step{Capability: "filesystem.write", Operation: "write_file", Input: map[string]any{"path": "via-execute.txt", "content": "routed"}})
	if err != nil {
		t.Fatal(err)
	}
	if output["path"] != filepath.Join(root, "via-execute.txt") {
		t.Fatalf("legacy Execute contract broken: %#v", output)
	}
}

func TestEffectProcessRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix process test")
	}
	value, root := writeWorld(t)
	adapter := NewAdapter(value)
	result, err := adapter.Effect(context.Background(), execution.Step{Capability: "process.execute", Operation: "run", Input: map[string]any{"command": "sh", "args": []any{"-c", "echo agent-effect"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectType != EffectProcessRun || !strings.Contains(result.Output["stdout"].(string), "agent-effect") {
		t.Fatalf("unexpected process result: %#v", result)
	}
	if result.Output["cwd"] != root {
		t.Fatalf("expected default cwd to be the declared root: %#v", result.Output)
	}
	// A relative cwd must resolve inside the declared resource, not against the
	// executor's own working directory ("." once ran the command wherever the
	// executor happened to start, silently testing the wrong repository).
	result, err = adapter.Effect(context.Background(), execution.Step{Capability: "process.execute", Operation: "run", Input: map[string]any{"command": "sh", "args": []any{"-c", "pwd"}, "cwd": "."}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["cwd"] != root {
		t.Fatalf("relative cwd must resolve to the declared root: %#v", result.Output)
	}
	_, err = adapter.Effect(context.Background(), execution.Step{Capability: "process.execute", Operation: "run", Input: map[string]any{"command": "sh", "args": []any{"-c", "exit 3"}}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorProcessFailed {
		t.Fatalf("non-zero exit not reported as failure: %v", err)
	}
	_, err = adapter.Effect(context.Background(), execution.Step{Capability: "process.execute", Operation: "run", Input: map[string]any{"command": "sh", "args": []any{"-c", "exit 0"}, "cwd": "../../outside"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("cwd escape not denied: %v", err)
	}
}

func TestEffectProcessPolicy(t *testing.T) {
	value, _ := writeWorld(t)
	value.Policies = []Policy{{Effect: PolicyAllow, Capability: "process.execute", Commands: []string{"echo"}}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(value)
	_, err := adapter.Effect(context.Background(), execution.Step{Capability: "process.execute", Operation: "run", Input: map[string]any{"command": "sh", "args": []any{"-c", "echo no"}}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("allowlist not enforced: %v", err)
	}
	result, err := adapter.Effect(context.Background(), execution.Step{Capability: "process.execute", Operation: "run", Input: map[string]any{"command": "echo", "args": []any{"allowed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["exit_code"].(int) != 0 {
		t.Fatalf("allowed command failed: %#v", result)
	}
}

func TestEffectProcessDenyCommandPolicy(t *testing.T) {
	value, _ := writeWorld(t)
	value.Policies = []Policy{{Effect: PolicyDeny, Capability: "process.execute", Commands: []string{"rm"}}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(value)
	_, err := adapter.Effect(context.Background(), execution.Step{Capability: "process.execute", Operation: "run", Input: map[string]any{"command": "rm", "args": []any{"-rf", "/"}}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("deny-list not enforced: %v", err)
	}
	if _, err = adapter.Effect(context.Background(), execution.Step{Capability: "process.execute", Operation: "run", Input: map[string]any{"command": "echo", "args": []any{"fine"}}}); err != nil {
		t.Fatalf("unscoped command blocked by deny-list: %v", err)
	}
}

func TestEffectPolicyCommandsOnlyForProcess(t *testing.T) {
	value, _ := writeWorld(t)
	value.Policies = []Policy{{Effect: PolicyAllow, Capability: "filesystem.write", Commands: []string{"x"}}}
	if err := value.Validate(); err == nil {
		t.Fatal("command policy accepted for non-process capability")
	}
}

func TestEffectGitBranchAndCommit(t *testing.T) {
	root := buildTestRepository(t)
	value := World{
		Protocol: "frp", Version: "0.3", WorldID: "effect-git", StateVersion: 1,
		Resources:    []Resource{{ID: "repo", Type: ResourceGitRepository, Path: root}},
		Capabilities: []string{"git.read", "git.write"},
		Limits:       Limits{MaxReadBytes: 4096, MaxEntries: 100, TimeoutSec: 5},
	}
	adapter := NewAdapter(value)
	result, err := adapter.Effect(context.Background(), execution.Step{Capability: "git.write", Operation: "create_branch", Input: map[string]any{"path": ".", "name": "agent/experiment"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectType != EffectGitBranch {
		t.Fatalf("unexpected branch result: %#v", result)
	}
	if err = os.WriteFile(filepath.Join(root, "notes.txt"), []byte("changed by agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = adapter.Effect(context.Background(), execution.Step{Capability: "git.write", Operation: "commit", Input: map[string]any{"path": ".", "message": "agent snapshot", "author": "Agent <agent@temporality.local>"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectType != EffectGitCommit || result.Output["branch"] != "main" {
		t.Fatalf("unexpected commit result: %#v", result)
	}
	if result.Output["parent"] == "" || result.Output["commit"] == result.Output["parent"] {
		t.Fatalf("commit did not advance history: %#v", result.Output)
	}
	// The ref must point at the new commit and our reader must decode it.
	gitDir, _ := GitDir(root)
	_, head, _, headErr := ReadHead(gitDir)
	if headErr != nil || head != result.Output["commit"] {
		t.Fatalf("ref not advanced: %q %v", head, headErr)
	}
	commit, readErr := ReadCommit(gitDir, head)
	if readErr != nil || commit.Summary != "agent snapshot" {
		t.Fatalf("commit unreadable: %#v %v", commit, readErr)
	}
}

func TestEffectGitReadOnlyDenied(t *testing.T) {
	root := buildTestRepository(t)
	value := World{
		Protocol: "frp", Version: "0.3", WorldID: "effect-git-ro", StateVersion: 1,
		Resources:    []Resource{{ID: "repo", Type: ResourceGitRepository, Path: root, ReadOnly: true}},
		Capabilities: []string{"git.read", "git.write"},
		Limits:       Limits{MaxReadBytes: 4096, MaxEntries: 100, TimeoutSec: 5},
	}
	adapter := NewAdapter(value)
	_, err := adapter.Effect(context.Background(), execution.Step{Capability: "git.write", Operation: "create_branch", Input: map[string]any{"path": ".", "name": "blocked"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("read-only repo write not denied: %v", err)
	}
}

func TestEffectHTTPPost(t *testing.T) {
	var received string
	var receivedContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 128)
		n, _ := r.Body.Read(raw)
		received = string(raw[:n])
		receivedContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	value := World{
		Protocol: "frp", Version: "0.3", WorldID: "effect-http", StateVersion: 1,
		Resources:    []Resource{{ID: "api", Type: ResourceHTTPEndpoint, Endpoint: server.URL}},
		Capabilities: []string{"http.read", "http.write"},
		Limits:       Limits{MaxReadBytes: 4096, MaxEntries: 100, TimeoutSec: 5},
	}
	adapter := NewAdapter(value)
	result, err := adapter.Effect(context.Background(), execution.Step{Capability: "http.write", Operation: "post", Input: map[string]any{"url": server.URL + "/items", "body": `{"hello":"world"}`, "content_type": "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectType != EffectHTTPPosted || result.Output["status"].(int) != http.StatusCreated {
		t.Fatalf("unexpected http effect: %#v", result)
	}
	if received != `{"hello":"world"}` || receivedContentType != "application/json" {
		t.Fatalf("server observed %q %q", received, receivedContentType)
	}
	_, err = adapter.Effect(context.Background(), execution.Step{Capability: "http.write", Operation: "post", Input: map[string]any{"url": "https://elsewhere.example/api", "body": "x"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("undeclared endpoint accepted: %v", err)
	}
}

func TestEffectHTTPReadOnlyDenied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	value := World{
		Protocol: "frp", Version: "0.3", WorldID: "effect-http-ro", StateVersion: 1,
		Resources:    []Resource{{ID: "api", Type: ResourceHTTPEndpoint, Endpoint: server.URL, ReadOnly: true}},
		Capabilities: []string{"http.read", "http.write"},
		Limits:       Limits{MaxReadBytes: 4096, MaxEntries: 100, TimeoutSec: 5},
	}
	adapter := NewAdapter(value)
	_, err := adapter.Effect(context.Background(), execution.Step{Capability: "http.write", Operation: "post", Input: map[string]any{"url": server.URL + "/x", "body": "x"}})
	if class, ok := executionErrorClass(err); !ok || class != execution.ErrorPermissionDenied {
		t.Fatalf("read-only endpoint write not denied: %v", err)
	}
}

func TestEffectEventShape(t *testing.T) {
	value, _ := writeWorld(t)
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	event, err := EffectEvent(value, "exec-1", "write_file", Effect{Resource: "filesystem:///tmp/x", EffectType: EffectFileWritten, Payload: map[string]any{"bytes": 3}}, "event-1", at, "episode-1", "branch-1", value.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != EventWorldEffect {
		t.Fatalf("unexpected event type: %s", event.Type)
	}
	if !IsCanonicalEventType(EventWorldEffect) {
		t.Fatal("world.effect not in canonical registry")
	}
	if event.Payload["effect_type"] != EffectFileWritten || event.Payload["execution_id"] != "exec-1" {
		t.Fatalf("unexpected payload: %#v", event.Payload)
	}
	if _, ok := event.Payload["intent_world_version"]; ok {
		t.Fatal("matching intent version must not be recorded as divergence")
	}
	divergent, err := EffectEvent(value, "exec-1", "write_file", Effect{Resource: "filesystem:///tmp/x", EffectType: EffectFileWritten, Payload: map[string]any{}}, "event-2", at, "episode-1", "branch-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := divergent.Payload["intent_world_version"]; !ok {
		t.Fatal("world divergence was not recorded in the effect event")
	}
	if _, err = EffectEvent(value, "exec-1", "write_file", Effect{Resource: "x", EffectType: "", Payload: map[string]any{}}, "event-3", at, "episode-1", "branch-1", 1); err == nil {
		t.Fatal("effect without type accepted")
	}
}
