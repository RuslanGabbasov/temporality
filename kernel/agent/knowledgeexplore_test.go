package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The knowledge_explore tool is the agent's research surface over the journal
// (docs/agent-knowledge-exploration.md §6): one composite tool, six logical
// operations, project-scoped like knowledge_lookup.
func TestKnowledgeExploreToolPostsOperation(t *testing.T) {
	var receivedPath, receivedMethod, receivedToken string
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath, receivedMethod, receivedToken = r.URL.Path, r.Method, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &receivedBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"operation": "search", "algorithm_version": "lexical-idf.v1",
			"results": []any{map[string]any{"knowledge_id": "K1", "proposition": "нужно создавать миграцию", "state": "confirmed", "matched_by": []string{"term:миграц"}}},
			"limits":  map[string]any{"max_items": 20, "items_returned": 1},
		})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL, APIToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", Name: "knowledge_explore",
		Arguments: map[string]any{
			"operation": "search", "question": "как делать миграции",
			"targets": []any{" K1 "}, "max_items": 20.0, "include_candidates": true,
		},
	})
	if err != nil {
		t.Fatalf("knowledge_explore: %v", err)
	}
	if receivedPath != "/v1/observations/knowledge/explore" || receivedMethod != http.MethodPost {
		t.Fatalf("request = %s %s", receivedMethod, receivedPath)
	}
	if receivedToken != "Bearer internal" {
		t.Fatalf("authorization = %q", receivedToken)
	}
	if receivedBody["project"] != "demo" || receivedBody["operation"] != "search" || receivedBody["question"] != "как делать миграции" {
		t.Fatalf("payload = %v", receivedBody)
	}
	targets, _ := receivedBody["targets"].([]any)
	if len(targets) != 1 || targets[0] != "K1" {
		t.Fatalf("targets not trimmed/coerced: %v", receivedBody["targets"])
	}
	if receivedBody["max_items"] != 20.0 || receivedBody["include_candidates"] != true {
		t.Fatalf("numeric/bool coercion broken: %v", receivedBody)
	}
	for _, want := range []string{"K1", "миграц", "lexical-idf.v1"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("result missing %q: %s", want, result.Content)
		}
	}
}

func TestKnowledgeExploreToolValidationAndErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"search requires a question"}`))
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL, APIToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", Name: "knowledge_explore", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("knowledge_explore: %v", err)
	}
	if !strings.Contains(result.Content, "error: operation is required") {
		t.Fatalf("expected operation validation error, got %q", result.Content)
	}

	result, err = activities.RunTool(context.Background(), ToolRequest{
		Project: "", Name: "knowledge_explore", Arguments: map[string]any{"operation": "search"},
	})
	if err != nil {
		t.Fatalf("knowledge_explore: %v", err)
	}
	if !strings.Contains(result.Content, "error: project is required") {
		t.Fatalf("expected project validation error, got %q", result.Content)
	}

	result, err = activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", Name: "knowledge_explore", Arguments: map[string]any{"operation": "search"},
	})
	if err != nil {
		t.Fatalf("knowledge_explore: %v", err)
	}
	if !strings.Contains(result.Content, "error:") || !strings.Contains(result.Content, "422") {
		t.Fatalf("expected HTTP error surface, got %q", result.Content)
	}
}
