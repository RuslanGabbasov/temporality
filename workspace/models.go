package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Project groups tasks and runs. Agents are top-level and reusable across projects.
type Project struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	DefaultAgentID string   `json:"default_agent_id,omitempty"`
	DefaultModel   string   `json:"default_model,omitempty"`
	Archived       bool     `json:"archived,omitempty"`
	AllowedUsers   []string `json:"allowed_users,omitempty"` // "*" = all, empty = admin only, ["user-1"] = specific users
	// Org bindings (docs/org-structure.md §3.4): a project spans several org
	// units; it is visible where the project's units intersect the viewer's
	// ancestor chain. Empty = org-neutral, visible everywhere (transition).
	OrgUnitIDs []string  `json:"org_units,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ProjectMember is one explicit membership row (docs/org-structure.md §16):
// the user sees the project even when their org chain does not intersect the
// project's units. The role scopes what the member may do inside the project.
type ProjectMember struct {
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

// OrgUnitRole is a role granted on an org unit (docs/org-structure.md §13):
// it acts on the unit and its whole subtree, upgrading the grantee's
// installation role inside that scope.
type OrgUnitRole struct {
	UserID    string    `json:"user_id"`
	OrgUnitID string    `json:"org_unit_id"`
	Role      string    `json:"role"`
	GrantedBy string    `json:"granted_by,omitempty"`
	GrantedAt time.Time `json:"granted_at"`
}

// ExecutionIdentity is the security context of automated runs
// (docs/org-structure.md §20): which agents, MCP servers, providers,
// projects and human request targets a trigger-driven run may touch. A
// "*" entry means "anything visible in the identity's org scope".
type ExecutionIdentity struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	OrgUnitID        string    `json:"org_unit_id,omitempty"`
	AllowedAgents    []string  `json:"allowed_agents"`
	AllowedMCP       []string  `json:"allowed_mcp"`
	AllowedProviders []string  `json:"allowed_providers"`
	AllowedProjects  []string  `json:"allowed_projects"`
	HumanTargets     []string  `json:"human_targets"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// OrgUnitKind values for OrgUnit.Kind.
const (
	OrgKindOrganization = "organization"
	OrgKindDepartment   = "department"
	OrgKindTeam         = "team"
)

// ValidOrgKinds lists the allowed org unit kinds.
var ValidOrgKinds = map[string]bool{
	OrgKindOrganization: true,
	OrgKindDepartment:   true,
	OrgKindTeam:         true,
}

// OrgUnit is one node of the org-structure tree (docs/org-structure.md §3.1).\n// ParentID empty marks a root (one per company). Path is the materialized
// ancestor-id path ("root.dept" for a team under dept); the unit itself is NOT
// part of its own path. Resources bound to a unit are visible to the unit and
// its whole subtree.
type OrgUnit struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id,omitempty"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Path      string    `json:"path,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SelfPath returns the unit's own materialized path prefix: the path its
// descendants carry. For a root it is just the id.
func (u OrgUnit) SelfPath() string { return joinPath(u.Path, u.ID) }

// Ancestors returns the unit's ancestor chain including itself, oldest first.
// For a root it is [root].
func (u OrgUnit) Ancestors() []string {
	chain := make([]string, 0, 4)
	if u.Path != "" {
		chain = append(chain, strings.Split(u.Path, ".")...)
	}
	return append(chain, u.ID)
}

// joinPath appends id to an ancestor path.
func joinPath(path, id string) string {
	if path == "" {
		return id
	}
	return path + "." + id
}

// AgentCapabilities is the user-facing toggle set from the agent form
// (docs/evaluable-agent.md §3.2). Enforcement is hard: a disabled capability
// removes the corresponding tools and environment access at run start — it is
// never just a line in the prompt.
type AgentCapabilities struct {
	ReadFiles   *bool `json:"read_files,omitempty"`   // read_file / search / grep
	ModifyFiles *bool `json:"modify_files,omitempty"` // write tools; false ⇒ read-only run
	RunCommands *bool `json:"run_commands,omitempty"` // run_command in sandbox
	Network     *bool `json:"network,omitempty"`      // sandbox network access
	Skills      *bool `json:"skills,omitempty"`       // skill injection for this run
	Knowledge   *bool `json:"knowledge,omitempty"`    // prior knowledge hints
	// Delegation lets the agent hand subtasks to other agents via the delegate
	// tool (docs/agent-delegation.md). Inverted default: nil or false ⇒ denied,
	// because delegation must be granted explicitly by a human.
	Delegation *bool `json:"delegation,omitempty"`
}

// Cap returns the effective value of a capability: nil (unset) means allowed,
// matching pre-definition agent behavior.
func (c AgentCapabilities) Cap(enabled *bool) bool { return enabled == nil || *enabled }

// AgentDefinition is the structured, human-edited part of an agent
// (docs/evaluable-agent.md). The system prompt is compiled from it; users never
// see YAML. Description (the agent's purpose) lives on Agent.Description.
type AgentDefinition struct {
	Capabilities   AgentCapabilities `json:"capabilities,omitempty"`
	Constraints    []string          `json:"constraints,omitempty"`     // what the agent must never do
	Completion     []string          `json:"completion,omitempty"`      // what to verify before declaring done
	PromptOverride string            `json:"prompt_override,omitempty"` // optional manual replacement of the compiled prompt
}

// AgentVersion is an immutable snapshot of the agent definition: what changed,
// who changed it, and the exact prompt compiled from it (docs/evaluable-agent.md §14).
type AgentVersion struct {
	AgentID        string          `json:"agent_id"`
	Version        int             `json:"version"`
	Definition     AgentDefinition `json:"definition"`
	Description    string          `json:"description"` // purpose snapshot
	CompiledPrompt string          `json:"compiled_prompt"`
	PromptSource   string          `json:"prompt_source"` // manual | ai | builtin
	GeneratorModel string          `json:"generator_model,omitempty"`
	Author         string          `json:"author,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// Agent is a top-level entity with full configuration. Agents are reusable
// across projects and tasks — they define HOW the agent behaves.
type Agent struct {
	ID string `json:"id"`
	// Identity
	Name        string `json:"name"`
	Description string `json:"description,omitempty"` // purpose: what this agent is for
	// Scope (optional — agents can be global or project-scoped)
	ProjectID string `json:"project_id,omitempty"`
	// Model configuration
	Model        string   `json:"model"`
	Provider     string   `json:"provider,omitempty"` // provider ID (see Provider)
	SystemPrompt string   `json:"system_prompt"`      // legacy/manual prompt; superseded by Definition
	Temperature  *float64 `json:"temperature,omitempty"`
	MaxTokens    *int     `json:"max_tokens,omitempty"`
	// Tools & capabilities
	Skills     []string `json:"skills,omitempty"`
	MCPServers []string `json:"mcp_servers,omitempty"`
	Tools      []string `json:"tools,omitempty"` // model tool names; empty = all available tools
	// Structured definition (docs/evaluable-agent.md); nil ⇒ legacy prompt-only agent
	Definition        *AgentDefinition `json:"definition,omitempty"`
	DefinitionVersion int              `json:"definition_version,omitempty"`
	// Sandbox constraints
	SandboxProfile string `json:"sandbox_profile,omitempty"` // restricted, standard, privileged
	NetworkAccess  *bool  `json:"network_access,omitempty"`
	ReadOnly       *bool  `json:"read_only,omitempty"`
	// Execution limits
	MaxTurns     *int    `json:"max_turns,omitempty"`
	ApprovalMode *string `json:"approval_mode,omitempty"` // auto, prompt, manual
	// Metadata
	Labels map[string]string `json:"labels,omitempty"`
	// Org binding (docs/org-structure.md §3.2): empty = whole installation.
	OrgUnitID string    `json:"org_unit_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Task is scoped to a project and optionally bound to an agent.
type Task struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	AgentID   string    `json:"agent_id,omitempty"`
	Title     string    `json:"title"`
	Prompt    string    `json:"prompt"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Run is one execution of a task.
type Run struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id,omitempty"`
	// AgentVersion pins the agent definition snapshot this run used
	// (docs/evaluable-agent.md §15); 0 = legacy run without a definition.
	AgentVersion int `json:"agent_version,omitempty"`
	// ExecContext is the authorization snapshot at start time
	// (docs/org-structure.md §34-35): which agent, skills, MCP servers,
	// model/project and identity the run was authorized to use, and by whom.
	ExecContext map[string]any `json:"exec_context,omitempty"`
	RunID       string         `json:"run_id"`
	Status      string         `json:"status"`
	Model       string         `json:"model,omitempty"`
	Answer      string         `json:"answer,omitempty"`
	Turns       int            `json:"turns"`
	Error       string         `json:"error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// Trigger is a configurable mechanism to launch agent runs.
type Trigger struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"project_id"`
	AgentID   string          `json:"agent_id,omitempty"`
	Name      string          `json:"name"`
	Type      string          `json:"type"` // schedule, webhook, event
	Enabled   bool            `json:"enabled"`
	Config    json.RawMessage `json:"config"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// ScheduleConfig is the config JSON for schedule triggers.
type ScheduleConfig struct {
	Cron     string `json:"cron"`
	Prompt   string `json:"prompt"`
	Timezone string `json:"timezone,omitempty"`
}

// WebhookConfig is the config JSON for webhook triggers.
type WebhookConfig struct {
	Path           string `json:"path"`
	Secret         string `json:"secret,omitempty"`
	PromptTemplate string `json:"prompt_template"`
}

// EventConfig is the config JSON for event triggers.
type EventConfig struct {
	EventType string            `json:"event_type"`
	Filter    map[string]string `json:"filter,omitempty"`
	Prompt    string            `json:"prompt"`
}

// Provider is an OpenAI-compatible model endpoint (e.g. z.ai, xiaomi).
type Provider struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	BaseURL   string            `json:"base_url"`
	APIKeyRef string            `json:"api_key_ref,omitempty"` // env var name or secret ref
	Models    []string          `json:"models,omitempty"`      // model IDs discovered or listed
	Labels    map[string]string `json:"labels,omitempty"`
	// Org binding (docs/org-structure.md §3.2): empty = whole installation.
	OrgUnitID string    `json:"org_unit_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UserChannel is one delivery transport owned by the user profile
// (docs/triggers-and-escalations.md §6). The agent never picks transports;
// the kernel resolves the recipient and their preferred channel.
type UserChannel struct {
	Type    string `json:"type"`    // matrix, telegram
	Address string `json:"address"` // matrix room id, telegram chat id
	Enabled bool   `json:"enabled"`
}

// User is an operator who can log in and use the platform.
type User struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Email    string   `json:"email,omitempty"`
	Role     string   `json:"role"`                // viewer, operator, admin
	Token    string   `json:"token,omitempty"`     // bearer token; only set in create/regenerate responses, never in list
	HasToken bool     `json:"has_token,omitempty"` // list responses: whether a token exists (value is never exposed)
	Projects []string `json:"projects,omitempty"`  // empty = all
	// Org position (docs/org-structure.md §3.3): the single unit the user
	// belongs to; empty = unassigned, sees everything (transition semantics).
	OrgUnitID string `json:"org_unit_id,omitempty"`
	Active    bool   `json:"active"`
	// Communication channels + preferred delivery. Preferred "" or "web"
	// means the UI inbox; otherwise it must reference an enabled channel.
	Channels         []UserChannel `json:"channels,omitempty"`
	PreferredChannel string        `json:"preferred_channel,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

// ValidChannelTypes lists the transports the kernel knows how to deliver to.
var ValidChannelTypes = map[string]bool{
	"matrix":   true,
	"telegram": true,
}

// ValidateChannels normalizes a channel list: known types only, non-empty
// addresses, at most one entry per type. It reports the first problem found.
func ValidateChannels(channels []UserChannel) error {
	seen := make(map[string]bool, len(channels))
	for _, channel := range channels {
		if !ValidChannelTypes[channel.Type] {
			return fmt.Errorf("unknown channel type %q (supported: matrix, telegram)", channel.Type)
		}
		if strings.TrimSpace(channel.Address) == "" {
			return fmt.Errorf("channel %q needs an address", channel.Type)
		}
		if seen[channel.Type] {
			return fmt.Errorf("channel %q is configured twice", channel.Type)
		}
		seen[channel.Type] = true
	}
	return nil
}

// CreateProjectRequest is the payload for creating a project.
type CreateProjectRequest struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	DefaultAgentID string   `json:"default_agent_id"`
	DefaultModel   string   `json:"default_model"`
	AllowedUsers   []string `json:"allowed_users,omitempty"` // "*" = all, empty = admin only
	OrgUnits       []string `json:"org_units,omitempty"`     // org unit ids the project spans
}

// CreateOrgUnitRequest is the payload for creating or updating an org unit.
type CreateOrgUnitRequest struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"` // empty = new root
	Kind     string `json:"kind"`                // organization | department | team
	Name     string `json:"name"`
}

// CreateAgentRequest is the payload for creating an agent.
type CreateAgentRequest struct {
	ID             string            `json:"id"`
	ProjectID      string            `json:"project_id,omitempty"`
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	Model          string            `json:"model"`
	Provider       string            `json:"provider,omitempty"`
	SystemPrompt   string            `json:"system_prompt"`
	Temperature    *float64          `json:"temperature,omitempty"`
	MaxTokens      *int              `json:"max_tokens,omitempty"`
	Skills         []string          `json:"skills,omitempty"`
	MCPServers     []string          `json:"mcp_servers,omitempty"`
	Tools          []string          `json:"tools,omitempty"`
	SandboxProfile string            `json:"sandbox_profile,omitempty"`
	NetworkAccess  *bool             `json:"network_access,omitempty"`
	ReadOnly       *bool             `json:"read_only,omitempty"`
	MaxTurns       *int              `json:"max_turns,omitempty"`
	ApprovalMode   *string           `json:"approval_mode,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	OrgUnitID      string            `json:"org_unit_id,omitempty"`
	// Structured definition (docs/evaluable-agent.md). PromptSource marks the
	// origin of the definition for version history: manual | ai | builtin;
	// GeneratorModel records the model behind AI drafts.
	Definition     *AgentDefinition `json:"definition,omitempty"`
	PromptSource   string           `json:"prompt_source,omitempty"`
	GeneratorModel string           `json:"generator_model,omitempty"`
}

// CreateTaskRequest is the payload for creating a task.
type CreateTaskRequest struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id"`
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`
}

// StartRunRequest starts a new run for a task.
type StartRunRequest struct {
	AgentID string `json:"agent_id,omitempty"`
	Model   string `json:"model,omitempty"`
}

// CreateProviderRequest is the payload for creating a model provider.
type CreateProviderRequest struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	BaseURL   string            `json:"base_url"`
	APIKeyRef string            `json:"api_key_ref,omitempty"`
	Models    []string          `json:"models,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	OrgUnitID string            `json:"org_unit_id,omitempty"`
}

// CreateUserRequest is the payload for creating a user.
type CreateUserRequest struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Email            string        `json:"email,omitempty"`
	Role             string        `json:"role"`
	Token            string        `json:"token,omitempty"` // auto-generated if empty
	Projects         []string      `json:"projects,omitempty"`
	OrgUnitID        *string       `json:"org_unit_id,omitempty"` // nil on update = keep; "" = unassign
	Active           *bool         `json:"active,omitempty"`
	Channels         []UserChannel `json:"channels,omitempty"`
	PreferredChannel string        `json:"preferred_channel,omitempty"`
}
