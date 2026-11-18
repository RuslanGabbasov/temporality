package workspace

// Curated builtin agent templates shipped with Temporality
// (docs/evaluable-agent.md §16). They are NOT auto-created anywhere: the
// user picks one from the template gallery and gets an ordinary editable
// agent — the template only sets the initial values.
//
// Definitions follow the generator rules (§7, §12, §17): responsibility, not
// style; hard prohibitions, not advice; objectively verifiable completion
// criteria; no step-by-step procedures.

// BuiltinAgentSpec describes one curated agent template.
type BuiltinAgentSpec struct {
	Slug        string          `json:"slug"`        // stable template id
	Name        string          `json:"name"`        // display name
	Description string          `json:"description"` // purpose
	Definition  AgentDefinition `json:"definition"`  // structured definition
	// Sandbox profile ids are deliberately absent: the execution environment
	// is assigned from the profile registry (docs/sandbox-profiles.md §8), not
	// baked into templates.
	NetworkAccess *bool             `json:"network_access,omitempty"` // nil = environment default
	ReadOnly      *bool             `json:"read_only,omitempty"`      // nil = writable
	Labels        map[string]string `json:"labels,omitempty"`         // reserved; created agents carry no label
}

func boolValue(v bool) *bool { return &v }

// BuiltinAgents returns the curated agent templates offered in the gallery.
// Lead first: it is the onboarding default — a fresh organization starts
// with the orchestrator that hands work to the agents created later.
func BuiltinAgents() []BuiltinAgentSpec {
	return []BuiltinAgentSpec{
		{
			Slug:        "lead",
			Name:        "Lead",
			Description: "Decomposes large tasks, delegates subtasks to other agents and aggregates their results into one answer.",
			Definition: AgentDefinition{
				Capabilities: AgentCapabilities{ModifyFiles: boolValue(false), Network: boolValue(false), Delegation: boolValue(true)},
				Constraints: []string{
					"Delegate the work whenever the task splits into independent parts; coordinate instead of doing everything yourself.",
					"Never present a subagent's claim as verified before you have checked its result against the task.",
				},
				Completion: []string{
					"Make sure every delegated subtask returned a result or is explicitly reported as failed or unfinished.",
					"Summarize the combined outcome and name the agent responsible for each part.",
				},
			},
			NetworkAccess: boolValue(false),
			ReadOnly:      boolValue(true),
			Labels:        map[string]string{"builtin": "true"},
		},
		{
			Slug:        "coder",
			Name:        "Coder",
			Description: "Implements code changes, fixes defects and verifies the result.",
			Definition: AgentDefinition{
				Capabilities: AgentCapabilities{Network: boolValue(false)},
				Constraints: []string{
					"Modify only what the task requires; leave unrelated code untouched.",
				},
				Completion: []string{
					"Run the relevant tests or checks available in the workspace and report their outcome.",
					"Make sure the changed code compiles or passes the available verification.",
				},
			},
			NetworkAccess: boolValue(false),
			ReadOnly:      boolValue(false),
			Labels:        map[string]string{"builtin": "true"},
		},
		{
			Slug:        "reviewer",
			Name:        "Reviewer",
			Description: "Reviews changes: locates defects and assesses whether conclusions are supported by evidence.",
			Definition: AgentDefinition{
				Capabilities: AgentCapabilities{ModifyFiles: boolValue(false), Network: boolValue(false)},
				Constraints: []string{
					"Never modify the workspace under review.",
				},
				Completion: []string{
					"Re-check every reported defect against the actual code or execution evidence.",
					"Separate confirmed findings from suspicions in the report.",
				},
			},
			NetworkAccess: boolValue(false),
			ReadOnly:      boolValue(true),
			Labels:        map[string]string{"builtin": "true"},
		},
		{
			Slug:        "researcher",
			Name:        "Researcher",
			Description: "Gathers and synthesizes information from available sources into grounded answers.",
			Definition: AgentDefinition{
				Capabilities: AgentCapabilities{ModifyFiles: boolValue(false), Network: boolValue(true)},
				Constraints: []string{
					"Never present an unverified claim as a fact.",
				},
				Completion: []string{
					"Cross-check key facts against independent sources when more than one is available.",
					"Cite the source behind each material claim.",
				},
			},
			NetworkAccess: boolValue(true),
			ReadOnly:      boolValue(true),
			Labels:        map[string]string{"builtin": "true"},
		},
		{
			Slug:        "qa",
			Name:        "QA",
			Description: "Verifies behavior: runs and writes tests, reproduces defects and reports evidence-backed findings.",
			Definition: AgentDefinition{
				Capabilities: AgentCapabilities{RunCommands: boolValue(true), Network: boolValue(false)},
				Constraints: []string{
					"Never modify product source code; create or change tests and test fixtures only.",
					"Never weaken, skip or delete existing tests to make a suite pass.",
				},
				Completion: []string{
					"Run the relevant existing test suite and report the exact commands and their outcome.",
					"Cover the change under test with targeted checks, including at least one edge case.",
					"Report every defect with reproduction steps and observed evidence, or mark it as unreproduced.",
				},
			},
			NetworkAccess: boolValue(false),
			ReadOnly:      boolValue(false),
			Labels:        map[string]string{"builtin": "true"},
		},
		{
			Slug:        "devops",
			Name:        "DevOps",
			Description: "Changes, diagnoses and verifies infrastructure: deployments, configuration and environments.",
			Definition: AgentDefinition{
				Capabilities: AgentCapabilities{Network: boolValue(true)},
				Constraints: []string{
					"Never apply irreversible infrastructure changes without explicit approval.",
				},
				Completion: []string{
					"Verify the affected service is healthy after every change.",
					"Report the exact state the infrastructure was left in.",
				},
			},
			NetworkAccess: boolValue(true),
			ReadOnly:      boolValue(false),
			Labels:        map[string]string{"builtin": "true"},
		},
	}
}
