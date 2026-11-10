package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
)

// The per-model output cap (docs/agents.md §routing) overrides the shared
// kernel default: TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS (16k) is a conservative
// floor no capable model should inherit by accident — a 44 KB artifact edit
// was truncated mid tool-call by exactly that floor (finish: length @ 16384).
func TestApplyModelOutputCap(t *testing.T) {
	config := llm.Config{MaxOutputTokens: 16384}
	applyModelOutputCap(&config, modelConfigAnswer{MaxOutputTokens: 32768})
	if config.MaxOutputTokens != 32768 {
		t.Fatalf("MaxOutputTokens = %d, want the per-model 32768", config.MaxOutputTokens)
	}
	// An absent cap must not reset the configured default.
	applyModelOutputCap(&config, modelConfigAnswer{})
	if config.MaxOutputTokens != 32768 {
		t.Fatalf("an absent cap must leave the value alone, got %d", config.MaxOutputTokens)
	}
	// A zero/absent answer cap leaves the default flow intact.
	fresh := llm.Config{MaxOutputTokens: 16384}
	applyModelOutputCap(&fresh, modelConfigAnswer{MaxOutputTokens: 0})
	if fresh.MaxOutputTokens != 16384 {
		t.Fatalf("MaxOutputTokens = %d, want the untouched default", fresh.MaxOutputTokens)
	}
}

// The routing answer carries the cap end to end: pinnedConfigFrom decodes it
// into the call config.
func TestPinnedConfigFromCarriesModelCap(t *testing.T) {
	activities := &Activities{}
	config, _, err := activities.pinnedConfigFrom(
		`{"provider_id":"prov-x","provider_name":"X","base_url":"http://x","api_key":"k","model":"mimo-v2.6-pro","max_output_tokens":32768}`,
		200, "prov-x", "mimo-v2.6-pro")
	if err != nil {
		t.Fatalf("pinnedConfigFrom: %v", err)
	}
	if config.MaxOutputTokens != 32768 {
		t.Fatalf("MaxOutputTokens = %d, want the routing answer's 32768", config.MaxOutputTokens)
	}
	config, _, err = activities.pinnedConfigFrom(
		`{"provider_id":"prov-x","provider_name":"X","base_url":"http://x","api_key":"k","model":"m"}`,
		200, "prov-x", "m")
	if err != nil {
		t.Fatalf("pinnedConfigFrom: %v", err)
	}
	if config.MaxOutputTokens <= 0 {
		t.Fatalf("the shared default must still apply without a cap, got %d", config.MaxOutputTokens)
	}
}

// A workspace still booting answers the pinned routing lookup with a 503
// {"booting":true} — infrastructure availability, not an answer about the
// model. The lookup must ride it out (one silent retry is enough here) and
// succeed once the workspace is up — the exact scenario that used to fail
// every resumed run after a stack restart.
func TestModelConfigForPinnedRidesOutBooting503(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"booting":true,"ok":false}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider_id":"prov-x","provider_name":"X","base_url":"http://x","api_key":"k","model":"m"}`))
	}))
	defer server.Close()

	activities := &Activities{WorkspaceURL: server.URL, HTTP: server.Client()}
	config, _, err := activities.modelConfigForPinned(context.Background(), "proj", "prov-x", "m")
	if err != nil {
		t.Fatalf("pinned routing must ride out a booting 503: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want the booting 503 retried once", calls)
	}
	if config.BaseURL != "http://x" || config.APIKey != "k" {
		t.Fatalf("config decoded from the recovered answer: %+v", config)
	}
}

// A 4xx routing answer stays definitive: no retries, the actionable error
// surfaces immediately (a disabled provider must not burn the wait window).
func TestModelConfigForPinnedDefinitive4xxIsNotRetried(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"provider is disabled"}`))
	}))
	defer server.Close()

	activities := &Activities{WorkspaceURL: server.URL, HTTP: server.Client()}
	_, _, err := activities.modelConfigForPinned(context.Background(), "proj", "prov-x", "m")
	if err == nil {
		t.Fatal("a 409 routing answer must fail loudly")
	}
	if !strings.Contains(err.Error(), "provider is disabled") {
		t.Fatalf("error must carry the actionable detail: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want the definitive 4xx to fail fast with no retries", calls)
	}
}
