package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// secretResolveStub serves the kernel's resolve endpoint shape: one call
// counter, a resolved value for the granted name and 404 for anything else.
func secretResolveStub(t *testing.T, value string, calls *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if !strings.HasSuffix(r.URL.Path, "/resolve") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/github-token/resolve") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "github-token", "value": value})
	}))
	t.Cleanup(server.Close)
	return server
}

type secretOutbox struct {
	events []observation.Event
}

func (c *secretOutbox) Enqueue(_ context.Context, event observation.Event) error {
	c.events = append(c.events, event)
	return nil
}

func TestSecretBrokerResolvesAndCaches(t *testing.T) {
	var calls int32
	server := secretResolveStub(t, "ghp_value", &calls)
	broker := &SecretBroker{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal", TTL: time.Minute}

	first, denied, err := broker.Resolve(context.Background(), "github-token")
	if err != nil || denied || first != "ghp_value" {
		t.Fatalf("first resolve: %q denied=%v err=%v", first, denied, err)
	}
	second, denied, err := broker.Resolve(context.Background(), "github-token")
	if err != nil || denied || second != "ghp_value" {
		t.Fatalf("cached resolve: %q denied=%v err=%v", second, denied, err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("cache must collapse repeated resolutions, got %d calls", calls)
	}
}

func TestSecretBrokerDeniedOnNotFound(t *testing.T) {
	var calls int32
	server := secretResolveStub(t, "", &calls)
	broker := &SecretBroker{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	value, denied, err := broker.Resolve(context.Background(), "missing")
	if err != nil {
		t.Fatalf("denial must not be a transport error, got %v", err)
	}
	if !denied || value != "" {
		t.Fatalf("expected denial, got value=%q denied=%v", value, denied)
	}
}

func TestSecretEnvVarName(t *testing.T) {
	cases := map[string]string{
		"github-token":          "SECRET_GITHUB_TOKEN",
		"db.json":               "SECRET_DB_JSON",
		"simple":                "SECRET_SIMPLE",
		// Spaces and other odd characters from a freestyle workspace name must
		// sanitize away — a raw space used to fail docker --env and kill runs.
		"azure devops project hrpro": "SECRET_AZURE_DEVOPS_PROJECT_HRPRO",
		"token (prod) v2":            "SECRET_TOKEN__PROD__V2",
		"--weird--":                  "SECRET___WEIRD__",
	}
	for input, want := range cases {
		if got := SecretEnvVarName(input); got != want {
			t.Errorf("SecretEnvVarName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSandboxSecretEnvSkipsCollidingNames(t *testing.T) {
	// This stub resolves ANY name (the collision test needs three granted
	// names, not just github-token).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/resolve") || r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "any", "value": "v"})
	}))
	t.Cleanup(server.Close)
	outbox := &secretOutbox{}
	activities := &Activities{
		HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal",
		Events: outbox, SourceID: "kernel", Secrets: &SecretBroker{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"},
	}
	// "azure-devops" and "azure devops" sanitize to the same env key: the
	// first wins, the second is skipped — a soft, model-visible condition,
	// never a run-killing docker validation error.
	request := ToolRequest{
		RunID: "run-collide", OperationID: "run-collide/turn/01/call_1", Project: "demo",
		Secrets: []string{"azure-devops", "azure devops", "plain"},
	}
	env, denied, err, scan, skipped, _ := activities.sandboxSecretEnv(context.Background(), request)
	if err != nil {
		t.Fatalf("sandboxSecretEnv: %v", err)
	}
	if len(denied) != 0 {
		t.Fatalf("denied = %v, want none", denied)
	}
	if len(skipped) != 1 || skipped[0] != "azure devops" {
		t.Fatalf("skipped = %v, want [azure devops]", skipped)
	}
	if len(env) != 2 || env[0] != "SECRET_AZURE_DEVOPS=v" || env[1] != "SECRET_PLAIN=v" {
		t.Fatalf("env = %v, want the first collision winner and plain", env)
	}
	// Only the injected values arm the scan: the skipped secret never enters
	// the sandbox, so there is nothing of it to guard.
	if len(scan) != 2 {
		t.Fatalf("scan secrets = %d, want 2 (injected only)", len(scan))
	}
}

func TestSandboxSecretEnvEmitsIssuedOncePerRun(t *testing.T) {
	var calls int32
	server := secretResolveStub(t, "ghp_value", &calls)
	outbox := &secretOutbox{}
	activities := &Activities{
		HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal",
		Events: outbox, SourceID: "kernel", Secrets: &SecretBroker{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"},
	}
	request := ToolRequest{
		RunID: "run-secret", OperationID: "run-secret/turn/01/call_1", Project: "demo",
		Secrets: []string{"github-token", "missing-secret"},
	}
	env, denied, err, _, _, _ := activities.sandboxSecretEnv(context.Background(), request) // disabled not exercised here
	if err != nil {
		t.Fatalf("sandboxSecretEnv: %v", err)
	}
	if len(env) != 1 || env[0] != "SECRET_GITHUB_TOKEN=ghp_value" {
		t.Fatalf("env = %v, want one resolved entry", env)
	}
	if len(denied) != 1 || denied[0] != "missing-secret" {
		t.Fatalf("denied = %v, want missing-secret", denied)
	}
	// A second command in the same run re-injects the value at its own
	// execution boundary but must not journal another issuance.
	request.OperationID = "run-secret/turn/02/call_2"
	env2, denied2, err, _, _, _ := activities.sandboxSecretEnv(context.Background(), request)
	if err != nil {
		t.Fatalf("second sandboxSecretEnv: %v", err)
	}
	if len(env2) != 1 || env2[0] != "SECRET_GITHUB_TOKEN=ghp_value" {
		t.Fatalf("second env = %v, want the value re-injected", env2)
	}
	if len(denied2) != 1 {
		t.Fatalf("second denied = %v, denials stay per-call", denied2)
	}
	issued, deniedEvents := 0, 0
	for _, event := range outbox.events {
		switch event.Type {
		case "secret.issued":
			issued++
		case "secret.denied":
			deniedEvents++
		}
		if event.Context.Project != "demo" || event.Context.Run != "run-secret" {
			t.Fatalf("event context not bound to the run: %+v", event.Context)
		}
		for _, value := range event.Data {
			if text, ok := value.(string); ok && strings.Contains(text, "ghp_value") {
				t.Fatalf("plaintext leaked into events: %q", text)
			}
		}
	}
	if issued != 1 {
		t.Fatalf("secret.issued events = %d, want exactly one per run per secret", issued)
	}
	if deniedEvents != 2 {
		t.Fatalf("secret.denied events = %d, want one per call", deniedEvents)
	}
}

func TestHandleSecretListShowsGrantedNamesOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": []map[string]any{
			{"name": "github-token", "description": "GitHub PAT", "provider": "builtin"},
			{"name": "aws-deploy", "description": "AWS keys", "provider": "builtin"},
		}})
	}))
	t.Cleanup(server.Close)
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.handleSecretList(context.Background(), ToolRequest{Secrets: []string{"github-token"}})
	if err != nil {
		t.Fatalf("handleSecretList: %v", err)
	}
	if !strings.Contains(result.Content, "github-token") || strings.Contains(result.Content, "aws-deploy") {
		t.Fatalf("secret_list must show granted names only, got %q", result.Content)
	}
	if strings.Contains(result.Content, "SECRET_GITHUB_TOKEN=") {
		t.Fatal("secret_list must never contain values")
	}
}

// An operator-DISABLED secret (the workspace answers 409) skips injection
// with a visible notice — the run continues; the other secrets still inject.
func TestSandboxSecretEnvDisabledContinues(t *testing.T) {
	var calls int32
	// github-token resolves; the disabled one answers 409.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if !strings.HasSuffix(r.URL.Path, "/resolve") || r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.Path, "off-secret") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"secret \"off-secret\" is disabled; enable it in Settings → Secrets"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "github-token", "value": "ghp_value"})
	}))
	t.Cleanup(server.Close)
	outbox := &secretOutbox{}
	activities := &Activities{
		HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal",
		Events: outbox, SourceID: "kernel", Secrets: &SecretBroker{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"},
	}
	request := ToolRequest{
		RunID: "run-disabled", OperationID: "run-disabled/turn/01/call_1", Project: "demo",
		Secrets: []string{"github-token", "off-secret"},
	}
	env, denied, err, scan, skipped, disabled := activities.sandboxSecretEnv(context.Background(), request)
	if err != nil {
		t.Fatalf("a disabled secret must not error the resolution: %v", err)
	}
	if len(denied) != 0 {
		t.Fatalf("denied = %v, want none (disabled is not a denial)", denied)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none", skipped)
	}
	if len(disabled) != 1 || disabled[0] != "off-secret" {
		t.Fatalf("disabled = %v, want [off-secret]", disabled)
	}
	if len(env) != 1 || env[0] != "SECRET_GITHUB_TOKEN=ghp_value" {
		t.Fatalf("env = %v, want the healthy secret only", env)
	}
	if len(scan) != 1 {
		t.Fatalf("scan = %d, want 1 (only the injected value)", len(scan))
	}
	// The denial rides the journal either way (audit of what was skipped).
	denials := 0
	for _, event := range outbox.events {
		if event.Type == "secret.denied" {
			denials++
		}
	}
	if denials == 0 {
		t.Fatal("the disabled secret must be journaled as secret.denied for the audit")
	}
}
