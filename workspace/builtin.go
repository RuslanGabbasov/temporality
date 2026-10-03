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
	Slug           string            `json:"slug"`                     // stable template id
	Name           string            `json:"name"`                     // display name
	Description    string            `json:"description"`              // purpose
	Definition     AgentDefinition   `json:"definition"`               // structured definition
	SandboxProfile string            `json:"sandbox_profile"`          // restricted | standard | privileged
	NetworkAccess  *bool             `json:"network_access,omitempty"` // nil = environment default
	ReadOnly       *bool             `json:"read_only,omitempty"`      // nil = writable
	Labels         map[string]string `json:"labels,omitempty"`         // reserved; created agents carry no label
}

func boolValue(v bool) *bool { return &v }

// BuiltinAgents returns the curated agent templates offered in the gallery.
func BuiltinAgents() []BuiltinAgentSpec {
	return []BuiltinAgentSpec{
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
			SandboxProfile: "standard",
			NetworkAccess:  boolValue(false),
			ReadOnly:       boolValue(false),
			Labels:         map[string]string{"builtin": "true"},
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
			SandboxProfile: "standard",
			NetworkAccess:  boolValue(false),
			ReadOnly:       boolValue(true),
			Labels:         map[string]string{"builtin": "true"},
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
			SandboxProfile: "standard",
			NetworkAccess:  boolValue(true),
			ReadOnly:       boolValue(true),
			Labels:         map[string]string{"builtin": "true"},
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
			SandboxProfile: "privileged",
			NetworkAccess:  boolValue(true),
			ReadOnly:       boolValue(false),
			Labels:         map[string]string{"builtin": "true"},
		},
	}
}
