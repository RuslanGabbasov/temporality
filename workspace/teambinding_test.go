package workspace

import (
	"testing"
)

func enabled(v bool) *bool { return &v }

// Slot matching (docs/agent-teams.md §7, wave C) follows the roster card
// semantics: unset capability means allowed, an explicit false opts out,
// delegation needs an explicit grant, and legacy agents without a structured
// definition satisfy only requirement-free slots.
func TestAgentSatisfiesCapability(t *testing.T) {
	structured := Agent{ID: "a", Definition: &AgentDefinition{Capabilities: AgentCapabilities{
		ReadFiles:   enabled(true),
		ModifyFiles: enabled(false),
	}}}
	if !structured.SatisfiesCapability("read_files") {
		t.Fatal("explicitly enabled capability must satisfy")
	}
	if structured.SatisfiesCapability("modify_files") {
		t.Fatal("explicitly disabled capability must not satisfy")
	}
	if !structured.SatisfiesCapability("run_commands") {
		t.Fatal("unset capability counts as allowed (roster card semantics)")
	}
	if structured.SatisfiesCapability("delegation") {
		t.Fatal("delegation requires an explicit grant")
	}
	if structured.SatisfiesCapability("write_files") {
		t.Fatal("write_files aliases modify_files, which is disabled here")
	}
	if _, known := NormalizeAgentCapability("write_files"); !known {
		t.Fatal("write_files must be a known alias")
	}
	if _, known := NormalizeAgentCapability("telepathy"); known {
		t.Fatal("unknown capability names must be rejected")
	}

	lead := Agent{ID: "lead", Definition: &AgentDefinition{Capabilities: AgentCapabilities{Delegation: enabled(true)}}}
	if !lead.SatisfiesCapability("delegation") {
		t.Fatal("explicit delegation grant must satisfy")
	}

	legacy := Agent{ID: "legacy"}
	if legacy.SatisfiesCapability("read_files") {
		t.Fatal("legacy agents report no capability profile")
	}
	if !legacy.SatisfiesRequirements(TeamRequirements{}) {
		t.Fatal("requirement-free slots accept any visible agent, legacy included")
	}
	if legacy.SatisfiesRequirements(TeamRequirements{Capabilities: []string{"read_files"}}) {
		t.Fatal("legacy agents must not satisfy capability requirements")
	}

	skilled := Agent{ID: "skilled", Skills: []string{"verify", "review"}, Definition: &AgentDefinition{}}
	if !skilled.SatisfiesRequirements(TeamRequirements{Skills: []string{"verify"}}) {
		t.Fatal("attached skill must satisfy the skill requirement")
	}
	if skilled.SatisfiesRequirements(TeamRequirements{Skills: []string{"deploy"}}) {
		t.Fatal("missing skill must not satisfy the requirement")
	}
}
