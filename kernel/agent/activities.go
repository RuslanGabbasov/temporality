package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/kernel/mcpclient"
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
	return &Activities{Events: events, Model: llm.New(config), HTTP: &http.Client{Timeout: config.Timeout}, TemporalityURL: strings.TrimRight(env("TEMPORALITY_URL", "http://localhost:8080"), "/"), MCP: mcpTools}, nil
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
	default:
		if a.MCP != nil && a.MCP.HasTool(request.Name) {
			content, err := a.MCP.Call(ctx, request.Name, request.Arguments)
			return ToolResult{Content: content}, err
		}
		return ToolResult{}, fmt.Errorf("tool %q is not registered", request.Name)
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

type Hint struct {
	KnowledgeID string `json:"knowledge_id"`
	HintID      string `json:"hint_id"`
	Proposition string `json:"proposition"`
	State       string `json:"state"`
	Caution     string `json:"caution,omitempty"`
}
