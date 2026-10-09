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
	"time"

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
	Events EventOutbox
	Model  *llm.Client
	HTTP   *http.Client
	// TemporalityURL is the journal base URL (observations, hints, events).
	TemporalityURL string
	// WorkspaceURL is the base URL of the kernel's own workspace API
	// (skills, triggers, projects). Agent tools that read or write workspace
	// entities must call this, not the journal.
	WorkspaceURL string
	// WorkspaceToken authenticates loopback calls against the kernel's own
	// workspace API when the kernel gate is enabled. It is a startup-random
	// internal credential injected in main, never exposed via config.
	WorkspaceToken string
	// APIToken authenticates direct journal calls (hints, knowledge lookup)
	// when the journal requires bearer auth; it mirrors TEMPORALITY_API_TOKEN
	// used by the event outbox publisher.
	APIToken      string
	MCP           *mcpclient.Registry
	SourceID      string
	Sandbox       SandboxRunner
	NetworkAccess bool
	// Tokens is the ephemeral live-token bus feeding the /runs/{id}/tokens SSE
	// endpoint; nil disables streaming (tests run the blocking path).
	Tokens *TokenBus
	// ChannelSettings loads the admin-editable transport credential overlay
	// (workspace channel_transport row). Nil — or a load error — degrades to
	// the KERNEL_* environment variables, preserving env-only deployments.
	ChannelSettings func(context.Context) (TransportSettings, error)
}

func NewActivities(events EventOutbox) (*Activities, error) {
	config, err := llm.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	registry := mcpclient.NewRegistry()
	legacy, err := mcpclient.FromEnv(context.Background())
	if err != nil {
		return nil, err
	}
	if legacy != nil {
		registry.AdoptLegacy(legacy)
	}
	sandboxRunner, err := sandbox.NewFromEnv()
	if err != nil {
		return nil, err
	}
	return &Activities{Events: events, Model: llm.New(config), HTTP: &http.Client{Timeout: config.Timeout}, TemporalityURL: strings.TrimRight(env("TEMPORALITY_URL", "http://localhost:8080"), "/"), WorkspaceURL: strings.TrimRight(env("KERNEL_WORKSPACE_URL", "http://localhost:8090"), "/"), APIToken: strings.TrimSpace(os.Getenv("TEMPORALITY_API_TOKEN")), MCP: registry, SourceID: env("KERNEL_SOURCE_ID", "temporality-agent-kernel"), Sandbox: sandboxRunner}, nil
}

func (a *Activities) ToolDefs() []llm.ToolDef {
	return a.ToolDefsFor(nil)
}

// runCommandTool is the sandbox execution surface. /workspace is the
// project's persistent storage shared across agents, runs and sessions
// (PrepareRun), so the description is the contract that tells models what
// survives there and what does not.
var runCommandTool = llm.ToolDef{
	Name:        "run_command",
	Description: "Run a command in an isolated sandbox container. The working directory is /workspace — persistent storage shared by all agents and runs of this project: files saved there survive across sessions and it may already contain files from earlier work, so treat them as shared and do not remove what you did not create. /tmp and /scratch are ephemeral and disappear between calls. Network access depends on agent configuration. Provide argv as an array.",
	Parameters:  map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "timeout_sec": map[string]any{"type": "integer"}}, "required": []string{"command"}},
}

// BuiltinToolDefs returns kernel tools plus run_command (when the sandbox is
// configured) — the tool surface independent of any MCP server.
func (a *Activities) BuiltinToolDefs() []llm.ToolDef {
	defs := KernelTools()
	if a.Sandbox != nil {
		defs = append(defs, runCommandTool)
	}
	return defs
}

// ToolDefsFor returns configured MCP tools for the given servers plus the
// sandbox tool. An empty server list keeps the legacy env server only.
func (a *Activities) ToolDefsFor(servers []string) []llm.ToolDef {
	defs := a.MCP.ToolDefsFor(servers)
	if a.Sandbox != nil {
		defs = append(defs, runCommandTool)
	}
	return defs
}

func (a *Activities) ApprovalTools() []string {
	return a.ApprovalToolsFor(nil)
}

func (a *Activities) ApprovalToolsFor(servers []string) []string {
	return a.MCP.ApprovalToolsFor(servers)
}

func (a *Activities) PrepareRun(input *RunInput) error {
	input.SourceID = a.SourceID
	input.Tools = a.ToolDefsFor(input.MCPServers)
	input.ApprovalTools = a.ApprovalToolsFor(input.MCPServers)
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

// ResolveAgentRequest asks the workspace for the enforced run configuration of
// a delegation target (docs/agent-delegation.md §4).
type ResolveAgentRequest struct {
	Project         string `json:"project"`
	AgentID         string `json:"agent_id"`
	RunID           string `json:"run_id"` // child run id assigned by the parent workflow
	TaskID          string `json:"task_id,omitempty"`
	Prompt          string `json:"prompt"`
	ActorID         string `json:"actor_id,omitempty"`
	MaxTurns        int    `json:"max_turns,omitempty"`
	DelegationDepth int    `json:"delegation_depth,omitempty"`
}

// ResolveAgent builds the child RunInput for a delegated run: it fetches the
// target agent's enforced configuration from the workspace API (the same code
// path a manual run start uses), applies sandbox/tool preparation and denies
// further delegation when the depth limit is reached.
func (a *Activities) ResolveAgent(ctx context.Context, request ResolveAgentRequest) (RunInput, error) {
	endpoint := fmt.Sprintf("%s/v1/workspace/agents/%s/run-config?project=%s", a.WorkspaceURL, url.PathEscape(request.AgentID), url.QueryEscape(request.Project))
	if request.ActorID != "" {
		// The delegated run inherits the org position of the delegating chain:
		// the run-config endpoint resolves the actor's policies from it (§24).
		endpoint += "&actor_id=" + url.QueryEscape(request.ActorID)
	}
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return RunInput{}, err
	}
	if status == http.StatusNotFound {
		return RunInput{}, temporal.NewNonRetryableApplicationError(fmt.Sprintf("agent %q is not available in project %q", request.AgentID, request.Project), "AgentNotAvailable", nil)
	}
	if status != http.StatusOK {
		return RunInput{}, temporal.NewNonRetryableApplicationError(fmt.Sprintf("workspace returned %d resolving agent %q: %s", status, request.AgentID, truncate(body, 400)), "AgentResolutionFailed", nil)
	}
	var child RunInput
	if err := json.Unmarshal([]byte(body), &child); err != nil {
		return RunInput{}, temporal.NewNonRetryableApplicationError("agent run configuration is malformed: "+err.Error(), "AgentResolutionFailed", nil)
	}
	child.RunID = request.RunID
	child.Project = request.Project
	child.TaskID = request.TaskID
	child.Prompt = request.Prompt
	child.ActorID = request.ActorID
	child.DelegationDepth = request.DelegationDepth
	if request.MaxTurns > 0 {
		child.MaxTurns = request.MaxTurns
	}
	if err := a.PrepareRun(&child); err != nil {
		return RunInput{}, temporal.NewNonRetryableApplicationError("prepare delegated run: "+err.Error(), "AgentResolutionFailed", nil)
	}
	if child.DelegationDepth >= MaxDelegationDepth {
		child.DenyTools = append(child.DenyTools, "delegate", "plan")
	}
	return child, nil
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
	client := a.Model
	if request.Model != "" {
		// The existing client config is immutable; constructing a provider client
		// per run keeps model selection out of deterministic Workflow code.
		config, err := llm.ConfigFromEnv()
		if err != nil {
			return llm.Completion{}, err
		}
		config.Model = request.Model
		client = llm.New(config)
	}
	if client == nil {
		return llm.Completion{}, errors.New("model client is not configured")
	}
	if a.Tokens != nil && request.RunID != "" {
		// Live path: stream tokens to the ephemeral bus for subscribed UIs. A
		// streaming failure falls through to the blocking call below, which has
		// its own retries — the live preview is best-effort, the Completion is
		// the source of truth the workflow records.
		if completion, err := client.StreamComplete(ctx, request.Messages, request.Tools, func(delta llm.StreamDelta) bool {
			if delta.Text != "" || delta.Reasoning != "" {
				a.Tokens.Publish(request.RunID, request.Turn, delta.Text, delta.Reasoning)
			}
			return false
		}); err == nil {
			return completion, nil
		}
	}
	return client.Complete(ctx, request.Messages, request.Tools)
}

func (a *Activities) RunTool(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if len(request.AllowedTools) > 0 && !allowedForAgent(request.AllowedTools, request.Name) {
		// Defense in depth: the workflow already filters the tools advertised to
		// the model; this rejects calls to tools outside the agent's allowlist.
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("tool %q is not allowed for this agent", request.Name), "ToolNotAllowed", nil)
	}
	if allowedForAgent(request.DeniedTools, request.Name) {
		// Capability enforcement: tools disabled by the agent definition are
		// neither advertised nor executable, regardless of the allowlist.
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("tool %q is disabled for this agent", request.Name), "ToolNotAllowed", nil)
	}
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
			if _, err := a.Sandbox.Execute(ctx, sandbox.Request{Workspace: request.WorkspacePath, Command: command, TimeoutSeconds: timeout, ReadOnly: request.ReadOnly || request.Role == "reviewer" || request.Role == "qa", Network: net}); err != nil {
				return ToolResult{}, err
			}
			return ToolResult{}, fmt.Errorf("injected worker crash after effect (operation %s)", request.OperationID)
		}
		net := ""
		if a.NetworkAccess {
			net = "bridge"
		}
		result, err := a.Sandbox.Execute(ctx, sandbox.Request{Workspace: request.WorkspacePath, Command: command, TimeoutSeconds: timeout, ReadOnly: request.ReadOnly || request.Role == "reviewer" || request.Role == "qa", Network: net})
		if err != nil {
			return ToolResult{}, err
		}
		exitCode := result.ExitCode
		return ToolResult{Content: fmt.Sprintf("exit_code=%d\n%s", result.ExitCode, result.Output), ExitCode: &exitCode}, nil
	case "list_triggers":
		return a.handleListTriggers(ctx, request)
	case "human_contacts":
		return a.handleHumanContacts(ctx, request)
	case "send_file":
		return a.handleSendFile(ctx, request)
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
	case "skill_propose":
		return a.handleSkillPropose(ctx, request)
	case "skill_evaluate":
		return a.handleSkillEvaluate(ctx, request)
	case "skill_diff":
		return a.handleSkillDiff(ctx, request)
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

func allowedForAgent(allowed []string, name string) bool {
	for _, candidate := range allowed {
		if candidate == name {
			return true
		}
	}
	return false
}

// Trigger tool handlers — call the kernel's own HTTP API.

func (a *Activities) handleListTriggers(ctx context.Context, request ToolRequest) (ToolResult, error) {
	project := request.Project
	if project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	url := fmt.Sprintf("%s/v1/workspace/projects/%s/triggers", a.WorkspaceURL, project)
	body, status, err := a.workspaceDo(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: list triggers failed (HTTP %d): %s", status, truncate(body, 400))}, nil
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
	body, status, err := a.workspaceDo(ctx, http.MethodPost, a.WorkspaceURL+"/v1/workspace/triggers", trigger)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: create trigger failed (HTTP %d): %s", status, truncate(body, 400))}, nil
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
	body, status, err := a.workspaceDo(ctx, http.MethodPost, a.WorkspaceURL+"/v1/workspace/triggers/"+triggerID, update)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: update trigger failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: body}, nil
}

func (a *Activities) handleDeleteTrigger(ctx context.Context, request ToolRequest) (ToolResult, error) {
	triggerID, _ := request.Arguments["trigger_id"].(string)
	if triggerID == "" {
		return ToolResult{Content: "error: trigger_id is required"}, nil
	}
	url := fmt.Sprintf("%s/v1/workspace/triggers/%s", a.WorkspaceURL, triggerID)
	body, status, err := a.workspaceDo(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: delete trigger failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: fmt.Sprintf("deleted trigger %s", triggerID)}, nil
}

// workspaceDo performs an authenticated request against the kernel's own
// workspace API (skills, triggers). It returns the response body and status;
// transport failures come back as errors so tool results never present a
// 404 payload as usable content.
func (a *Activities) workspaceDo(ctx context.Context, method, url string, body any) (string, int, error) {
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return "", 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	token := a.WorkspaceToken
	if token == "" {
		token = a.APIToken
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), resp.StatusCode, nil
}

// Skill tool handlers — read-only navigation over the skill registry. Skills
// change only through proposals and approval, never from a run
// (docs/living-skills.md §26).
func (a *Activities) handleSkillSearch(ctx context.Context, request ToolRequest) (ToolResult, error) {
	// Skills are workspace-global: the registry is shared across projects.
	url := fmt.Sprintf("%s/v1/workspace/skills", a.WorkspaceURL)
	body, status, err := a.workspaceDo(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: skill search failed (HTTP %d): %s", status, truncate(body, 400))}, nil
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
		url = fmt.Sprintf("%s/v1/workspace/skills/%s", a.WorkspaceURL, skillID)
	case "skill_validate":
		return a.handleSkillValidate(ctx, skillID)
	case "skill_history":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s/versions", a.WorkspaceURL, skillID)
	case "skill_executions":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s/executions", a.WorkspaceURL, skillID)
	case "skill_memory":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s/memory", a.WorkspaceURL, skillID)
	default:
		return ToolResult{Content: "error: unknown skill tool " + request.Name}, nil
	}
	body, status, err := a.workspaceDo(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status == 404 {
		return ToolResult{Content: fmt.Sprintf("error: skill %q not found; use skill_search to list ids", skillID)}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: %s failed (HTTP %d): %s", request.Name, status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: compactJSON(body, 6000)}, nil
}

// handleSkillValidate fetches the stored skill and re-checks its contract.
func (a *Activities) handleSkillValidate(ctx context.Context, skillID string) (ToolResult, error) {
	body, status, err := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/skills/%s", a.WorkspaceURL, skillID), nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: skill %q not readable (HTTP %d)", skillID, status)}, nil
	}
	var skill struct {
		Markdown     string          `json:"markdown"`
		ManifestYAML json.RawMessage `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(body), &skill); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	response, status, err := a.workspaceDo(ctx, http.MethodPost, fmt.Sprintf("%s/v1/workspace/skills/%s/validate", a.WorkspaceURL, skillID), map[string]any{
		"markdown":      skill.Markdown,
		"manifest_yaml": string(skill.ManifestYAML),
	})
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: validate failed (HTTP %d): %s", status, truncate(response, 400))}, nil
	}
	return ToolResult{Content: compactJSON(response, 4000)}, nil
}

// handleSkillPropose turns a natural-language capability description into a
// structured skill draft (via the skill builder) and saves it as a proposal:
// a draft version for human review, never the live revision. Per
// docs/knowledge-evolution.md Wave A the proposal lands with full provenance
// (origin, source run, evidence) and goes live only when a human applies it
// in the Skills UI (skill.applied event).
func (a *Activities) handleSkillPropose(ctx context.Context, request ToolRequest) (ToolResult, error) {
	description, _ := request.Arguments["description"].(string)
	if strings.TrimSpace(description) == "" {
		return ToolResult{Content: "error: description is required"}, nil
	}
	draft, err := BuildSkillDraft(ctx, a.Model, description, a.ToolsSummary(), false)
	if err != nil {
		return ToolResult{Content: "error: skill builder failed: " + err.Error()}, nil
	}
	var manifest struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	_ = json.Unmarshal(draft.Manifest, &manifest)
	id := manifest.ID
	if id == "" {
		id = skillSlug(draft.Name)
	}
	if id == "" {
		return ToolResult{Content: "error: could not derive a skill id from the draft"}, nil
	}
	// Provenance: the run and operation that produced this proposal. Version
	// policy stays server-side — the workspace API picks the next free patch
	// for existing skills and 0.x for new capabilities.
	sourceRuns := []string{}
	if request.RunID != "" {
		sourceRuns = append(sourceRuns, request.RunID)
	}
	evidenceRefs := []string{}
	if request.OperationID != "" {
		evidenceRefs = append(evidenceRefs, request.OperationID)
	}
	changeSummary := "Agent proposal: " + truncate(draft.Description, 180)
	// Normalize the manifest so identity fields agree with the stored row.
	manifestMap := map[string]any{}
	if err := json.Unmarshal(draft.Manifest, &manifestMap); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if _, ok := manifestMap["name"]; !ok {
		manifestMap["name"] = draft.Name
	}
	if _, ok := manifestMap["description"]; !ok {
		manifestMap["description"] = draft.Description
	}
	manifestMap["id"] = id
	delete(manifestMap, "version") // server picks the version, never the model
	normalizedManifest, err := json.Marshal(manifestMap)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	exists := false
	if current, status, err := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/skills/%s", a.WorkspaceURL, id), nil); err == nil && status == 200 {
		var existing struct {
			Version string `json:"version"`
		}
		if json.Unmarshal([]byte(current), &existing) == nil && existing.Version != "" {
			exists = true
		}
	}
	var (
		version string
		action  string
	)
	if exists {
		// Skill exists — propose the draft as its next version; the current
		// revision keeps serving agents until a human applies the draft.
		payload := map[string]any{
			"markdown":       draft.Markdown,
			"manifest_yaml":  string(normalizedManifest),
			"origin":         "agent-proposal",
			"source_runs":    sourceRuns,
			"evidence_refs":  evidenceRefs,
			"change_summary": changeSummary,
		}
		body, status, err := a.workspaceDo(ctx, http.MethodPost, fmt.Sprintf("%s/v1/workspace/skills/%s/versions", a.WorkspaceURL, id), payload)
		if err != nil {
			return ToolResult{Content: "error: " + err.Error()}, nil
		}
		if status >= 400 {
			return ToolResult{Content: fmt.Sprintf("error: propose skill version failed (HTTP %d): %s", status, truncate(body, 600))}, nil
		}
		var proposed struct {
			Version string `json:"version"`
		}
		_ = json.Unmarshal([]byte(body), &proposed)
		version = proposed.Version
		action = "draft version proposed for existing skill"
	} else {
		payload := map[string]any{
			"id":             id,
			"name":           draft.Name,
			"description":    draft.Description,
			"version":        "0.1.0",
			"markdown":       draft.Markdown,
			"manifest_yaml":  string(normalizedManifest),
			"draft":          true,
			"origin":         "agent-proposal",
			"source_runs":    sourceRuns,
			"evidence_refs":  evidenceRefs,
			"change_summary": changeSummary,
		}
		body, status, err := a.workspaceDo(ctx, http.MethodPost, a.WorkspaceURL+"/v1/workspace/skills", payload)
		if err != nil {
			return ToolResult{Content: "error: " + err.Error()}, nil
		}
		if status >= 400 {
			return ToolResult{Content: fmt.Sprintf("error: save skill draft failed (HTTP %d): %s", status, truncate(body, 600))}, nil
		}
		version = "0.1.0"
		action = "skill created as draft"
	}
	a.emitSkillProposed(ctx, request, id, draft.Name, version, changeSummary, evidenceRefs)
	response := map[string]any{
		"skill_id":  id,
		"name":      draft.Name,
		"version":   version,
		"state":     "draft",
		"action":    action,
		"next_step": "a human must review and apply the draft in the Skills UI before the skill becomes available to agents",
		"questions": draft.Questions,
	}
	encoded, _ := json.Marshal(response)
	return ToolResult{Content: string(encoded)}, nil
}

// emitSkillProposed records the skill.proposed event in the journal via the
// durable outbox. Best-effort: the draft row is already saved, so an outbox
// miss must not fail the tool call.
func (a *Activities) emitSkillProposed(ctx context.Context, request ToolRequest, skillID, skillName, version, changeSummary string, evidenceRefs []string) {
	if a.Events == nil {
		return
	}
	evidence := make([]observation.Evidence, 0, len(evidenceRefs))
	for _, ref := range evidenceRefs {
		evidence = append(evidence, observation.Evidence{Ref: ref})
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("%s/skill-proposed/%s", request.RunID, version),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: a.SourceID, Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: request.Project, Run: request.RunID,
			Actor: observation.Actor{ID: a.SourceID, Type: "agent"},
		},
		Type:     "skill.proposed",
		Data:     map[string]any{"skill_id": skillID, "skill_name": skillName, "version": version, "origin": "agent-proposal", "change_summary": changeSummary, "source_runs": []string{request.RunID}},
		Evidence: evidence,
	}
	if err := a.Events.Enqueue(ctx, event); err != nil {
		return
	}
}

// ToolsSummary renders builtin + MCP tools as a compact text list for the
// skill builder prompt, so drafts reference real tool names. It is shared by
// the UI wizard endpoint and the skill_propose agent tool.
func (a *Activities) ToolsSummary() string {
	var b strings.Builder
	for _, def := range a.BuiltinToolDefs() {
		if def.Description != "" {
			fmt.Fprintf(&b, "- %s — %s\n", def.Name, def.Description)
		} else {
			fmt.Fprintf(&b, "- %s\n", def.Name)
		}
	}
	for name, server := range a.MCP.ServerTools() {
		fmt.Fprintf(&b, "MCP server %q: ", name)
		tools := make([]string, 0, len(server.Tools))
		for _, tool := range server.Tools {
			tools = append(tools, tool.ModelName)
		}
		b.WriteString(strings.Join(tools, ", "))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// skillSlug derives a kebab-case id from a human skill name.
func skillSlug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// truncate shortens s for inclusion in a compact tool error message.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
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
// item. Exists is false when the item is unknown to the project. Proposition
// rides along so lifecycle events can carry the human-readable text instead
// of a bare id.
type KnowledgeLookupResult struct {
	Exists      bool
	State       string
	Proposition string
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
			State       string `json:"state"`
			Proposition string `json:"proposition"`
		} `json:"knowledge"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return KnowledgeLookupResult{}, err
	}
	if len(reply.Knowledge) == 0 {
		return KnowledgeLookupResult{}, nil
	}
	return KnowledgeLookupResult{Exists: true, State: reply.Knowledge[0].State, Proposition: reply.Knowledge[0].Proposition}, nil
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
