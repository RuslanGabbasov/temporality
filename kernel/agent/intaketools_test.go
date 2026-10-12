package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Intake tool tests: identity flows from the activity (ActorID, RunID), never
// from model arguments; API failures surface as tool errors, not payloads.

func TestIntakeSearchProjectsSendsSenderFromActor(t *testing.T) {
	var gotSender, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/intake/projects" {
			http.NotFound(w, r)
			return
		}
		gotSender = r.URL.Query().Get("sender")
		gotQuery = r.URL.Query().Get("query")
		_ = json.NewEncoder(w).Encode(map[string]any{"projects": []any{
			map[string]any{"id": "graphmap", "name": "GraphMap"},
		}})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "intake-m1", Project: "intake", ActorID: "u1", Name: "search_projects",
		Arguments: map[string]any{"query": "граф"},
	})
	if err != nil {
		t.Fatalf("search_projects: %v", err)
	}
	if gotSender != "u1" {
		t.Fatalf("sender = %q, want the run actor u1", gotSender)
	}
	if gotQuery != "граф" {
		t.Fatalf("query = %q", gotQuery)
	}
	if !strings.Contains(result.Content, "graphmap") {
		t.Fatalf("unexpected content: %q", result.Content)
	}

	// No actor (a non-intake run borrowing the tool): the model gets an error,
	// not a sender-less listing.
	result, err = activities.RunTool(context.Background(), ToolRequest{
		RunID: "r", Project: "p", Name: "search_projects", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("search_projects without actor: %v", err)
	}
	if !strings.Contains(result.Content, "error:") {
		t.Fatalf("expected error without actor, got %q", result.Content)
	}
}

func TestIntakeDecideValidatesClassificationLocally(t *testing.T) {
	activities := &Activities{HTTP: http.DefaultClient, WorkspaceURL: "http://unused"}
	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "intake-m1", Name: "intake_decide",
		Arguments: map[string]any{"classification": "urgent"},
	})
	if err != nil {
		t.Fatalf("intake_decide: %v", err)
	}
	if !strings.Contains(result.Content, "error:") {
		t.Fatalf("expected local validation error, got %q", result.Content)
	}
}

func TestIntakeStartTaskPostsPayloadAndReportsAPIErrors(t *testing.T) {
	var posted map[string]any
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "/start-task") {
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Errorf("decode body: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "inbound-matrix-m1", "run_id": "inbound-matrix-m1-20260101-000000"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "intake-m1", Project: "intake", ActorID: "u1", Name: "start_task",
		Arguments: map[string]any{
			"project_id": "graphmap", "title": "Смета", "prompt": "Подготовь смету...", "agent_id": "coder",
		},
	})
	if err != nil {
		t.Fatalf("start_task: %v", err)
	}
	if want := "/v1/intake/runs/intake-m1/start-task"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if posted["project_id"] != "graphmap" || posted["title"] != "Смета" || posted["agent_id"] != "coder" {
		t.Fatalf("payload: %+v", posted)
	}
	if !strings.Contains(result.Content, "inbound-matrix-m1") {
		t.Fatalf("unexpected content: %q", result.Content)
	}

	// A 403 from the API (sender lacks rights) must reach the model as an
	// error: it changes the dialog (ask the user for another project).
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"effective role reader is not enough"}`, http.StatusForbidden)
	}))
	defer errorServer.Close()
	activities.WorkspaceURL = errorServer.URL
	activities.HTTP = errorServer.Client()
	result, err = activities.RunTool(context.Background(), ToolRequest{
		RunID: "intake-m1", Name: "start_task",
		Arguments: map[string]any{"project_id": "graphmap", "title": "t", "prompt": "p"},
	})
	if err != nil {
		t.Fatalf("start_task error case: %v", err)
	}
	if !strings.Contains(result.Content, "error:") || !strings.Contains(result.Content, "403") {
		t.Fatalf("expected API error surfaced, got %q", result.Content)
	}
}
