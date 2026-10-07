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

// evalWorkspaceStub fakes the workspace API surface the evaluator touches:
// the skill, its versions, the suite and the run-recording endpoint.
func evalWorkspaceStub(t *testing.T, skill, versions, suite string) (*httptest.Server, *evalRecorder) {
	t.Helper()
	recorder := &evalRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/skills/deploy-service":
			w.Write([]byte(skill))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/skills/deploy-service/versions":
			w.Write([]byte(versions))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/skills/deploy-service/evaluation-suite":
			w.Write([]byte(suite))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspace/skills/deploy-service/evaluation-runs":
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

type evalRecorder struct {
	mu       sync.Mutex
	recorded map[string]any
}

// fixedAnswerModel fakes a provider that always returns one plain answer.
func fixedAnswerModel(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": answer}}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSkillEvaluateRunsSuiteAndRecordsRun(t *testing.T) {
	model := fixedAnswerModel(t, "The service was deployed successfully and is healthy.")
	skill := `{"id":"deploy-service","name":"Deploy service","version":"1.2.0","markdown":"## Purpose\nDeploy."}`
	versions := `{"versions":[{"version":"1.2.0","status":"active"}]}`
	suite := `{"cases":[{"name":"health check","input":"Deploy and verify","must_contain":["deployed"],"must_not_contain":["maybe"]}]}`
	server, recorder := evalWorkspaceStub(t, skill, versions, suite)
	outbox := &captureOutbox{}
	activities := &Activities{
		HTTP: server.Client(),
		Model: llm.New(llm.Config{
			BaseURL: model.URL, Model: "m",
		}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
		Events: outbox, SourceID: "kernel",
	}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "run-eval", Project: "demo",
		Name: "skill_evaluate", Arguments: map[string]any{"skill_id": "deploy-service"},
	})
	if err != nil {
		t.Fatalf("skill_evaluate: %v", err)
	}
	if !strings.Contains(result.Content, "1 passed, 0 failed") {
		t.Fatalf("summary = %q", result.Content)
	}
	recorder.mu.Lock()
	recorded := recorder.recorded
	recorder.mu.Unlock()
	if recorded == nil {
		t.Fatal("evaluation run was not recorded")
	}
	if recorded["skill_version"] != "1.2.0" || recorded["passed"] != float64(1) {
		t.Fatalf("recorded run = %#v", recorded)
	}
	if len(outbox.events) == 0 || outbox.events[0].Type != "skill.evaluation.completed" {
		t.Fatalf("expected skill.evaluation.completed event, got %+v", outbox.events)
	}
}

func TestSkillEvaluateFailsCaseOnMissingPattern(t *testing.T) {
	answer := `{"choices":[{"message":{"content":"the deployment maybe skipped tests"}}]}`
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(answer))
	}))
	t.Cleanup(model.Close)
	skill := `{"id":"deploy-service","name":"Deploy service","version":"1.2.0","markdown":"## Purpose\nDeploy."}`
	versions := `{"versions":[{"version":"1.2.0","status":"active"}]}`
	suite := `{"cases":[{"name":"strict","input":"Deploy","must_contain":["deployed"],"must_not_contain":["maybe"]}]}`
	server, _ := evalWorkspaceStub(t, skill, versions, suite)
	activities := &Activities{
		HTTP:         server.Client(),
		Model:        llm.New(llm.Config{BaseURL: model.URL, Model: "m"}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "run-eval", Project: "demo",
		Name: "skill_evaluate", Arguments: map[string]any{"skill_id": "deploy-service"},
	})
	if err != nil {
		t.Fatalf("skill_evaluate: %v", err)
	}
	if !strings.Contains(result.Content, "0 passed, 1 failed") {
		t.Fatalf("summary = %q", result.Content)
	}
	if !strings.Contains(result.Content, "missed: deployed") || !strings.Contains(result.Content, "unexpected: maybe") {
		t.Fatalf("failure details missing patterns: %q", result.Content)
	}
}

func TestSkillEvaluateEmptySuiteIsAnError(t *testing.T) {
	skill := `{"id":"deploy-service","name":"Deploy service","version":"1.2.0","markdown":"x"}`
	versions := `{"versions":[{"version":"1.2.0","status":"active"}]}`
	server, _ := evalWorkspaceStub(t, skill, versions, `{"cases":[]}`)
	activities := &Activities{
		HTTP: server.Client(), Model: llm.New(llm.Config{BaseURL: "http://localhost:1", Model: "m"}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}
	result, err := activities.RunTool(context.Background(), ToolRequest{
		Name: "skill_evaluate", Arguments: map[string]any{"skill_id": "deploy-service"},
	})
	if err != nil {
		t.Fatalf("skill_evaluate: %v", err)
	}
	if !strings.Contains(result.Content, "no evaluation cases") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestSkillDiffCurrentVsDraft(t *testing.T) {
	versions := `{"versions":[
		{"version":"1.3.0","status":"draft","markdown":"## Purpose\nDeploy.\nWait for health.","manifest":{"id":"deploy-service","capabilities":["deploy","verify"],"tools":["run_command"]}},
		{"version":"1.2.0","status":"active","markdown":"## Purpose\nDeploy.","manifest":{"id":"deploy-service","capabilities":["deploy"],"tools":["run_command","curl"]}}
	]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/workspace/skills/deploy-service/versions" {
			w.Write([]byte(versions))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	activities := &Activities{
		HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal",
	}
	result, err := activities.RunTool(context.Background(), ToolRequest{
		Name: "skill_diff", Arguments: map[string]any{"skill_id": "deploy-service"},
	})
	if err != nil {
		t.Fatalf("skill_diff: %v", err)
	}
	for _, want := range []string{"1.2.0 → 1.3.0", "capabilities: (none) → + verify", "tools: - curl → (none)", "SKILL.md: +1 -0 lines"} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("diff missing %q in:\n%s", want, result.Content)
		}
	}
}
