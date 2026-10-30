package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// A 401 from the routed provider must: annotate the error with the routing
// outcome, journal model.call_failed with the masked credential and the
// request/response pair, and evict the routing cache so the retry re-resolves.
func TestCallModelAuthRejectionJournalsCallReport(t *testing.T) {
	var requests int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Www-Authenticate", `Bearer realm="gpustack"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"reason":"Unauthorized","message":"Invalid authentication credentials"}`))
	}))
	defer provider.Close()

	var lookups int32
	// Fresh key on the SECOND routing lookup: the eviction must re-resolve
	// (the first cached resolution held the stale key).
	lookupsKey := map[int]string{1: "stale-key-value-0001", 2: "fresh-key-value-0002"}
	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/model-config" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		n := atomic.AddInt32(&lookups, 1)
		key := lookupsKey[int(n)]
		if key == "" {
			key = "fresh-key"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"provider_id": "prov-x", "base_url": provider.URL, "api_key": key, "model": "mimo-x",
		})
	}))
	defer workspace.Close()

	outbox := &capturedOutbox{}
	activities := &Activities{
		HTTP:         &http.Client{},
		WorkspaceURL: workspace.URL,
		Events:       outbox,
		SourceID:     "kernel",
	}

	_, err := activities.CallModel(context.Background(), ModelRequest{
		RunID: "run-401", Turn: 2, Project: "proj-1", Model: "mimo-x",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected the 401 to fail the call")
	}
	if !strings.Contains(err.Error(), "served by provider prov-x") {
		t.Fatalf("error must carry the routing outcome, got: %v", err)
	}

	var report *observation.Event
	for i := range outbox.events {
		if outbox.events[i].Type == "model.call_failed" {
			report = &outbox.events[i]
			break
		}
	}
	if report == nil {
		t.Fatalf("model.call_failed must be journaled, events: %v", outbox.events)
	}
	// The event data in memory carries the typed CallReport; round-trip it
	// through JSON the way the journal does before asserting shapes.
	flat, err := json.Marshal(report.Data)
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	var flatData map[string]any
	if err := json.Unmarshal(flat, &flatData); err != nil {
		t.Fatalf("unmarshal event data: %v", err)
	}
	requestData := flatData["request"].(map[string]any)
	if requestData["url"] == "" || !strings.Contains(requestData["url"].(string), "/chat/completions") {
		t.Fatalf("report url = %v", requestData["url"])
	}
	headers := requestData["request_headers"].(map[string]any)
	auth := headers["Authorization"].(string)
	if !strings.Contains(auth, "stale") || !strings.Contains(auth, "…") || strings.Contains(auth, "stale-key-value") {
		t.Fatalf("Authorization must be masked but identifiable, got %q", auth)
	}
	if flatData["route"] != "provider prov-x" {
		t.Fatalf("route = %v", report.Data["route"])
	}
	if flatData["status"].(float64) != 401 {
		t.Fatalf("status = %v", report.Data["status"])
	}

	// The eviction: the retry's routing lookup must happen again (2 lookups
	// total) instead of serving the stale cached key. The second call still
	// fails (the provider always 401s), but its report must show the FRESH
	// credential — proof the cache was evicted and re-resolved.
	_, err2 := activities.CallModel(context.Background(), ModelRequest{
		RunID: "run-401", Turn: 3, Project: "proj-1", Model: "mimo-x",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err2 == nil {
		t.Fatal("second call must still fail (the fake provider always 401s)")
	}
	if !strings.Contains(err2.Error(), "served by provider prov-x") {
		t.Fatalf("second error must carry the routing outcome, got: %v", err2)
	}
	if atomic.LoadInt32(&lookups) < 2 {
		t.Fatalf("model-config lookups = %d, want >= 2 (cache must be evicted on 401)", lookups)
	}
	var report2 *observation.Event
	for i := len(outbox.events) - 1; i >= 0; i-- {
		if outbox.events[i].Type == "model.call_failed" && outbox.events[i].Data["turn"] == 3 {
			report2 = &outbox.events[i]
			break
		}
	}
	if report2 == nil {
		t.Fatal("the second attempt must journal its own model.call_failed")
	}
	flat2, _ := json.Marshal(report2.Data)
	var data2 map[string]any
	_ = json.Unmarshal(flat2, &data2)
	req2 := data2["request"].(map[string]any)
	auth2 := req2["request_headers"].(map[string]any)["Authorization"].(string)
	if !strings.Contains(auth2, "fresh") {
		t.Fatalf("the retried call must carry the re-resolved key, got %q", auth2)
	}
}
