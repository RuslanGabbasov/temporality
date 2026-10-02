package agent

import (
	"strings"

	"github.com/temporality-project/temporality/workspace"
)

// AgentPromptInput carries everything the deterministic prompt compiler needs.
// It is a plain struct so the kernel does not depend on how the definition is
// stored (docs/evaluable-agent.md §6).
type AgentPromptInput struct {
	Name        string
	Description string // purpose: what this agent is responsible for
	Definition  workspace.AgentDefinition
	Sandbox     string // restricted | standard | privileged | ""
}

// CompileAgentPrompt assembles the system prompt from semantic components:
// base contract (evidence, memory, sandbox) + identity/responsibility +
// constraints + completion criteria + environment policy.
//
// Rules (docs/evaluable-agent.md §7–§10, §17): no step-by-step instructions,
// no tool descriptions (tool schemas already reach the model), no skill
// contents (injected as a separate section), no forced memory usage, minimal
// length, verifiable completion criteria only.
func CompileAgentPrompt(in AgentPromptInput) string {
	caps := in.Definition.Capabilities
	var b strings.Builder

	// Identity + responsibility (role defines responsibility, not style — §12).
	b.WriteString("You are ")
	if in.Name == "" {
		b.WriteString("an autonomous agent")
	} else {
		b.WriteString(in.Name)
	}
	if in.Description != "" {
		b.WriteString(". Responsibility: ")
		b.WriteString(strings.TrimSpace(in.Description))
	}

	// Base evidence contract (§11) — shared by every autonomous agent.
	b.WriteString("\n\nDo not claim a result is achieved unless it is confirmed by available evidence. Distinguish what is done and verified, done but unverified, assumed, and impossible to verify; state which one applies when you report outcomes.")

	// Prior knowledge policy (§10): knowledge is offered, never mandated.
	if caps.Cap(caps.Knowledge) {
		b.WriteString(" Use provided prior knowledge when it is relevant, but prefer current evidence when the two conflict.")
	}

	// Constraints (§5: what the agent must never do).
	for _, c := range in.Definition.Constraints {
		if c = strings.TrimSpace(c); c != "" {
			b.WriteString("\nNever: " + c)
		}
	}

	// Completion criteria (§5: what to verify before declaring done).
	var completion []string
	for _, c := range in.Definition.Completion {
		if c = strings.TrimSpace(c); c != "" {
			completion = append(completion, c)
		}
	}
	if len(completion) > 0 {
		b.WriteString("\nBefore declaring the work done, verify:")
		for _, c := range completion {
			b.WriteString("\n- " + c)
		}
	}

	// Environment policy: behavior-level statements only; tool mechanics stay
	// in tool schemas (§8).
	b.WriteString("\n\nEnvironment: use the available tools to inspect and modify the workspace when needed")
	if !caps.Cap(caps.ModifyFiles) {
		b.WriteString(". This run is read-only: analyze and report, do not attempt to change anything")
	}
	if !caps.Cap(caps.RunCommands) {
		b.WriteString(". Command execution is not available to you")
	}
	if !caps.Cap(caps.Network) {
		b.WriteString(". Network access is disabled")
	}
	b.WriteString(". Choose your own trajectory; converge to a final answer before the turn budget runs out.")

	// Sandbox note: caches go to /scratch, never the workspace.
	if caps.Cap(caps.RunCommands) {
		b.WriteString(" Every command sandbox has a writable /scratch directory (HOME and TMPDIR point there); use it for build and package caches, and never write caches into the workspace, which may be read-only for your role.")
	}

	// Memory recording policy (kept from the base contract; recording is
	// distinct from being forced to consult memory).
	b.WriteString(" Successful verification and build command outcomes are recorded as knowledge automatically; do not restate them with remember. Call remember only when you are highly confident in a durable conclusion that goes beyond the recorded execution results, and include evidence refs.")

	return b.String()
}

// EffectiveSystemPrompt returns the prompt for a run: the manual override when
// present, then the stored manual prompt when the definition carries no
// semantics (legacy agent), then the compiled definition. Agents with neither
// fall back to the role prompt (empty result lets the workflow apply
// systemPromptForRole).
func EffectiveSystemPrompt(in AgentPromptInput, role, manualPrompt string) string {
	if in.Definition.PromptOverride != "" {
		return in.Definition.PromptOverride
	}
	if definitionEmpty(in.Definition) {
		if manualPrompt != "" {
			return manualPrompt
		}
		return ""
	}
	return CompileAgentPrompt(in)
}

func definitionEmpty(d workspace.AgentDefinition) bool {
	return d.Constraints == nil && d.Completion == nil && d.PromptOverride == "" &&
		d.Capabilities == (workspace.AgentCapabilities{})
}
