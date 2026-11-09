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

	"github.com/temporality-project/temporality/kernel/llm"
)

// Regression (docs/supervisor-model.md §4): a vision override must route the
// call to whichever provider serves the vision model — the agent's provider
// pin binds its OWN model, so a z.ai-pinned agent viewing a screenshot must
// reach the Xiaomi vision model instead of failing with "the agent's provider
// does not serve model ...".
func TestCallModelVisionOverrideIgnoresProviderPin(t *testing.T) {
	var visionAuth string
	visionProvider := routingFakeProvider(t, &visionAuth)
	defer visionProvider.Close()

	// The workspace serves ONLY the vision model for the project. The agent's
	// pinned provider (z.ai stand-in) does not serve it — a call to the pinned
	// config endpoint 404s exactly like the real one does.
	var pinnedHits, generalHits int32
	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workspace/supervisor":
			_, _ = w.Write([]byte(`{"vision_model": "mimo-v2.6-flash"}`))
		case r.URL.Path == "/v1/workspace/model-config":
			q := r.URL.Query()
			if q.Get("provider_id") != "" {
				// The pinned lookup: the pin does not serve the vision model.
				atomic.AddInt32(&pinnedHits, 1)
				http.Error(w, "the provider does not serve model "+q.Get("model"), http.StatusNotFound)
				return
			}
			if q.Get("model") == "mimo-v2.6-flash" && q.Get("project") == "proj-1" {
				atomic.AddInt32(&generalHits, 1)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"provider_id": "prov-xiaomi", "base_url": visionProvider.URL, "api_key": "xiaomi-key", "model": "mimo-v2.6-flash",
				})
				return
			}
			http.Error(w, "no provider serves model "+q.Get("model"), http.StatusNotFound)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer workspace.Close()

	// The default client must never be reached either.
	var defaultHits int32
	unused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&defaultHits, 1)
		http.Error(w, "default provider must not be hit", http.StatusForbidden)
	}))
	defer unused.Close()

	activities := &Activities{
		Model:        llm.New(llm.Config{BaseURL: unused.URL, Model: "glm-text-only", APIKey: "env-key", Timeout: 10 * time.Second}),
		HTTP:         &http.Client{},
		WorkspaceURL: workspace.URL,
	}

	completion, err := activities.CallModel(context.Background(), ModelRequest{
		Model:      "glm-text-only",
		ProviderID: "prov-zai", // the agent's pin
		Project:    "proj-1",
		Messages:   []llm.Message{{Role: "user", Content: "Что на скриншоте?", Images: []llm.ImageBlock{{MIMEType: "image/png", Data: []byte("hi")}}}},
	})
	if err != nil {
		t.Fatalf("vision override must not fail through the agent's pin: %v", err)
	}
	if completion.Content != "routed answer" {
		t.Fatalf("content = %q", completion.Content)
	}
	if visionAuth != "Bearer xiaomi-key" {
		t.Fatalf("vision provider auth = %q, want the Xiaomi api_key", visionAuth)
	}
	if n := atomic.LoadInt32(&pinnedHits); n != 0 {
		t.Fatalf("the pinned-provider lookup fired %d times — the pin must not apply to the supervisor's vision model", n)
	}
	if n := atomic.LoadInt32(&generalHits); n != 1 {
		t.Fatalf("general vision-model lookups = %d, want 1", n)
	}
	if n := atomic.LoadInt32(&defaultHits); n != 0 {
		t.Fatalf("default provider was hit %d times", n)
	}
}

// A pinned provider that DOES serve the vision model keeps working through
// the general lookup — no behavior change for operators who registered the
// vision model on the agent's own provider.
func TestCallModelVisionOverrideFindsServingProvider(t *testing.T) {
	var visionAuth string
	visionProvider := routingFakeProvider(t, &visionAuth)
	defer visionProvider.Close()

	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workspace/supervisor":
			_, _ = w.Write([]byte(`{"vision_model": "mimo-v2.6-flash"}`))
		case r.URL.Path == "/v1/workspace/model-config":
			q := r.URL.Query()
			if q.Get("provider_id") != "" {
				t.Errorf("pinned lookup fired for a vision-overridden call")
				http.Error(w, "unexpected pin", http.StatusNotFound)
				return
			}
			if q.Get("model") == "mimo-v2.6-flash" && q.Get("project") == "proj-1" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"provider_id": "prov-x", "base_url": visionProvider.URL, "api_key": "prov-key", "model": "mimo-v2.6-flash",
				})
				return
			}
			http.Error(w, "no provider serves model "+q.Get("model"), http.StatusNotFound)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer workspace.Close()

	activities := &Activities{
		Model:        llm.New(llm.Config{BaseURL: "http://unused", Model: "default-model", APIKey: "env-key", Timeout: 10 * time.Second}),
		HTTP:         &http.Client{},
		WorkspaceURL: workspace.URL,
	}

	completion, err := activities.CallModel(context.Background(), ModelRequest{
		Model:      "default-model",
		ProviderID: "prov-x",
		Project:    "proj-1",
		Messages:   []llm.Message{{Role: "user", Content: "look", Images: []llm.ImageBlock{{MIMEType: "image/png", Data: []byte("hi")}}}},
	})
	if err != nil {
		t.Fatalf("CallModel: %v", err)
	}
	if completion.Content != "routed answer" || !strings.Contains(visionAuth, "prov-key") {
		t.Fatalf("vision call did not route to the serving provider (content=%q auth=%q)", completion.Content, visionAuth)
	}
}
