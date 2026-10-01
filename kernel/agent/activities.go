package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/kernel/mcpclient"
	"github.com/temporality-project/temporality/kernel/sandbox"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/temporal"
)

type EventOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// SandboxRunner is the sandbox contract the kernel depends on: resolving a
// workspace against the configured root and executing commands inside it.
// It exists so failure-injection drills can fake the runner in tests.
type SandboxRunner interface {
	SandboxRoot() string
	ResolveWorkspace(path string) (string, error)
	Execute(context.Context, sandbox.Request) (sandbox.Result, error)
}

type Activities struct {
	Events         EventOutbox
	Model          *llm.Client
	HTTP           *http.Client
	TemporalityURL string
	// APIToken authenticates direct journal calls (hints, knowledge lookup)
	// when the journal requires bearer auth; it mirrors TEMPORALITY_API_TOKEN
	// used by the event outbox publisher.
	APIToken      string
	MCP           *mcpclient.Client
	SourceID      string
	Sandbox       SandboxRunner
	NetworkAccess bool
}

func NewActivities(events EventOutbox) (*Activities, error) {
	config, err := llm.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	mcpTools, err := mcpclient.FromEnv(context.Background())
	if err != nil {
		return nil, err
	}
	sandboxRunner, err := sandbox.NewFromEnv()
	if err != nil {
		return nil, err
	}
	return &Activities{Events: events, Model: llm.New(config), HTTP: &http.Client{Timeout: config.Timeout}, TemporalityURL: strings.TrimRight(env("TEMPORALITY_URL", "http://localhost:8080"), "/"), APIToken: strings.TrimSpace(os.Getenv("TEMPORALITY_API_TOKEN")), MCP: mcpTools, SourceID: env("KERNEL_SOURCE_ID", "temporality-agent-kernel"), Sandbox: sandboxRunner}, nil
}

func (a *Activities) ToolDefs() []llm.ToolDef {
	defs := a.MCP.ToolDefs()
	if a.Sandbox != nil {
		defs = append(defs, llm.ToolDef{Name: "run_command", Description: "Run a command in an isolated sandbox container. The working directory is /workspace which persists between calls — save ALL files there. /tmp and /scratch are ephemeral and disappear between calls. Network access depends on agent configuration. Provide argv as an array.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "timeout_sec": map[string]any{"type": "integer"}}, "required": []string{"command"}}})
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
	input.MCPServer = MCPServerName()
	a.NetworkAccess = input.NetworkAccess
	if a.Sandbox != nil {
		autoApproveTools := []string{"run_command"}
		input.AutoApproveTools = autoApproveTools
		if input.WorkspacePath == "" {
			// Persistent workspace per project: shared across agents and runs,
			// so build caches (node_modules, go mod, pip) and artifacts persist.
			projectDir := filepath.Join(a.Sandbox.SandboxRoot(), "projects", input.Project)
			if err := os.MkdirAll(projectDir, 0o777); err != nil {
				return fmt.Errorf("create project workspace: %w", err)
			}
			input.WorkspacePath = projectDir
		}
		workspace, err := a.Sandbox.ResolveWorkspace(input.WorkspacePath)
		if err != nil {
			return err
		}
		input.WorkspacePath = workspace
	}
	return nil
}

// MCPServerName identifies the MCP server behind mcp__* tools in the event
// stream. An explicit KERNEL_MCP_SERVER_NAME wins; otherwise the basename of
// KERNEL_MCP_COMMAND serves as a stable default. Empty when no MCP server is
// configured.
func MCPServerName() string {
	if name := strings.TrimSpace(os.Getenv("KERNEL_MCP_SERVER_NAME")); name != "" {
		return name
	}
	command := strings.TrimSpace(os.Getenv("KERNEL_MCP_COMMAND"))
	if command == "" {
		return ""
	}
	return filepath.Base(command)
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
			// Argument validation happens before anything executes: the failure is
			// deterministic, has no side effect, and is fixable by the caller, so it
			// must be neither retried nor reported as an uncertain effect.
			return ToolResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidToolArguments", nil)
		}
		timeout := 0
		if value, ok := request.Arguments["timeout_sec"].(float64); ok {
			timeout = int(value)
		}
		if faultAfterEffect(request.RunID, "run_command", request.OperationID) {
			// Fault injection for live reconciliation drills: run the command for
			// real (the effect lands), then report a worker-style crash so the
			// workflow records tool.failed with effect=uncertain.
			net := ""
			if a.NetworkAccess {
				net = "bridge"
			}
			if _, err := a.Sandbox.Execute(ctx, sandbox.Request{Workspace: request.WorkspacePath, Command: command, TimeoutSeconds: timeout, ReadOnly: request.Role == "reviewer" || request.Role == "qa", Network: net}); err != nil {
				return ToolResult{}, err
			}
			return ToolResult{}, fmt.Errorf("injected worker crash after effect (operation %s)", request.OperationID)
		}
		net := ""
		if a.NetworkAccess {
			net = "bridge"
		}
		result, err := a.Sandbox.Execute(ctx, sandbox.Request{Workspace: request.WorkspacePath, Command: command, TimeoutSeconds: timeout, ReadOnly: request.Role == "reviewer" || request.Role == "qa", Network: net})
		if err != nil {
			return ToolResult{}, err
		}
		exitCode := result.ExitCode
		return ToolResult{Content: fmt.Sprintf("exit_code=%d\n%s", result.ExitCode, result.Output), ExitCode: &exitCode}, nil
	case "list_triggers":
		return a.handleListTriggers(ctx, request)
	case "create_trigger":
		return a.handleCreateTrigger(ctx, request)
	case "update_trigger":
		return a.handleUpdateTrigger(ctx, request)
	case "delete_trigger":
		return a.handleDeleteTrigger(ctx, request)
	case "skill_search":
		return a.handleSkillSearch(ctx, request)
	case "skill_inspect", "skill_validate", "skill_history", "skill_executions", "skill_memory":
		return a.handleSkillAction(ctx, request)
	default:
		if a.MCP != nil && a.MCP.HasTool(request.Name) {
			if faultAfterEffect(request.RunID, request.Name, request.OperationID) {
				// The effect must really commit before the "crash": the drill
				// exercises the window where the side effect landed but no
				// terminal event will ever arrive. The idempotency key keeps a
				// later operator-driven re-issue from duplicating it.
				if _, err := a.MCP.Call(ctx, request.Name, request.Arguments, request.OperationID); err != nil {
					return ToolResult{}, err
				}
				return ToolResult{}, fmt.Errorf("injected worker crash after effect (operation %s)", request.OperationID)
			}
			content, err := a.MCP.Call(ctx, request.Name, request.Arguments, request.OperationID)
			return ToolResult{Content: content}, err
		}
		return ToolResult{}, fmt.Errorf("tool %q is not registered", request.Name)
	}
}

// Trigger tool handlers — call the kernel's own HTTP API.

func (a *Activities) handleListTriggers(ctx context.Context, request ToolRequest) (ToolResult, error) {
	project := request.Project
	if project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	url := fmt.Sprintf("%s/v1/workspace/projects/%s/triggers", a.TemporalityURL, project)
	body, err := a.kernelGET(ctx, url)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	return ToolResult{Content: body}, nil
}

func (a *Activities) handleCreateTrigger(ctx context.Context, request ToolRequest) (ToolResult, error) {
	name, _ := request.Arguments["name"].(string)
	typ, _ := request.Arguments["type"].(string)
	prompt, _ := request.Arguments["prompt"].(string)
	if name == "" || typ == "" || prompt == "" {
		return ToolResult{Content: "error: name, type, and prompt are required"}, nil
	}
	config := map[string]any{"prompt": prompt}
	if cron, ok := request.Arguments["cron"].(string); ok && cron != "" {
		config["cron"] = cron
	}
	if path, ok := request.Arguments["path"].(string); ok && path != "" {
		config["path"] = path
		config["prompt_template"] = prompt
		delete(config, "prompt")
	}
	if eventType, ok := request.Arguments["event_type"].(string); ok && eventType != "" {
		config["event_type"] = eventType
	}
	trigger := map[string]any{
		"project_id": request.Project,
		"name":       name,
		"type":       typ,
		"enabled":    true,
		"config":     config,
	}
	if agentID, ok := request.Arguments["agent_id"].(string); ok && agentID != "" {
		trigger["agent_id"] = agentID
	}
	body, err := a.kernelPOST(ctx, a.TemporalityURL+"/v1/workspace/triggers", trigger)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	return ToolResult{Content: body}, nil
}

func (a *Activities) handleUpdateTrigger(ctx context.Context, request ToolRequest) (ToolResult, error) {
	triggerID, _ := request.Arguments["trigger_id"].(string)
	if triggerID == "" {
		return ToolResult{Content: "error: trigger_id is required"}, nil
	}
	update := map[string]any{}
	if enabled, ok := request.Arguments["enabled"].(bool); ok {
		update["enabled"] = enabled
	}
	if name, ok := request.Arguments["name"].(string); ok && name != "" {
		update["name"] = name
	}
	if cron, ok := request.Arguments["cron"].(string); ok && cron != "" {
		update["config"] = map[string]any{"cron": cron}
	}
	if prompt, ok := request.Arguments["prompt"].(string); ok && prompt != "" {
		cfg, _ := update["config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
		}
		cfg["prompt"] = prompt
		update["config"] = cfg
	}
	body, err := a.kernelPOST(ctx, a.TemporalityURL+"/v1/workspace/triggers/"+triggerID, update)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	return ToolResult{Content: body}, nil
}

func (a *Activities) handleDeleteTrigger(ctx context.Context, request ToolRequest) (ToolResult, error) {
	triggerID, _ := request.Arguments["trigger_id"].(string)
	if triggerID == "" {
		return ToolResult{Content: "error: trigger_id is required"}, nil
	}
	url := fmt.Sprintf("%s/v1/workspace/triggers/%s", a.TemporalityURL, triggerID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	req.Header.Set("Authorization", "Bearer "+a.APIToken)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	defer resp.Body.Close()
	return ToolResult{Content: fmt.Sprintf("deleted trigger %s (HTTP %d)", triggerID, resp.StatusCode)}, nil
}

func (a *Activities) kernelGET(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.APIToken)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), nil
}

// Skill tool handlers — read-only navigation over the skill registry. Skills
// change only through proposals and approval, never from a run
// (docs/living-skills.md §26).
func (a *Activities) handleSkillSearch(ctx context.Context, request ToolRequest) (ToolResult, error) {
	project := request.Project
	if project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	url := fmt.Sprintf("%s/v1/workspace/skills?project=%s", a.TemporalityURL, urlQueryEscape(project))
	body, err := a.kernelGET(ctx, url)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	query, _ := request.Arguments["query"].(string)
	if query == "" {
		return ToolResult{Content: compactJSON(body, 4000)}, nil
	}
	var page struct {
		Skills []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Version  string `json:"version"`
			Manifest struct {
				Capabilities []string `json:"capabilities"`
				Tools        []string `json:"tools"`
			} `json:"manifest"`
		} `json:"skills"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	needle := strings.ToLower(query)
	var matches []map[string]any
	for _, s := range page.Skills {
		haystack := strings.ToLower(s.ID + " " + s.Name + " " + strings.Join(s.Manifest.Capabilities, " ") + " " + strings.Join(s.Manifest.Tools, " "))
		if strings.Contains(haystack, needle) {
			matches = append(matches, map[string]any{"id": s.ID, "name": s.Name, "version": s.Version, "capabilities": s.Manifest.Capabilities, "tools": s.Manifest.Tools})
		}
	}
	encoded, _ := json.Marshal(map[string]any{"skills": matches, "count": len(matches)})
	return ToolResult{Content: string(encoded)}, nil
}

func (a *Activities) handleSkillAction(ctx context.Context, request ToolRequest) (ToolResult, error) {
	skillID, _ := request.Arguments["skill_id"].(string)
	if skillID == "" {
		return ToolResult{Content: "error: skill_id is required"}, nil
	}
	var url string
	switch request.Name {
	case "skill_inspect":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s", a.TemporalityURL, skillID)
	case "skill_validate":
		return a.handleSkillValidate(ctx, skillID)
	case "skill_history":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s/versions", a.TemporalityURL, skillID)
	case "skill_executions":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s/executions", a.TemporalityURL, skillID)
	case "skill_memory":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s/memory", a.TemporalityURL, skillID)
	default:
		return ToolResult{Content: "error: unknown skill tool " + request.Name}, nil
	}
	body, err := a.kernelGET(ctx, url)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	return ToolResult{Content: compactJSON(body, 6000)}, nil
}

// handleSkillValidate fetches the stored skill and re-checks its contract.
func (a *Activities) handleSkillValidate(ctx context.Context, skillID string) (ToolResult, error) {
	body, err := a.kernelGET(ctx, fmt.Sprintf("%s/v1/workspace/skills/%s", a.TemporalityURL, skillID))
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	var skill struct {
		Markdown     string          `json:"markdown"`
		ManifestYAML json.RawMessage `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(body), &skill); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	response, err := a.kernelPOST(ctx, fmt.Sprintf("%s/v1/workspace/skills/%s/validate", a.TemporalityURL, skillID), map[string]any{
		"markdown":      skill.Markdown,
		"manifest_yaml": string(skill.ManifestYAML),
	})
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	return ToolResult{Content: compactJSON(response, 4000)}, nil
}

func (a *Activities) kernelPOST(ctx context.Context, url string, body any) (string, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.APIToken)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(rb), nil
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

// faultDrilled tracks which runs already consumed the tool-name fault drill;
// entries live for the worker's lifetime, one per drilled run.
var (
	faultMu      sync.Mutex
	faultDrilled = map[string]bool{}
)

// faultAfterEffect reports whether this call should simulate a worker crash
// after its effect lands. KERNEL_FAULT_AFTER_EFFECT holds either an exact
// operation id, or a tool name ("run_command", "mcp__create_issue") which
// faults the first matching execution per run — operation ids embed
// model-generated call ids and cannot be predicted before the run starts,
// which is what a live drill needs.
func faultAfterEffect(runID, toolName, operationID string) bool {
	fault := strings.TrimSpace(os.Getenv("KERNEL_FAULT_AFTER_EFFECT"))
	if fault == "" || (fault != toolName && fault != operationID) {
		return false
	}
	if fault == operationID {
		return true
	}
	faultMu.Lock()
	defer faultMu.Unlock()
	if faultDrilled[runID] {
		return false
	}
	faultDrilled[runID] = true
	return true
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
	if a.APIToken != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+a.APIToken)
	}
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
	if a.APIToken != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+a.APIToken)
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
