package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// agentProposeWorkspaceStub fakes the workspace API surface the agent_propose
// tool touches: the target agent header and the proposals endpoint.
func agentProposeWorkspaceStub(t *testing.T, agent string) (*httptest.Server, *agentProposeRecorder) {
	t.Helper()
	recorder := &agentProposeRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/agents/qa":
			if agent == "" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(agent))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspace/agents/qa/proposals":
			_ = json.NewDecoder(r.Body).Decode(&recorder.proposed)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "base_version": 3, "status": "pending"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

type agentProposeRecorder struct {
	mu       sync.Mutex
	proposed map[string]any
}

const agentProposeTarget = `{"id":"qa","name":"QA","definition_version":3,"definition":{"capabilities":{"run_commands":true},"constraints":["never merge"]}}`

// A proposal must land as pending with automatic provenance (run + operation
// refs) and never carry capabilities, whatever the caller supplies.
func TestAgentProposeLandsPendingWithProvenance(t *testing.T) {
	server, recorder := agentProposeWorkspaceStub(t, agentProposeTarget)
	activities := &Activities{
		HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "run-9", OperationID: "run-9/turn/03/call-4", Project: "demo",
		Name: "agent_propose",
		Arguments: map[string]any{
			"agent_id":     "qa",
			"problem":      "keeps declaring done without running the suite",
			"change":       "require a green test run before declaring done",
			"effect":       "fewer false completions",
			"evidence":     []any{"knowledge:ext/abc"},
			"capabilities": map[string]any{"network": true},
		},
	})
	if err != nil {
		t.Fatalf("agent_propose: %v", err)
	}
	parsed := decodeToolJSON(t, result.Content)
	if parsed["state"] != "pending" || parsed["proposal_id"] != float64(7) {
		t.Fatalf("result = %v", parsed)
	}
	if parsed["next_step"] == "" {
		t.Fatal("next_step must tell the agent a human decides")
	}
	recorder.mu.Lock()
	proposed := recorder.proposed
	recorder.mu.Unlock()
	if proposed == nil {
		t.Fatal("proposal payload missing")
	}
	if proposed["author"] != "agent-run:run-9" {
		t.Fatalf("author = %v, want agent-run:run-9", proposed["author"])
	}
	evidence, _ := proposed["evidence"].([]any)
	if len(evidence) != 3 || evidence[0] != "run:run-9" || evidence[1] != "knowledge:ext/abc" || evidence[2] != "op:run-9/turn/03/call-4" {
		t.Fatalf("evidence = %v, want run ref, caller refs, operation ref", evidence)
	}
	// No constraints/completion were proposed, so the payload carries no
	// definition at all: the server keeps the current one. Capabilities are
	// dropped either way — a proposal never grants powers.
	if definition, has := proposed["definition"]; has {
		if _, caps := definition.(map[string]any)["capabilities"]; caps {
			t.Fatal("capabilities must never be proposed (server copies them from the current definition)")
		}
	}
}

// The proposed definition may replace constraints and completion only.
func TestAgentProposeReplacesConstraintsAndCompletion(t *testing.T) {
	server, recorder := agentProposeWorkspaceStub(t, agentProposeTarget)
	activities := &Activities{
		HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}

	_, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "run-10", Name: "agent_propose",
		Arguments: map[string]any{
			"agent_id":    "qa",
			"problem":     "noisy completion checks",
			"change":      "sharper checks",
			"constraints": []any{"never merge", "always cite evidence"},
			"completion":  []any{"go build ./...", "go test ./..."},
		},
	})
	if err != nil {
		t.Fatalf("agent_propose: %v", err)
	}
	recorder.mu.Lock()
	proposed := recorder.proposed
	recorder.mu.Unlock()
	definition, _ := proposed["definition"].(map[string]any)
	constraints, _ := definition["constraints"].([]any)
	if len(constraints) != 2 || constraints[1] != "always cite evidence" {
		t.Fatalf("constraints = %v", constraints)
	}
	completion, _ := definition["completion"].([]any)
	if len(completion) != 2 || completion[1] != "go test ./..." {
		t.Fatalf("completion = %v", completion)
	}
}

func TestAgentProposeRequiresProblemAndChange(t *testing.T) {
	server, _ := agentProposeWorkspaceStub(t, agentProposeTarget)
	activities := &Activities{
		HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}
	for _, args := range []map[string]any{
		{"agent_id": "qa"},
		{"agent_id": "qa", "problem": "something"},
		{"agent_id": "qa", "change": "something"},
		{"problem": "p", "change": "c"},
	} {
		result, err := activities.RunTool(context.Background(), ToolRequest{RunID: "run-11", Name: "agent_propose", Arguments: args})
		if err != nil {
			t.Fatalf("agent_propose(%v): %v", args, err)
		}
		if result.Content == "" || result.Content[:6] != "error:" {
			t.Fatalf("agent_propose(%v) must fail with an error content, got %q", args, result.Content)
		}
	}
}

func TestAgentProposeUnknownAgentIsAnError(t *testing.T) {
	server, _ := agentProposeWorkspaceStub(t, "")
	activities := &Activities{
		HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}
	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "run-12", Name: "agent_propose",
		Arguments: map[string]any{"agent_id": "qa", "problem": "p", "change": "c"},
	})
	if err != nil {
		t.Fatalf("agent_propose: %v", err)
	}
	if want := `error: agent "qa" not found`; result.Content != want {
		t.Fatalf("content = %q, want %q", result.Content, want)
	}
}
