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
		// Shell-wrapped commands attribute to the final script command, which is
		// the only one that determines the recorded exit code.
		{[]string{"sh", "-c", "cd calculator && GOCACHE=$PWD/.gocache go test ./..."}, "test"},
		{[]string{"sh", "-c", "GOCACHE=$PWD/.gocache GOTMPDIR=$PWD/tmp go test -count=1 -v ./..."}, "test"},
		{[]string{"bash", "-c", "npm run build"}, "build"},
		{[]string{"sh", "-c", "cd calculator && ls -la"}, ""},
		// The exit code belongs to the trailing echo, not to go test: a zero exit
		// here must not become knowledge.
		{[]string{"sh", "-c", "cd calculator && go test ./...; echo EXIT=$?"}, ""},
		// Pipelines and command substitution have exit-code semantics the kernel
		// cannot attribute.
		{[]string{"sh", "-c", "go test ./... | tee test.log"}, ""},
		{[]string{"sh", "-c", "go test $(go list ./...)"}, ""},
		{[]string{"sh", "-c", "go test ./... > /dev/null"}, "test"},
		{[]string{"sh", "-c", "go test ./... 2>&1"}, "test"},
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
	// Shell-wrapped and bare invocations of the same verification command map to
	// one knowledge node: wrappers must not flood the projection.
	wrapped := map[string]any{"command": []any{"sh", "-c", "cd calculator && GOCACHE=$PWD/.gocache go test ./..."}}
	wrappedProposal := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: wrapped}, ToolResult{ExitCode: exitCode(0)})
	if wrappedProposal == nil {
		t.Fatal("shell-wrapped successful verification must produce a proposal")
	}
	if wrappedProposal.Command != "go test ./..." {
		t.Fatalf("canonical command = %q, want %q", wrappedProposal.Command, "go test ./...")
	}
	if wrappedProposal.KnowledgeID != stable.KnowledgeID {
		t.Fatal("shell-wrapped and bare invocations must share one knowledge id")
	}
	// Exit-code masking wrappers must not produce knowledge even on exit 0.
	masked := map[string]any{"command": []any{"sh", "-c", "cd calculator && go test ./...; echo EXIT=$?"}}
	if proposal := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: masked}, ToolResult{ExitCode: exitCode(0)}); proposal != nil {
		t.Fatal("exit-code masking wrapper must not produce a proposal")
	}
	// Output-path variants of one build command share a knowledge node: where
	// the binary is written is incidental to the observed fact.
	scratch := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: map[string]any{"command": []any{"go", "build", "-o", "/scratch/forge", "."}}}, ToolResult{ExitCode: exitCode(0)})
	if scratch == nil || scratch.Command != "go build -o OUT ." {
		t.Fatalf("canonical build command = %v", scratch)
	}
	for _, variant := range []map[string]any{
		{"command": []any{"go", "build", "-o", "./forge", "."}},
		{"command": []any{"go", "build", "-o", "/tmp/forge", "."}},
		{"command": []any{"go", "build", "-o=/tmp/forge", "."}},
		{"command": []any{"sh", "-c", "cd repo && go build -o /workspace/forge ."}},
	} {
		if proposal := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: variant}, ToolResult{ExitCode: exitCode(0)}); proposal == nil || proposal.KnowledgeID != scratch.KnowledgeID {
			t.Fatalf("output-path variant %v must share one knowledge id (got %v)", variant, proposal)
		}
	}
	// A different output flag spelling stays a distinct observation: only the
	// flag VALUE is canonicalized, not the flag name.
	if longFlag := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: map[string]any{"command": []any{"go", "build", "--output", "/workspace/forge", "."}}}, ToolResult{ExitCode: exitCode(0)}); longFlag == nil || longFlag.Command != "go build --output OUT ." || longFlag.KnowledgeID == scratch.KnowledgeID {
		t.Fatalf("flag spellings must stay distinct: %v", longFlag)
	}
	// Arguments other than the output path still distinguish observations.
	if tagged := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: map[string]any{"command": []any{"go", "build", "-o", "/tmp/forge", "-tags", "integration", "."}}}, ToolResult{ExitCode: exitCode(0)}); tagged.KnowledgeID == scratch.KnowledgeID {
		t.Fatal("non-output arguments must still distinguish knowledge ids")
	}
	// Commands without an explicit output flag are canonicalized untouched.
	if plain := executionObservationProposal(run, llm.ToolCall{Name: "run_command", Args: map[string]any{"command": []any{"go", "build", "./..."}}}, ToolResult{ExitCode: exitCode(0)}); plain == nil || plain.Command != "go build ./..." {
		t.Fatalf("plain build command = %v", plain)
	}
}
