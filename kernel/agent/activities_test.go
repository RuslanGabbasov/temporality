package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/mcpclient"
	"github.com/temporality-project/temporality/kernel/sandbox"
)

type recordingSandbox struct {
	mu       sync.Mutex
	commands [][]string
}

func (r *recordingSandbox) SandboxRoot() string                          { return os.TempDir() }
func (r *recordingSandbox) ResolveWorkspace(path string) (string, error) { return path, nil }

func (r *recordingSandbox) Execute(_ context.Context, request sandbox.Request) (sandbox.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, request.Command)
	return sandbox.Result{Output: "ok", ExitCode: 0}, nil
}

func runCommand(t *testing.T, activities *Activities, runID, operationID string) error {
	t.Helper()
	_, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: runID, OperationID: operationID, Name: "run_command",
		Arguments: map[string]any{"command": []string{"touch", "evidence.txt"}},
	})
	return err
}

func TestRunToolWithoutFaultExecutesNormally(t *testing.T) {
	runner := &recordingSandbox{}
	activities := &Activities{Sandbox: runner}

	if err := runCommand(t, activities, "run-plain", "frame-1/call-1"); err != nil {
		t.Fatalf("unexpected failure without fault env: %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("expected one execution, got %d", len(runner.commands))
	}
}

func TestSkillSearchReportsHTTPErrorsAsToolErrors(t *testing.T) {
	// The registry lives on the kernel's own workspace API; a miss there must
	// surface as a tool error, never as a "404 page not found" payload the
	// model could mistake for usable content.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/skills" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer internal" {
			t.Errorf("authorization = %q, want internal workspace token", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"skills": []any{}})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", Name: "skill_search", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("skill_search: %v", err)
	}
	if !strings.Contains(result.Content, "skills") {
		t.Fatalf("unexpected content: %q", result.Content)
	}

	result, err = activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", Name: "skill_inspect", Arguments: map[string]any{"skill_id": "missing"},
	})
	if err != nil {
		t.Fatalf("skill_inspect: %v", err)
	}
	if !strings.Contains(result.Content, "error:") || strings.Contains(result.Content, "404 page not found") {
		t.Fatalf("expected a tool error for missing skill, got %q", result.Content)
	}
}

func TestSkillSearchMatchesAnyKeyword(t *testing.T) {
	// A multi-word query must match per keyword, not as one literal substring:
	// "code review quality gate" should still surface review-related skills.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"skills": []any{
			map[string]any{"id": "code-review", "name": "Code Review", "version": "1.0.0", "manifest": map[string]any{"capabilities": []string{"review"}, "tools": []string{}}},
			map[string]any{"id": "review-quality-gate", "name": "Review Quality Gate", "version": "1.0.0", "manifest": map[string]any{"capabilities": []string{"quality"}, "tools": []string{}}},
			map[string]any{"id": "deploy", "name": "Deploy", "version": "1.0.0", "manifest": map[string]any{"capabilities": []string{"deploy"}, "tools": []string{}}},
		}})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	type page struct {
		Skills []struct {
			ID string `json:"id"`
		} `json:"skills"`
		Count int `json:"count"`
	}
	run := func(query string) page {
		t.Helper()
		result, err := activities.RunTool(context.Background(), ToolRequest{
			Project: "demo", Name: "skill_search", Arguments: map[string]any{"query": query},
		})
		if err != nil {
			t.Fatalf("skill_search(%q): %v", query, err)
		}
		var out page
		if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
			t.Fatalf("skill_search(%q): parse result: %v (content %q)", query, err, result.Content)
		}
		return out
	}

	got := run("code review quality gate")
	if got.Count != 2 {
		t.Fatalf("want 2 skills for multi-word query, got %d", got.Count)
	}
	if ids := []string{got.Skills[0].ID, got.Skills[1].ID}; ids[0] != "review-quality-gate" || ids[1] != "code-review" {
		t.Fatalf("want [review-quality-gate code-review] ordered by keyword coverage, got %v", ids)
	}

	if got := run("deploy nothing matches here"); got.Count != 1 || got.Skills[0].ID != "deploy" {
		t.Fatalf("want deploy skill on partial keyword match, got %+v", got)
	}

	if got := run("database migration"); got.Count != 0 {
		t.Fatalf("want no skills for unrelated query, got %d", got.Count)
	}
}

func TestSkillSlug(t *testing.T) {
	for input, want := range map[string]string{
		"Deploy Service":      "deploy-service",
		"  Code Review & QA ": "code-review-qa",
		"Ревью кода":          "",
		"":                    "",
	} {
		if got := skillSlug(input); got != want {
			t.Errorf("skillSlug(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRunToolFaultByOperationIDExecutesEffectThenCrashes(t *testing.T) {
	runner := &recordingSandbox{}
	activities := &Activities{Sandbox: runner}
	t.Setenv("KERNEL_FAULT_AFTER_EFFECT", "frame-1/call-1")

	err := runCommand(t, activities, "run-fault-exact", "frame-1/call-1")
	if err == nil || !strings.Contains(err.Error(), "injected worker crash") {
		t.Fatalf("expected injected crash, got %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("effect must land exactly once before the crash, got %d executions", len(runner.commands))
	}
	// A different operation is not affected by the exact-id drill.
	if err := runCommand(t, activities, "run-fault-exact", "frame-2/call-2"); err != nil {
		t.Fatalf("unrelated operation must not fault: %v", err)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("expected two executions total, got %d", len(runner.commands))
	}
}

func TestRunToolFaultByToolNameFaultsFirstPerRun(t *testing.T) {
	runner := &recordingSandbox{}
	activities := &Activities{Sandbox: runner}
	t.Setenv("KERNEL_FAULT_AFTER_EFFECT", "run_command")

	faulted := func(runID, operationID string) bool {
		err := runCommand(t, activities, runID, operationID)
		return err != nil && strings.Contains(err.Error(), "injected worker crash")
	}
	if !faulted("run-drill-a", "frame-1/call-1") {
		t.Fatal("the first matching call in a run must fault")
	}
	if faulted("run-drill-a", "frame-2/call-2") {
		t.Fatal("only the first matching call per run may fault")
	}
	if !faulted("run-drill-b", "frame-1/call-1") {
		t.Fatal("a new run re-arms the drill")
	}
	if len(runner.commands) != 3 {
		t.Fatalf("every call still executes its effect, got %d executions", len(runner.commands))
	}
}

func TestRunToolMCPFaultCommitsEffectBeforeCrash(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KERNEL_MCP_TEST_ROOT", root)
	t.Setenv("KERNEL_MCP_COMMAND", "go")
	args, _ := json.Marshal([]string{"run", "../../examples/test-mcp"})
	t.Setenv("KERNEL_MCP_ARGS", string(args))
	t.Setenv("KERNEL_MCP_ALLOW", "create_issue")
	t.Setenv("KERNEL_FAULT_AFTER_EFFECT", "mcp__create_issue")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := mcpclient.FromEnv(ctx)
	if err != nil {
		t.Fatalf("discover stdio MCP server: %v", err)
	}
	registry := mcpclient.NewRegistry()
	registry.AdoptLegacy(client)
	activities := &Activities{MCP: registry}

	_, err = activities.RunTool(ctx, ToolRequest{
		RunID: "run-mcp-fault", OperationID: "frame-1/call-9", Name: "mcp__create_issue",
		Arguments: map[string]any{"title": "drill", "body": "effect must land"},
	})
	if err == nil || !strings.Contains(err.Error(), "injected worker crash") {
		t.Fatalf("expected injected crash, got %v", err)
	}
	issues, err := os.ReadFile(filepath.Join(root, ".test-issues.log"))
	if err != nil || !strings.Contains(string(issues), "effect must land") {
		t.Fatalf("the MCP effect must commit before the crash: %v", err)
	}
}
