// Command temporality is the Living Skills CLI: a thin read-only client over
// the skill registry served by agent-kernel (docs/living-skills.md §43 MVP).
//
// Skill endpoints live on the kernel HTTP API, not the journal: through the
// debugger proxy that is http://localhost:3000/kernel-api, or directly
// http://localhost:8090 (docker-compose). The validate command runs entirely
// locally against the skills package and needs no server.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// defaultBaseURL points at the kernel workspace API through the debugger
// proxy, the base that works in the standard docker-compose topology.
const defaultBaseURL = "http://localhost:3000/kernel-api"

const usage = `temporality — Living Skills CLI (read-only)

Usage:
  temporality skill <command> [flags] [<skill-id> | <path>]

Commands:
  list               list skills in the workspace registry
  inspect <id>       full skill detail: manifest contract and SKILL.md
  validate <path>    validate a local skill (skill.yaml, SKILL.md or directory)
  history <id>       immutable version history of a skill
  executions <id>    executions (runs) linked to a skill
  memory <id>        knowledge linked to a skill

Flags:
  --url <base>       workspace API base URL (env TEMPORALITY_URL,
                     default http://localhost:3000/kernel-api)
  --token <token>    API bearer token (env TEMPORALITY_API_TOKEN)
  --json             machine-readable JSON output
  --project <id>     filter by project (executions, memory)
  --limit <n>        maximum executions to fetch (executions)
  -h, --help         show this help

Skill endpoints live on the agent-kernel API, so --url must point at its
base: http://localhost:3000/kernel-api via the debugger proxy, or
http://localhost:8090 directly. Skills are workspace-global; --project
filters only executions and memory, which record where a skill ran.
validate reuses the shared skills package and works offline.

Examples:
  temporality skill list
  temporality skill inspect deploy-service
  temporality skill validate ./my-skill/skill.yaml
  temporality skill executions test-driven-development --limit 10
  temporality skill memory graphmap-qa-workflow --project lighthouse
`

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run dispatches a command line and returns the process exit code:
// 0 success, 1 runtime failure (HTTP, invalid skill), 2 usage error.
func run(args []string, env func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if args[0] != "skill" {
		fmt.Fprintf(stderr, "temporality: unknown command %q (only \"skill\" exists)\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
	if len(args) == 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	sub, rest := args[1], reorderFlags(args[2:])
	switch sub {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "list":
		return runSkillList(rest, env, stdout, stderr)
	case "inspect":
		return runSkillInspect(rest, env, stdout, stderr)
	case "validate":
		return runSkillValidate(rest, env, stdout, stderr)
	case "history":
		return runSkillHistory(rest, env, stdout, stderr)
	case "executions":
		return runSkillExecutions(rest, env, stdout, stderr)
	case "memory":
		return runSkillMemory(rest, env, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "temporality skill: unknown command %q\n\n", sub)
		fmt.Fprint(stderr, usage)
		return 2
	}
}

// reorderFlags moves flag arguments in front of positional arguments so both
// "skill inspect --json deploy" and "skill inspect deploy --json" parse.
// valueFlags lists the flags that consume a separate value argument; keep in
// sync with the flag sets in skills.go.
func reorderFlags(args []string) []string {
	valueFlags := map[string]bool{
		"--url": true, "-url": true,
		"--token": true, "-token": true,
		"--project": true, "-project": true,
		"--limit": true, "-limit": true,
	}
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg != "-" && strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			// A value flag without "=" consumes the next argument.
			if valueFlags[arg] && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, arg)
	}
	return append(flags, positional...)
}
