package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/temporality-project/temporality/aml/llm"
)

// ExecutionObservationPolicy identifies the deterministic kernel heuristic that
// turns successful verification/build command outcomes into knowledge
// proposals. The policy never asserts truth: it records an observed execution
// fact with evidence, leaving confirmation to later verification or review.
const ExecutionObservationPolicy = "kernel-heuristic/execution-observation.v1"

// ReverificationRule marks a knowledge.confirmed event produced by a repeated
// successful execution of a command that already has a proposal.
const ReverificationRule = "execution-reverification.v1"

// ReuseRule marks a knowledge.used event produced by a successful execution of
// a command whose observation is already confirmed. Repeated success then
// grows reuse telemetry instead of duplicating knowledge nodes.
const ReuseRule = "execution-reuse.v1"

// verificationClass classifies a sandbox command as a verification ("test") or
// build ("build") command. Empty string means the outcome is not recorded as
// knowledge by the heuristic. Classification is a pure function of the argv
// tokens so it stays deterministic under workflow replay. A token counts only
// in the program or subcommand position, so `echo test` and `cat build.md`
// stay ordinary commands.
func verificationClass(command []string) string {
	program := ""
	if len(command) > 0 {
		program = command[0]
	}
	subcommand := ""
	if len(command) > 1 {
		subcommand = command[1]
	}
	switch program {
	case "test", "tests", "pytest", "unittest", "tox", "nox":
		return "test"
	case "tsc":
		return "build"
	case "go", "cargo", "npm", "pnpm", "yarn", "make", "mvn", "gradle", "dotnet", "composer", "bundle", "rake":
	default:
		return ""
	}
	for _, token := range command[min(1, len(command)):] {
		switch token {
		case "test", "tests", "vet", "lint", "check", "tsc", "build", "compile":
			if token == "build" || token == "tsc" || token == "compile" {
				return "build"
			}
			return "test"
		}
	}
	if subcommand == "run" && len(command) > 2 {
		switch command[2] {
		case "test", "lint":
			return "test"
		case "build":
			return "build"
		}
	}
	return ""
}

// executionKnowledgeID returns a project-scoped stable identity for a
// verified command outcome. It deliberately excludes the run: repeated
// successful executions across runs must reuse the same knowledge node so the
// stream records reuse instead of flooding the projection with duplicates.
func executionKnowledgeID(project, workspace, command string) string {
	digest := sha256.Sum256([]byte(project + "\x00" + workspace + "\x00" + command))
	return "auto/" + hex.EncodeToString(digest[:12])
}

type executionObservation struct {
	KnowledgeID string
	Proposition string
	Class       string
	Command     string
	Policy      string
}

// executionObservationProposal returns a knowledge proposal for a successful
// verification or build command, or nil when the tool call does not qualify.
// Only sandbox executions with an explicit zero exit code qualify; failures and
// unknown outcomes stay runtime events, and the model remains responsible for
// conclusions that go beyond the recorded execution fact.
func executionObservationProposal(run RunInput, call llm.ToolCall, result ToolResult) *executionObservation {
	if call.Name != "run_command" || result.ExitCode == nil || *result.ExitCode != 0 {
		return nil
	}
	command, err := stringArgs(call.Args["command"])
	if err != nil || len(command) == 0 {
		return nil
	}
	class := verificationClass(command)
	if class == "" {
		return nil
	}
	joined := strings.Join(command, " ")
	proposition := fmt.Sprintf("%s command `%s` exited 0", map[string]string{"test": "Verification", "build": "Build"}[class], joined)
	if run.WorkspacePath != "" {
		proposition += " in workspace " + run.WorkspacePath
	}
	return &executionObservation{
		KnowledgeID: executionKnowledgeID(run.Project, run.WorkspacePath, joined),
		Proposition: proposition,
		Class:       class,
		Command:     joined,
		Policy:      ExecutionObservationPolicy,
	}
}
