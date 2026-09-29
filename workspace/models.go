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
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Agent is a top-level entity with full configuration. Agents are reusable
// across projects and tasks — they define HOW the agent behaves.
type Agent struct {
	ID string `json:"id"`
	// Identity
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Scope (optional — agents can be global or project-scoped)
	ProjectID string `json:"project_id,omitempty"`
	// Model configuration
	Model        string   `json:"model"`
	Provider     string   `json:"provider,omitempty"` // provider ID (see Provider)
	SystemPrompt string   `json:"system_prompt"`
	Temperature  *float64 `json:"temperature,omitempty"`
	MaxTokens    *int     `json:"max_tokens,omitempty"`
	// Tools & capabilities
	Skills     []string `json:"skills,omitempty"`
	MCPServers []string `json:"mcp_servers,omitempty"`
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
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	ProjectID string    `json:"project_id"`
	AgentID   string    `json:"agent_id,omitempty"`
	RunID     string    `json:"run_id"`
	Status    string    `json:"status"`
	Model     string    `json:"model,omitempty"`
	Answer    string    `json:"answer,omitempty"`
	Turns     int       `json:"turns"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	DefaultAgentID string `json:"default_agent_id"`
	DefaultModel   string `json:"default_model"`
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
	SandboxProfile string            `json:"sandbox_profile,omitempty"`
	NetworkAccess  *bool             `json:"network_access,omitempty"`
	ReadOnly       *bool             `json:"read_only,omitempty"`
	MaxTurns       *int              `json:"max_turns,omitempty"`
	ApprovalMode   *string           `json:"approval_mode,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
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
