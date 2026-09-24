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
	class, _ := verificationTarget(command)
	return class
}

// verificationTarget resolves a sandbox command to its knowledge class and the
// canonical command string that identifies the observation. Bare argv commands
// are classified directly. Shell-wrapped commands (`sh -c "cd dir && go test
// ./..."`) are attributed to the final command of the script, because only that
// command determines the recorded exit code. The canonical form strips the
// wrapper so equivalent invocations across runs map to one knowledge node
// instead of flooding the projection with near-duplicates.
func verificationTarget(command []string) (string, string) {
	if class := argvVerificationClass(command); class != "" {
		return class, strings.Join(command, " ")
	}
	script, ok := shellScript(command)
	if !ok {
		return "", ""
	}
	tokens, ok := scriptTailCommand(script)
	if !ok {
		return "", ""
	}
	class := argvVerificationClass(tokens)
	if class == "" {
		return "", ""
	}
	return class, strings.Join(tokens, " ")
}

// argvVerificationClass is the bare-token classifier.
func argvVerificationClass(command []string) string {
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

// shellScript returns the script body when the command invokes a shell with
// `-c`, e.g. `sh -c "go test ./..."` or `bash -eu -c "..."`.
func shellScript(command []string) (string, bool) {
	if len(command) < 3 {
		return "", false
	}
	switch command[0] {
	case "sh", "bash", "zsh", "dash", "ash":
	default:
		return "", false
	}
	for _, token := range command[1 : len(command)-1] {
		if token == "-c" {
			return command[len(command)-1], true
		}
	}
	return "", false
}

// scriptTailCommand extracts the tokens of the final command of a shell script.
// The exit code recorded by run_command belongs to that command alone, so any
// earlier verification in the script is intentionally not attributed (e.g.
// `go test ./...; echo EXIT=$?` credits echo, not go test). Extraction is
// deliberately conservative: pipelines, command substitution, and background
// jobs have exit-code semantics the kernel cannot attribute, so they never
// become knowledge. Redirections and leading environment assignments do not
// affect the exit code and are stripped.
func scriptTailCommand(script string) ([]string, bool) {
	segment := lastSplit(script, ";", "\n")
	tail := lastSplit(segment, "&&", "||")
	if tail == "" {
		return nil, false
	}
	if strings.ContainsAny(tail, "|`") || strings.Contains(tail, "$(") {
		return nil, false
	}
	tokens := strings.Fields(tail)
	for len(tokens) > 0 && isEnvAssignment(tokens[0]) {
		tokens = tokens[1:]
	}
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if isRedirection(token) {
			continue
		}
		if strings.ContainsAny(token, "&") {
			return nil, false
		}
		kept = append(kept, token)
	}
	return kept, true
}

// lastSplit returns the last non-empty chunk of s after splitting on any of
// the separators.
func lastSplit(s string, separators ...string) string {
	chunks := []string{s}
	for _, separator := range separators {
		split := make([]string, 0, len(chunks))
		for _, chunk := range chunks {
			split = append(split, strings.Split(chunk, separator)...)
		}
		chunks = split
	}
	for i := len(chunks) - 1; i >= 0; i-- {
		if strings.TrimSpace(chunks[i]) != "" {
			return strings.TrimSpace(chunks[i])
		}
	}
	return ""
}

func isEnvAssignment(token string) bool {
	name, _, ok := strings.Cut(token, "=")
	return ok && name != "" && !strings.ContainsAny(name, "/.$-")
}

func isRedirection(token string) bool {
	if strings.HasPrefix(token, ">") || strings.HasPrefix(token, "<") {
		return true
	}
	i := strings.IndexAny(token, "><")
	if i <= 0 {
		return false
	}
	digits := token[:i]
	return strings.Trim(digits, "0123456789") == ""
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
	class, canonical := verificationTarget(command)
	if class == "" {
		return nil
	}
	proposition := fmt.Sprintf("%s command `%s` exited 0", map[string]string{"test": "Verification", "build": "Build"}[class], canonical)
	if run.WorkspacePath != "" {
		proposition += " in workspace " + run.WorkspacePath
	}
	return &executionObservation{
		KnowledgeID: executionKnowledgeID(run.Project, run.WorkspacePath, canonical),
		Proposition: proposition,
		Class:       class,
		Command:     canonical,
		Policy:      ExecutionObservationPolicy,
	}
}
