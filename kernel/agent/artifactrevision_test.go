package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The revision path (docs/artifact-lifecycle.md §5 + docs/agent-async-operations
// spirit): an agent may open a new revision of an APPROVED artifact and hand it
// to re-approval, but the decision stays human.

func TestArtifactUpdateNewVersionCallsRevisionEndpoint(t *testing.T) {
	var gotPath, gotMethod string
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "req-spec", "status": "draft", "version": 2,
		})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-42", Name: "artifact_update",
		Arguments: map[string]any{
			"artifact_id": "req-spec", "new_version": true,
			"content":     "# spec v2 — added the caching section",
			"description": "when the caching rules matter",
		},
	})
	if err != nil {
		t.Fatalf("artifact_update new_version: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/workspace/artifacts/req-spec/revision" {
		t.Fatalf("want POST /v1/workspace/artifacts/req-spec/revision, got %s %s", gotMethod, gotPath)
	}
	if gotPayload["content"] != "# spec v2 — added the caching section" {
		t.Fatalf("revision body must pass through, got %v", gotPayload["content"])
	}
	var out struct {
		ArtifactID string `json:"artifact_id"`
		Status     string `json:"status"`
		Version    int    `json:"version"`
		Next       string `json:"next"`
	}
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("result is not JSON: %v: %q", err, result.Content)
	}
	if out.Status != "draft" || out.Version != 2 {
		t.Fatalf("revision must land as draft v2, got %s v%d", out.Status, out.Version)
	}
	if !strings.Contains(out.Next, "artifact_submit") {
		t.Fatalf("the model must be pointed at artifact_submit, got %q", out.Next)
	}
}

func TestArtifactUpdateApprovedWithoutFlagExplainsThePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The kernel PUT refuses agent edits of approved artifacts (409).
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"artifact is approved — agents cannot revise approved documents; ask a human"}`))
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-42", Name: "artifact_update",
		Arguments: map[string]any{"artifact_id": "req-spec", "content": "rewrite"},
	})
	if err != nil {
		t.Fatalf("artifact_update: %v", err)
	}
	if !strings.Contains(result.Content, "new_version=true") || !strings.Contains(result.Content, "artifact_submit") {
		t.Fatalf("the 409 must name the controlled path, got %q", result.Content)
	}
}
