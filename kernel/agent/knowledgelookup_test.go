package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The knowledge_lookup tool closes the priming contract: a compact cue carries
// knowledge_id, and the agent can pull the full item (grounds, lifecycle,
// scope) when a cue actually matters (docs/plan-priming-relevance.md §10).
func TestKnowledgeLookupToolFetchesFullItem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/observations/knowledge" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer internal" {
			t.Errorf("authorization = %q, want internal api token", got)
		}
		if r.URL.Query().Get("knowledge_id") != "k-claim1" || r.URL.Query().Get("project") != "demo" {
			t.Errorf("unexpected query %v", r.URL.Query())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"knowledge": []any{
			map[string]any{"id": "k-claim1", "proposition": "The gatekeeper CLI requires the --out flag", "state": "confirmed", "evidence": []any{map[string]any{"ref": "op-1", "type": "execution"}}},
		}, "count": 1})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL, APIToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", Name: "knowledge_lookup", Arguments: map[string]any{"knowledge_id": "k-claim1"},
	})
	if err != nil {
		t.Fatalf("knowledge_lookup: %v", err)
	}
	for _, want := range []string{"k-claim1", "gatekeeper CLI", "confirmed", "evidence"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("result missing %q: %s", want, result.Content)
		}
	}
}

func TestKnowledgeLookupToolReportsMissingAndValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"knowledge not found"}`))
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL, APIToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", Name: "knowledge_lookup", Arguments: map[string]any{"knowledge_id": "k-gone"},
	})
	if err != nil {
		t.Fatalf("knowledge_lookup: %v", err)
	}
	if !strings.Contains(result.Content, "error:") || !strings.Contains(result.Content, "k-gone") {
		t.Fatalf("expected a tool error naming the id, got %q", result.Content)
	}

	result, err = activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", Name: "knowledge_lookup", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("knowledge_lookup: %v", err)
	}
	if !strings.Contains(result.Content, "error: knowledge_id is required") {
		t.Fatalf("expected validation error, got %q", result.Content)
	}
}
