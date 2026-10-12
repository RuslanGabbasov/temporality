package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
)

// routingFakeWorkspace fakes the kernel workspace API's model-config endpoint:
// it answers for exactly one (project, model) pair and counts lookups so the
// test can assert the activity-side TTL cache does its job.
func routingFakeWorkspace(t *testing.T, project, model, providerBaseURL string, lookups *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/model-config" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		atomic.AddInt32(lookups, 1)
		if r.URL.Query().Get("model") != model || r.URL.Query().Get("project") != project {
			http.Error(w, fmt.Sprintf("no provider serves model %q", r.URL.Query().Get("model")), http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"provider_id": "prov-x", "base_url": providerBaseURL, "api_key": "prov-key", "model": model,
		})
	}))
}

// routingFakeProvider fakes the routed-to provider: a plain blocking chat
// completion that records the Authorization it received.
func routingFakeProvider(t *testing.T, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		body, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "routed answer"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

// A non-default model must be routed to the workspace provider that serves
// it — not to the env/default provider with the model name swapped.
func TestCallModelRoutesToServingProvider(t *testing.T) {
	var gotAuth string
	provider := routingFakeProvider(t, &gotAuth)
	defer provider.Close()

	var lookups int32
	workspace := routingFakeWorkspace(t, "proj-1", "mimo-x", provider.URL, &lookups)
	defer workspace.Close()

	// The default client points at a server that must never be reached: a
	// routing bug would send "mimo-x" there and fail the test below.
	var defaultHits int32
	unused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&defaultHits, 1)
		http.Error(w, "default provider must not be hit", http.StatusForbidden)
	}))
	defer unused.Close()

	activities := &Activities{
		Model:        llm.New(llm.Config{BaseURL: unused.URL, Model: "default-model", APIKey: "env-key", Timeout: 10 * time.Second}),
		HTTP:         &http.Client{},
		WorkspaceURL: workspace.URL,
	}

	for i := 0; i < 3; i++ {
		completion, err := activities.CallModel(context.Background(), ModelRequest{
			Model: "mimo-x", Project: "proj-1",
			Messages: []llm.Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("CallModel #%d: %v", i+1, err)
		}
		if completion.Content != "routed answer" {
			t.Fatalf("content = %q", completion.Content)
		}
	}
	if gotAuth != "Bearer prov-key" {
		t.Fatalf("provider auth = %q, want the provider api_key", gotAuth)
	}
	if hits := atomic.LoadInt32(&defaultHits); hits != 0 {
		t.Fatalf("default provider was hit %d times", hits)
	}
	// Three turns, one workspace lookup: the TTL cache keeps the hot path
	// free of routing round trips.
	if n := atomic.LoadInt32(&lookups); n != 1 {
		t.Fatalf("workspace lookups = %d, want 1 (TTL cache miss)", n)
	}
}

// No provider serving the model → clean 404 falls back to the env config
// with the model swapped in (the pre-routing behavior), and the negative
// answer is cached: the fallback turn does not re-hit the workspace either.
func TestCallModelFallsBackWhenNoProviderServesModel(t *testing.T) {
	var lookups int32
	workspace := routingFakeWorkspace(t, "proj-1", "mimo-x", "http://unused.example", &lookups)
	defer workspace.Close()

	var envHits int32
	envProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&envHits, 1)
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Model != "mimo-y" {
			t.Errorf("env fallback model = %q, want mimo-y", payload.Model)
		}
		body, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "env answer"}, "finish_reason": "stop"}},
		})
		_, _ = w.Write(body)
	}))
	defer envProvider.Close()

	t.Setenv("TEMPORALITY_MODEL_BASE_URL", envProvider.URL)
	t.Setenv("TEMPORALITY_MODEL_ID", "default-model")
	t.Setenv("TEMPORALITY_MODEL_API_KEY", "env-key")

	activities := &Activities{
		Model:        llm.New(llm.Config{BaseURL: envProvider.URL, Model: "default-model", APIKey: "env-key", Timeout: 10 * time.Second}),
		HTTP:         &http.Client{},
		WorkspaceURL: workspace.URL,
	}

	for i := 0; i < 2; i++ {
		completion, err := activities.CallModel(context.Background(), ModelRequest{
			Model: "mimo-y", Project: "proj-1",
			Messages: []llm.Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("CallModel #%d: %v", i+1, err)
		}
		if completion.Content != "env answer" {
			t.Fatalf("content = %q", completion.Content)
		}
	}
	if n := atomic.LoadInt32(&lookups); n != 1 {
		t.Fatalf("workspace lookups = %d, want 1 (negative cached)", n)
	}
	if n := atomic.LoadInt32(&envHits); n != 2 {
		t.Fatalf("env provider hits = %d, want 2", n)
	}
}

// The default model keeps using the pre-built client: no workspace lookup,
// no client rebuild — the routing path is for non-default models only.
func TestCallModelDefaultModelSkipsRouting(t *testing.T) {
	var lookups int32
	workspace := routingFakeWorkspace(t, "proj-1", "mimo-x", "http://unused.example", &lookups)
	defer workspace.Close()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "default answer"}, "finish_reason": "stop"}},
		})
		_, _ = w.Write(body)
	}))
	defer provider.Close()

	activities := &Activities{
		Model:        llm.New(llm.Config{BaseURL: provider.URL, Model: "default-model", APIKey: "env-key", Timeout: 10 * time.Second}),
		HTTP:         &http.Client{},
		WorkspaceURL: workspace.URL,
	}
	completion, err := activities.CallModel(context.Background(), ModelRequest{
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CallModel: %v", err)
	}
	if completion.Content != "default answer" {
		t.Fatalf("content = %q", completion.Content)
	}
	if n := atomic.LoadInt32(&lookups); n != 0 {
		t.Fatalf("workspace lookups = %d, want 0 for default model", n)
	}
}
