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
		"github-token": "SECRET_GITHUB_TOKEN",
		"db.json":      "SECRET_DB_JSON",
		"simple":       "SECRET_SIMPLE",
	}
	for input, want := range cases {
		if got := SecretEnvVarName(input); got != want {
			t.Errorf("SecretEnvVarName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSandboxSecretEnvEmitsIssuedAndDenied(t *testing.T) {
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
	env, denied, err := activities.sandboxSecretEnv(context.Background(), request)
	if err != nil {
		t.Fatalf("sandboxSecretEnv: %v", err)
	}
	if len(env) != 1 || env[0] != "SECRET_GITHUB_TOKEN=ghp_value" {
		t.Fatalf("env = %v, want one resolved entry", env)
	}
	if len(denied) != 1 || denied[0] != "missing-secret" {
		t.Fatalf("denied = %v, want missing-secret", denied)
	}
	kinds := map[string]bool{}
	for _, event := range outbox.events {
		kinds[event.Type] = true
		if event.Context.Project != "demo" || event.Context.Run != "run-secret" {
			t.Fatalf("event context not bound to the run: %+v", event.Context)
		}
		for _, value := range event.Data {
			if text, ok := value.(string); ok && strings.Contains(text, "ghp_value") {
				t.Fatalf("plaintext leaked into events: %q", text)
			}
		}
	}
	if !kinds["secret.issued"] || !kinds["secret.denied"] {
		t.Fatalf("expected secret.issued and secret.denied events, got %v", kinds)
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
