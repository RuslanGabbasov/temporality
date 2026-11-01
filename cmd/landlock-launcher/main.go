// Command landlock-launcher wraps one sandbox command in the run's frozen
// filesystem policy (docs/filesystem-policy.md §5.3): it compiles the policy
// to Landlock path-beneath rules, restricts its own process, then exec's the
// command — every child inherits the restriction. Fail-closed by contract:
// when Landlock cannot be applied the launcher refuses to run the command
// (exit 125) instead of executing it unrestricted; the operator's explicit
// KERNEL_SANDBOX_LANDLOCK=off lives kernel-side and skips the wrapper
// entirely. The policy rides the environment (TEMPORALITY_FS_POLICY, the
// workspace.EffectiveFilesystem JSON) — one policy, one semantics for the
// kernel gateway and the OS layer (§5.4).
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/temporality-project/temporality/kernel/sandbox/landlock"
	"github.com/temporality-project/temporality/workspace"
)

const (
	envPolicy        = "TEMPORALITY_FS_POLICY"
	envPolicyVersion = "TEMPORALITY_FS_POLICY_VERSION"
	envWorkspaceRoot = "TEMPORALITY_FS_ROOT"
)

// technicalFull are the non-project trees that need full access for the
// sandbox to function (§5.3 «права на технические каталоги»): device nodes,
// proc/sys, runtime state, and the run-scoped scratch/tmp. They are outside
// the project policy by design — project storage is the workspace tree.
var technicalFull = []string{"/dev", "/proc", "/sys", "/run", "/tmp", "/scratch", "/var"}

// technicalRead are system trees the toolchains need to read (execve reads
// the binaries themselves — a read denial there blocks every tool). Writes
// are excluded: the sandbox root is read-only anyway, and policy must not
// depend on that accident.
var technicalRead = []string{"/bin", "/sbin", "/usr", "/lib", "/lib64", "/etc", "/opt"}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fail("no command to run (usage: landlock-launcher -- <command> [args...])")
	}
	raw := os.Getenv(envPolicy)
	if raw == "" {
		fail("refusing to run without enforcement: " + envPolicy + " is empty")
	}
	var policy workspace.EffectiveFilesystem
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		fail("refusing to run without enforcement: cannot parse " + envPolicy + ": " + err.Error())
	}
	root := os.Getenv(envWorkspaceRoot)
	if root == "" {
		root = "/workspace"
	}
	rules := technicalRules()
	compiled := landlock.Compile(root, &policy)
	rules = append(rules, compiled.Rules...)
	for _, note := range compiled.Notes {
		fmt.Fprintln(os.Stderr, "landlock-launcher: policy approximation:", note)
	}
	if err := applyAndExec(rules, args); err != nil {
		fail("refusing to run without filesystem enforcement: " + err.Error())
	}
}

// technicalRules grants the non-project trees: full where tools write
// temporaries and runtime state, read where the toolchain lives.
func technicalRules() []landlock.Rule {
	rules := make([]landlock.Rule, 0, len(technicalFull)+len(technicalRead))
	for _, path := range technicalFull {
		if _, err := os.Stat(path); err == nil {
			rules = append(rules, landlock.Rule{Path: path, Access: landlock.FullOps})
		}
	}
	for _, path := range technicalRead {
		if _, err := os.Stat(path); err == nil {
			rules = append(rules, landlock.Rule{Path: path, Access: landlock.ReadOps})
		}
	}
	return rules
}

// fail is the fail-closed exit: distinct code 125 so the kernel surfaces
// "the wrapper refused" instead of mistaking it for a command failure.
func fail(message string) {
	fmt.Fprintln(os.Stderr, "landlock-launcher:", message)
	os.Exit(125)
}
