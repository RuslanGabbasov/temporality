package agent

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/aml/llm"
)

func TestVerificationClass(t *testing.T) {
	cases := []struct {
		command []string
		want    string
	}{
		{[]string{"pytest", "-q"}, "test"},
		{[]string{"go", "test", "./..."}, "test"},
		{[]string{"go", "vet", "./..."}, "test"},
		{[]string{"cargo", "test"}, "test"},
		{[]string{"npm", "test"}, "test"},
		{[]string{"cargo", "check"}, "test"},
		{[]string{"npm", "run", "build"}, "build"},
		{[]string{"npm", "run", "test"}, "test"},
		{[]string{"go", "build", "./..."}, "build"},
		{[]string{"make", "build"}, "build"},
		{[]string{"ls", "-la"}, ""},
		{[]string{"echo", "test"}, ""},
		{[]string{"cat", "build.md"}, ""},
		{[]string{"go", "run", "main.go"}, ""},
		{nil, ""},
	}
	for _, item := range cases {
		if got := verificationClass(item.command); got != item.want {
			t.Errorf("verificationClass(%q) = %q, want %q", item.command, got, item.want)
		}
	}
}

func exitCode(value int) *int { return &value }

func TestExecutionObservationProposal(t *testing.T) {
	run := RunInput{RunID: "run-1", Project: "repo", WorkspacePath: "/workspace/task"}
	command := map[string]any{"command": []any{"go", "test", "./..."}}

	if proposal := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: command}, ToolResult{ExitCode: exitCode(0)}); proposal == nil {
		t.Fatal("successful verification command must produce a proposal")
	} else {
		if proposal.Policy != ExecutionObservationPolicy {
			t.Fatalf("policy = %q", proposal.Policy)
		}
		if !strings.HasPrefix(proposal.KnowledgeID, "auto/") {
			t.Fatalf("knowledge id = %q", proposal.KnowledgeID)
		}
		if !strings.Contains(proposal.Proposition, "`go test ./...` exited 0") || !strings.Contains(proposal.Proposition, "/workspace/task") {
			t.Fatalf("proposition = %q", proposal.Proposition)
		}
	}
	if proposal := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: command}, ToolResult{ExitCode: exitCode(1)}); proposal != nil {
		t.Fatal("failing command must not produce a proposal")
	}
	if proposal := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: command}, ToolResult{}); proposal != nil {
		t.Fatal("unknown exit code must not produce a proposal")
	}
	if proposal := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: map[string]any{"command": []any{"ls", "-la"}}}, ToolResult{ExitCode: exitCode(0)}); proposal != nil {
		t.Fatal("non-verification command must not produce a proposal")
	}
	if proposal := executionObservationProposal(run, llm.ToolCall{Name: "echo", Args: map[string]any{"text": "test"}}, ToolResult{ExitCode: exitCode(0)}); proposal != nil {
		t.Fatal("non-sandbox tool must not produce a proposal")
	}
	// Identity is project-scoped and run-independent so repeated successes
	// across runs reuse one node instead of duplicating it.
	stable := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: command}, ToolResult{ExitCode: exitCode(0)})
	otherRun := run
	otherRun.RunID = "run-2"
	if stable.KnowledgeID != executionObservationProposal(otherRun, llm.ToolCall{Name: "run_command", Args: command}, ToolResult{ExitCode: exitCode(0)}).KnowledgeID {
		t.Fatal("knowledge id must be stable across runs")
	}
	other := run
	other.WorkspacePath = "/workspace/other"
	if stable.KnowledgeID == executionObservationProposal(other, llm.ToolCall{Name: "run_command", Args: command}, ToolResult{ExitCode: exitCode(0)}).KnowledgeID {
		t.Fatal("knowledge id must differ between workspaces")
	}
	otherProject := run
	otherProject.Project = "repo-b"
	if stable.KnowledgeID == executionObservationProposal(otherProject, llm.ToolCall{Name: "run_command", Args: command}, ToolResult{ExitCode: exitCode(0)}).KnowledgeID {
		t.Fatal("knowledge id must differ between projects")
	}
}
