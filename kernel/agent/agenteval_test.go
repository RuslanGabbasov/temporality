package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
)

// agentEvalWorkspaceStub fakes the workspace API surface the agent evaluator
// touches: the agent header, its versions, the effective prompt, the suite and
// the run-recording endpoint.
func agentEvalWorkspaceStub(t *testing.T, agent, versions, prompt, suite string) (*httptest.Server, *evalRecorder) {
	t.Helper()
	recorder := &evalRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/agents/coder":
			w.Write([]byte(agent))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/agents/coder/versions":
			w.Write([]byte(versions))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/agents/coder/prompt":
			w.Write([]byte(prompt))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/agents/coder/evaluation-suite":
			w.Write([]byte(suite))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspace/agents/coder/evaluation-runs":
			_ = json.NewDecoder(r.Body).Decode(&recorder.recorded)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

const agentEvalSuite = `{"cases":[{"name":"verifies result","input":"Fix the failing test","must_contain":["verify"],"must_not_contain":["assume"]}]}`
const agentEvalVersions = `{"versions":[
	{"version":2,"compiled_prompt":"v2 prompt: verify everything"},
	{"version":1,"compiled_prompt":"v1 prompt"}
]}`
const agentEvalCurrent = `{"id":"coder","name":"Coder","definition_version":2}`
const agentEvalPrompt = `{"agent_id":"coder","version":2,"source":"definition","prompt":"v2 current prompt"}`

func TestAgentEvaluateCurrentVersionUsesEffectivePrompt(t *testing.T) {
	model := fixedAnswerModel(t, "I will verify the change with the relevant tests.")
	server, recorder := agentEvalWorkspaceStub(t, agentEvalCurrent, agentEvalVersions, agentEvalPrompt, agentEvalSuite)
	outbox := &captureOutbox{}
	activities := &Activities{
		HTTP: server.Client(),
		Model: llm.New(llm.Config{
			BaseURL: model.URL, Model: "m",
		}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
		Events: outbox, SourceID: "kernel",
	}

	summary, err := activities.RunAgentEvaluation(context.Background(), "coder", 0)
	if err != nil {
		t.Fatalf("RunAgentEvaluation: %v", err)
	}
	if !strings.Contains(summary, "1 passed, 0 failed") || !strings.Contains(summary, "coder v2") {
		t.Fatalf("summary = %q", summary)
	}
	recorder.mu.Lock()
	recorded := recorder.recorded
	recorder.mu.Unlock()
	if recorded == nil {
		t.Fatal("evaluation run was not recorded")
	}
	if recorded["agent_version"] != float64(2) || recorded["passed"] != float64(1) {
		t.Fatalf("recorded run = %#v", recorded)
	}
	if len(outbox.events) == 0 || outbox.events[0].Type != "agent.evaluation.completed" {
		t.Fatalf("expected agent.evaluation.completed event, got %+v", outbox.events)
	}
}

// A pinned version must test the compiled prompt snapshot of that exact
// version, not the current effective prompt.
func TestAgentEvaluatePinnedVersionUsesSnapshot(t *testing.T) {
	var systemUsed string
	var mu sync.Mutex
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		for _, m := range payload.Messages {
			if m.Role == "system" {
				mu.Lock()
				systemUsed = m.Content
				mu.Unlock()
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "verify"}}},
		})
	}))
	t.Cleanup(model.Close)
	server, recorder := agentEvalWorkspaceStub(t, agentEvalCurrent, agentEvalVersions, agentEvalPrompt, agentEvalSuite)
	activities := &Activities{
		HTTP:         server.Client(),
		Model:        llm.New(llm.Config{BaseURL: model.URL, Model: "m"}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}

	summary, err := activities.RunAgentEvaluation(context.Background(), "coder", 1)
	if err != nil {
		t.Fatalf("RunAgentEvaluation: %v", err)
	}
	if !strings.Contains(summary, "coder v1") {
		t.Fatalf("summary = %q", summary)
	}
	mu.Lock()
	used := systemUsed
	mu.Unlock()
	if used != "v1 prompt" {
		t.Fatalf("pinned version must test the v1 snapshot, system = %q", used)
	}
	recorder.mu.Lock()
	recorded := recorder.recorded
	recorder.mu.Unlock()
	if recorded["agent_version"] != float64(1) {
		t.Fatalf("recorded run = %#v", recorded)
	}
}

func TestAgentEvaluateUnknownVersionIsAnError(t *testing.T) {
	server, _ := agentEvalWorkspaceStub(t, agentEvalCurrent, agentEvalVersions, agentEvalPrompt, agentEvalSuite)
	activities := &Activities{
		HTTP:         server.Client(),
		Model:        llm.New(llm.Config{BaseURL: "http://localhost:1", Model: "m"}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}
	_, err := activities.RunAgentEvaluation(context.Background(), "coder", 7)
	if err == nil || !strings.Contains(err.Error(), "version 7 not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentEvaluateEmptySuiteIsAnError(t *testing.T) {
	server, _ := agentEvalWorkspaceStub(t, agentEvalCurrent, agentEvalVersions, agentEvalPrompt, `{"cases":[]}`)
	activities := &Activities{
		HTTP:         server.Client(),
		Model:        llm.New(llm.Config{BaseURL: "http://localhost:1", Model: "m"}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}
	_, err := activities.RunAgentEvaluation(context.Background(), "coder", 0)
	if err == nil || !strings.Contains(err.Error(), "no evaluation cases") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentEvaluateFailsCaseOnUnexpectedPattern(t *testing.T) {
	model := fixedAnswerModel(t, "I assume it works, no checks needed.")
	server, _ := agentEvalWorkspaceStub(t, agentEvalCurrent, agentEvalVersions, agentEvalPrompt, agentEvalSuite)
	activities := &Activities{
		HTTP:         server.Client(),
		Model:        llm.New(llm.Config{BaseURL: model.URL, Model: "m"}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}
	summary, err := activities.RunAgentEvaluation(context.Background(), "coder", 0)
	if err != nil {
		t.Fatalf("RunAgentEvaluation: %v", err)
	}
	if !strings.Contains(summary, "0 passed, 1 failed") {
		t.Fatalf("summary = %q", summary)
	}
	if !strings.Contains(summary, "missed: verify") || !strings.Contains(summary, "unexpected: assume") {
		t.Fatalf("failure details missing patterns: %q", summary)
	}
}

func TestAgentEvaluateUnknownAgentIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	activities := &Activities{
		HTTP:         server.Client(),
		Model:        llm.New(llm.Config{BaseURL: "http://localhost:1", Model: "m"}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}
	_, err := activities.RunAgentEvaluation(context.Background(), "ghost", 0)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}
