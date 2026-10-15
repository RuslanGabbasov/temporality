package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Artifact lifecycle tools (docs/artifact-lifecycle.md §6): create pins
// origin_run server-side, submit refuses without reviewers, and every call
// surfaces workspace errors as tool errors (not activity failures).

func TestArtifactCreatePinsOriginRun(t *testing.T) {
	var gotPayload map[string]any
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "req-spec", "status": "draft", "version": 1, "kind": "work",
		})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-42", Name: "artifact_create",
		Arguments: map[string]any{
			"title": "Requirements spec", "content": "# spec", "saga_id": "saga-7",
		},
	})
	if err != nil {
		t.Fatalf("artifact_create: %v", err)
	}
	if gotPath != "/v1/workspace/artifacts" {
		t.Fatalf("want POST /v1/workspace/artifacts, got %s", gotPath)
	}
	if gotPayload["origin_run"] != "run-42" {
		t.Fatalf("origin_run must be pinned to the calling run, got %v", gotPayload["origin_run"])
	}
	if gotPayload["kind"] != "work" {
		t.Fatalf("agent drafts default to kind=work, got %v", gotPayload["kind"])
	}
	if gotPayload["saga_id"] != "saga-7" {
		t.Fatalf("saga_id must pass through, got %v", gotPayload["saga_id"])
	}
	var out struct {
		ArtifactID string `json:"artifact_id"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("parse result: %v (content %q)", err, result.Content)
	}
	if out.ArtifactID != "req-spec" || out.Status != "draft" {
		t.Fatalf("want req-spec draft, got %+v", out)
	}
}

func TestArtifactCreateRequiresTitle(t *testing.T) {
	activities := &Activities{WorkspaceURL: "http://unused.example"}
	result, err := activities.RunTool(context.Background(), ToolRequest{
		Name: "artifact_create", Arguments: map[string]any{"content": "no title"},
	})
	if err != nil {
		t.Fatalf("artifact_create: %v", err)
	}
	if !strings.HasPrefix(result.Content, "error:") {
		t.Fatalf("want a tool error, got %q", result.Content)
	}
}

func TestArtifactSubmitRequiresReviewers(t *testing.T) {
	activities := &Activities{WorkspaceURL: "http://unused.example"}
	result, err := activities.RunTool(context.Background(), ToolRequest{
		Name: "artifact_submit", Arguments: map[string]any{"artifact_id": "req-spec"},
	})
	if err != nil {
		t.Fatalf("artifact_submit: %v", err)
	}
	if !strings.Contains(result.Content, "reviewers is required") {
		t.Fatalf("want reviewers validation error, got %q", result.Content)
	}
}

func TestArtifactSubmitSendsRoundAndExplainsHumanGate(t *testing.T) {
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/artifacts/req-spec/submit" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "round-1", "artifact_version": 2, "mode": "all", "status": "open",
		})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-42", Name: "artifact_submit",
		Arguments: map[string]any{
			"artifact_id": "req-spec", "reviewers": []any{"user:ivanov", "project_owner"}, "note": "ready",
		},
	})
	if err != nil {
		t.Fatalf("artifact_submit: %v", err)
	}
	reviewers, _ := gotPayload["reviewers"].([]any)
	if len(reviewers) != 2 || reviewers[0] != "user:ivanov" {
		t.Fatalf("reviewers must pass through as strings, got %v", gotPayload["reviewers"])
	}
	var out struct {
		ReviewID string `json:"review_id"`
		Version  int    `json:"version"`
		Note     string `json:"note"`
	}
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("parse result: %v (content %q)", err, result.Content)
	}
	if out.ReviewID != "round-1" || out.Version != 2 {
		t.Fatalf("want round-1 v2, got %+v", out)
	}
	if !strings.Contains(out.Note, "human") {
		t.Fatalf("result must explain the human decision gate, got %q", out.Note)
	}
}

func TestArtifactUpdateSurfacesLockedConflict(t *testing.T) {
	var sawPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/artifacts/req-spec" || r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&sawPayload)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"artifact is locked: artifact is in_review (locked while a review is open)"}`))
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", Name: "artifact_update",
		Arguments: map[string]any{"artifact_id": "req-spec", "content": "# v2"},
	})
	if err != nil {
		t.Fatalf("artifact_update: %v", err)
	}
	if !strings.HasPrefix(result.Content, "error:") || !strings.Contains(result.Content, "HTTP 409") {
		t.Fatalf("want a surfaced 409 tool error, got %q", result.Content)
	}
	// The update never mentioned the saga: the payload must omit the field so
	// the server keeps the stored link (nil ≠ explicitly cleared).
	if _, ok := sawPayload["saga_id"]; ok {
		t.Fatalf("saga_id must be omitted when not passed, got %v", sawPayload["saga_id"])
	}
}

// The workspace server side keeps the invariant: submit is idempotent per open
// round, approve/reject refuse the kernel-internal identity. Those live behind
// the HTTP surface tested in cmd/agent-kernel; here we only pin the tool wire
// format above. This guard makes sure the tools never grow an approve call.
func TestArtifactToolsHaveNoApprove(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/approve") || strings.HasSuffix(r.URL.Path, "/reject") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}
	for _, name := range []string{"artifact_create", "artifact_update", "artifact_submit"} {
		args := map[string]any{"artifact_id": "x", "title": "T", "content": "c", "reviewers": []any{"project_owner"}}
		if _, err := activities.RunTool(context.Background(), ToolRequest{Project: "p", Name: name, Arguments: args}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, p := range paths {
		if strings.Contains(p, "/approve") || strings.Contains(p, "/reject") {
			t.Fatalf("agent tools must never resolve reviews themselves, saw %s", p)
		}
	}
}
