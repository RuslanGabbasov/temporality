package workspace

import "time"

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Agent struct {
	ID             string   `json:"id"`
	ProjectID      string   `json:"project_id"`
	Name           string   `json:"name"`
	Model          string   `json:"model"`
	SystemPrompt   string   `json:"system_prompt"`
	Skills         []string `json:"skills"`
	MCPServers     []string `json:"mcp_servers"`
	SandboxProfile string   `json:"sandbox_profile"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Task struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	AgentID   string    `json:"agent_id,omitempty"`
	Title     string    `json:"title"`
	Prompt    string    `json:"prompt"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

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

// CreateProjectRequest is the payload for creating a project.
type CreateProjectRequest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CreateAgentRequest is the payload for creating an agent profile.
type CreateAgentRequest struct {
	ID             string   `json:"id"`
	ProjectID      string   `json:"project_id"`
	Name           string   `json:"name"`
	Model          string   `json:"model"`
	SystemPrompt   string   `json:"system_prompt"`
	Skills         []string `json:"skills"`
	MCPServers     []string `json:"mcp_servers"`
	SandboxProfile string   `json:"sandbox_profile"`
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