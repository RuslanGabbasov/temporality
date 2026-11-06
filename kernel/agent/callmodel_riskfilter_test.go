package agent

// A provider risk filter that rejects a request returns its refusal AS the
// completion content (200 + plain text). Left unguarded that becomes the
// agent's answer — the run completes with "The request was rejected because
// it was considered high risk" and the operator hunts their own approval
// gates for a rejection that never happened there (the 2026-11-06 incident).
// The guard turns it into a retryable model error instead.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
)

func TestCallModelRiskFilterRefusalFailsInsteadOfAnswering(t *testing.T) {
	refusal := "The request was rejected because it was considered high risk"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": refusal}}},
		})
	}))
	defer provider.Close()

	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"provider_id": "prov-x", "base_url": provider.URL, "api_key": "key-1", "model": "mimo-x",
		})
	}))
	defer workspace.Close()

	activities := &Activities{
		HTTP:         &http.Client{},
		WorkspaceURL: workspace.URL,
		SourceID:     "kernel",
	}

	completion, err := activities.CallModel(context.Background(), ModelRequest{
		RunID: "run-risk", Turn: 3, Project: "proj-1", Model: "mimo-x",
		Messages: []llm.Message{{Role: "user", Content: "продолжай"}},
	})
	if err == nil {
		t.Fatal("a risk-filter refusal must fail the call, not answer it")
	}
	if !strings.Contains(err.Error(), "risk filter rejected") {
		t.Fatalf("the error must name the risk filter, got: %v", err)
	}
	if completion.Content != "" {
		t.Fatalf("no completion must be returned for a rejected request, got %q", completion.Content)
	}
}

// A refusal is the whole answer; an assistant that merely mentions the
// sentence while working, or proposes tool calls, is not refusing.
func TestProviderRejectionMatchesWholeRefusalOnly(t *testing.T) {
	cases := []struct {
		content string
		refused bool
	}{
		{"The request was rejected because it was considered high risk", true},
		{"  THE REQUEST WAS REJECTED BECAUSE IT WAS CONSIDERED HIGH RISK\n", true},
		{"The request was rejected because it was considered high risk.", true},
		{"The request was rejected because it was considered high risk — so I tried a different approach and finished the task.", false},
		{"The request was rejected because it was considered high risk. Please rephrase.", false},
		{"", false},
	}
	for _, c := range cases {
		refused := providerRejection(c.content) != ""
		if refused != c.refused {
			t.Fatalf("providerRejection(%q) = %v, want %v", c.content, refused, c.refused)
		}
	}
	// Azure's content-management wording is the other common shape.
	if providerRejection("The response was filtered due to the prompt triggering Azure's content management policy") == "" {
		t.Fatal("azure content-management refusal must match")
	}
}
