package agent

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/workspace"
)

func boolPtr(v bool) *bool { return &v }

func TestCompileAgentPromptFullDefinition(t *testing.T) {
	prompt := CompileAgentPrompt(AgentPromptInput{
		Name:        "Coder",
		Description: "Implements code changes, fixes bugs and verifies the result.",
		Definition: workspace.AgentDefinition{
			Constraints: []string{"Never touch infrastructure configuration.", "Never delete files."},
			Completion:  []string{"Run the relevant tests.", "Make sure the build passes."},
		},
		Sandbox: "standard",
	})

	want := `You are Coder. Responsibility: Implements code changes, fixes bugs and verifies the result.

Do not claim a result is achieved unless it is confirmed by available evidence. Distinguish what is done and verified, done but unverified, assumed, and impossible to verify; state which one applies when you report outcomes. Use provided prior knowledge when it is relevant, but prefer current evidence when the two conflict.
Never: Never touch infrastructure configuration.
Never: Never delete files.
Before declaring the work done, verify:
- Run the relevant tests.
- Make sure the build passes.

Environment: use the available tools to inspect and modify the workspace when needed. Choose your own trajectory; converge to a final answer before the turn budget runs out. Every command sandbox has a writable /scratch directory (HOME and TMPDIR point there); use it for build and package caches, and never write caches into the workspace, which may be read-only for your role. Successful verification and build command outcomes are recorded as knowledge automatically; do not restate them with remember. Call remember only when you are highly confident in a durable conclusion that goes beyond the recorded execution results, and include evidence refs.`
	if prompt != want {
		t.Fatalf("compiled prompt mismatch:\n%q\nwant:\n%q", prompt, want)
	}
}

func TestCompileAgentPromptRestrictedCapabilities(t *testing.T) {
	prompt := CompileAgentPrompt(AgentPromptInput{
		Name:        "Reviewer",
		Description: "Finds defects and assesses evidence sufficiency.",
		Definition: workspace.AgentDefinition{
			Capabilities: workspace.AgentCapabilities{
				ModifyFiles: boolPtr(false),
				RunCommands: boolPtr(false),
				Network:     boolPtr(false),
			},
		},
	})

	for _, want := range []string{
		"This run is read-only: analyze and report, do not attempt to change anything",
		"Command execution is not available to you",
		"Network access is disabled",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "/scratch") {
		t.Errorf("read-only prompt should not mention command sandbox caches:\n%s", prompt)
	}
}

func TestCompileAgentPromptOmitsToolAndSkillDetails(t *testing.T) {
	prompt := CompileAgentPrompt(AgentPromptInput{
		Name:        "Coder",
		Description: "Writes code.",
		Definition:  workspace.AgentDefinition{Completion: []string{"Run tests."}},
	})
	// Generator rules (§8–§10): the prompt must not describe tool mechanics,
	// skill procedures, mandate memory usage or prescribe steps. Generic
	// guidance like "use the available tools" is explicitly allowed by §8.
	for _, banned := range []string{
		"use the tool", "with the parameter", "call the function", // tool mechanics (§8)
		"skill", "always use", "memory", // skills/memory mandates (§9, §10)
		"first", "then", "after that", "step 1", // step-by-step instructions (§7)
	} {
		if banned != "" && strings.Contains(strings.ToLower(prompt), strings.ToLower(banned)) {
			t.Errorf("prompt should not contain %q (tool/skill mechanics, step-by-step, forced memory):\n%s", banned, prompt)
		}
	}
}

func TestEffectiveSystemPromptFallbacks(t *testing.T) {
	// Empty definition with no manual prompt: the workflow falls back to the
	// role prompt (empty result signals that).
	legacy := EffectiveSystemPrompt(AgentPromptInput{Name: "old"}, "reviewer", "")
	if legacy != "" {
		t.Fatalf("empty definition without manual prompt should defer to role prompt, got:\n%s", legacy)
	}
	// Legacy agent (empty definition) with a stored manual prompt keeps it.
	manual := "Manual legacy prompt."
	if got := EffectiveSystemPrompt(AgentPromptInput{Name: "old"}, "reviewer", manual); got != manual {
		t.Fatalf("legacy manual prompt should be kept, got %q", got)
	}
	override := "Custom override prompt."
	got := EffectiveSystemPrompt(AgentPromptInput{
		Name:        "x",
		Description: "d",
		Definition:  workspace.AgentDefinition{PromptOverride: override},
	}, "", "ignored manual")
	if got != override {
		t.Fatalf("override should win, got %q", got)
	}
}
