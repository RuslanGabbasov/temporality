package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/kernel/mcpclient"
	"github.com/temporality-project/temporality/kernel/sandbox"
	"github.com/temporality-project/temporality/observation"
)

type EventOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

type Activities struct {
	Events         EventOutbox
	Model          *llm.Client
	HTTP           *http.Client
	TemporalityURL string
	MCP            *mcpclient.Client
	SourceID       string
	Sandbox        *sandbox.Docker
}

func NewActivities(events EventOutbox) (*Activities, error) {
	config, err := llm.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	mcpCtx, cancelMCP := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelMCP()
	mcpTools, err := mcpclient.FromEnv(mcpCtx)
	if err != nil {
		return nil, err
	}
	sandboxRunner, err := sandbox.NewFromEnv()
	if err != nil {
		return nil, err
	}
	return &Activities{Events: events, Model: llm.New(config), HTTP: &http.Client{Timeout: config.Timeout}, TemporalityURL: strings.TrimRight(env("TEMPORALITY_URL", "http://localhost:8080"), "/"), MCP: mcpTools, SourceID: env("KERNEL_SOURCE_ID", "temporality-agent-kernel"), Sandbox: sandboxRunner}, nil
}

func (a *Activities) ToolDefs() []llm.ToolDef {
	defs := a.MCP.ToolDefs()
	if a.Sandbox != nil {
		defs = append(defs, llm.ToolDef{Name: "run_command", Description: "Run a command in the isolated workspace container. It is automatically authorized by the sandbox.workspace.v1 policy; it has no network and can write only to this run's workspace. Provide argv as an array.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "timeout_sec": map[string]any{"type": "integer"}}, "required": []string{"command"}}})
	}
	return defs
}

func (a *Activities) ApprovalTools() []string {
	return a.MCP.ApprovalTools()
}

func (a *Activities) PrepareRun(input *RunInput) error {
	input.SourceID = a.SourceID
	input.Tools = a.ToolDefs()
	input.ApprovalTools = a.ApprovalTools()
	input.AutoApproveTools = nil
	if a.Sandbox != nil {
		input.AutoApproveTools = []string{"run_command"}
		workspace, err := a.Sandbox.ResolveWorkspace(input.WorkspacePath)
		if err != nil {
			return err
		}
		input.WorkspacePath = workspace
	}
	return nil
}

func (a *Activities) RecordEvent(ctx context.Context, event observation.Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if a.Events == nil {
		return errors.New("event outbox is not configured")
	}
	return a.Events.Enqueue(ctx, event)
}

func (a *Activities) CallModel(ctx context.Context, request ModelRequest) (llm.Completion, error) {
	if a.Model == nil {
		return llm.Completion{}, errors.New("model client is not configured")
	}
	if request.Model != "" {
		// The existing client config is immutable; constructing a provider client
		// per run keeps model selection out of deterministic Workflow code.
		config, err := llm.ConfigFromEnv()
		if err != nil {
			return llm.Completion{}, err
		}
		config.Model = request.Model
		return llm.New(config).Complete(ctx, request.Messages, request.Tools)
	}
	return a.Model.Complete(ctx, request.Messages, request.Tools)
}

func (a *Activities) RunTool(ctx context.Context, request ToolRequest) (ToolResult, error) {
	switch request.Name {
	case "echo":
		value, _ := request.Arguments["text"].(string)
		if value == "" {
			return ToolResult{}, errors.New("echo requires text")
		}
		return ToolResult{Content: value}, nil
	case "run_command":
		if a.Sandbox == nil {
			return ToolResult{}, errors.New("sandbox is not configured")
		}
		command, err := stringArgs(request.Arguments["command"])
		if err != nil {
			return ToolResult{}, err
		}
		timeout := 0
		if value, ok := request.Arguments["timeout_sec"].(float64); ok {
			timeout = int(value)
		}
		result, err := a.Sandbox.Execute(ctx, sandbox.Request{Workspace: request.WorkspacePath, Command: command, TimeoutSeconds: timeout, ReadOnly: request.Role == "reviewer" || request.Role == "qa"})
		if err != nil {
			return ToolResult{}, err
		}
		exitCode := result.ExitCode
		return ToolResult{Content: fmt.Sprintf("exit_code=%d\n%s", result.ExitCode, result.Output), ExitCode: &exitCode}, nil
	default:
		if a.MCP != nil && a.MCP.HasTool(request.Name) {
			content, err := a.MCP.Call(ctx, request.Name, request.Arguments)
			return ToolResult{Content: content}, err
		}
		return ToolResult{}, fmt.Errorf("tool %q is not registered", request.Name)
	}
}

func stringArgs(value any) ([]string, error) {
	switch values := value.(type) {
	case []string:
		return values, nil
	case []any:
		result := make([]string, len(values))
		for i, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("command[%d] must be a string", i)
			}
			result[i] = text
		}
		return result, nil
	default:
		return nil, errors.New("command must be an array of strings")
	}
}

func (a *Activities) KnowledgeHints(ctx context.Context, request HintRequest) ([]Hint, error) {
	payload, err := json.Marshal(map[string]any{"project": request.Project, "run": request.RunID, "task": request.TaskID, "actor": map[string]string{"id": request.ActorID, "type": "agent"}, "query": request.Query, "limit": 8})
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TemporalityURL+"/v1/observations/hints", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := a.HTTP.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Temporality hints returned HTTP %d", response.StatusCode)
	}
	var reply struct {
		Hints []Hint `json:"hints"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return reply.Hints, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type HintRequest struct {
	Project string `json:"project"`
	RunID   string `json:"run_id"`
	TaskID  string `json:"task_id"`
	ActorID string `json:"actor_id"`
	Query   string `json:"query"`
}

// KnowledgeLookupResult reports the current projected state of one knowledge
// item. Exists is false when the item is unknown to the project.
type KnowledgeLookupResult struct {
	Exists bool
	State  string
}

// KnowledgeLookup fetches the current state of one knowledge item so the
// workflow can reuse or confirm an existing observation instead of proposing
// a duplicate node.
func (a *Activities) KnowledgeLookup(ctx context.Context, request KnowledgeLookupQuery) (KnowledgeLookupResult, error) {
	url := fmt.Sprintf("%s/v1/observations/knowledge?project=%s&knowledge_id=%s", a.TemporalityURL, url.QueryEscape(request.Project), url.QueryEscape(request.KnowledgeID))
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return KnowledgeLookupResult{}, err
	}
	response, err := a.HTTP.Do(httpRequest)
	if err != nil {
		return KnowledgeLookupResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return KnowledgeLookupResult{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return KnowledgeLookupResult{}, fmt.Errorf("Temporality knowledge lookup returned HTTP %d", response.StatusCode)
	}
	var reply struct {
		Knowledge []struct {
			State string `json:"state"`
		} `json:"knowledge"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return KnowledgeLookupResult{}, err
	}
	if len(reply.Knowledge) == 0 {
		return KnowledgeLookupResult{}, nil
	}
	return KnowledgeLookupResult{Exists: true, State: reply.Knowledge[0].State}, nil
}

type KnowledgeLookupQuery struct {
	Project     string
	KnowledgeID string
}

type Hint struct {
	KnowledgeID string `json:"knowledge_id"`
	HintID      string `json:"hint_id"`
	Proposition string `json:"proposition"`
	State       string `json:"state"`
	Caution     string `json:"caution,omitempty"`
}
