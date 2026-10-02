package workspace

import (
	"encoding/json"
	"time"
)

// Project groups tasks and runs. Agents are top-level and reusable across projects.
type Project struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	DefaultAgentID string    `json:"default_agent_id,omitempty"`
	DefaultModel   string    `json:"default_model,omitempty"`
	Archived       bool      `json:"archived,omitempty"`
	AllowedUsers   []string  `json:"allowed_users,omitempty"` // "*" = all, empty = admin only, ["user-1"] = specific users
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
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
	AgentVersion int       `json:"agent_version,omitempty"`
	RunID        string    `json:"run_id"`
	Status       string    `json:"status"`
	Model        string    `json:"model,omitempty"`
	Answer       string    `json:"answer,omitempty"`
	Turns        int       `json:"turns"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// User is an operator who can log in and use the platform.
type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email,omitempty"`
	Role      string    `json:"role"`               // viewer, operator, admin
	Token     string    `json:"token,omitempty"`    // bearer token (write-only, never returned in list)
	Projects  []string  `json:"projects,omitempty"` // empty = all
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateProjectRequest is the payload for creating a project.
type CreateProjectRequest struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	DefaultAgentID string   `json:"default_agent_id"`
	DefaultModel   string   `json:"default_model"`
	AllowedUsers   []string `json:"allowed_users,omitempty"` // "*" = all, empty = admin only
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
}

// CreateUserRequest is the payload for creating a user.
type CreateUserRequest struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Email    string   `json:"email,omitempty"`
	Role     string   `json:"role"`
	Token    string   `json:"token,omitempty"` // auto-generated if empty
	Projects []string `json:"projects,omitempty"`
	Active   *bool    `json:"active,omitempty"`
}
