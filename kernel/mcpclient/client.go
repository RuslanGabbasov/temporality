// Package mcpclient adapts configured MCP servers (stdio, SSE, Streamable
// HTTP) to Kernel tools. Registry servers may leave the allowlist empty to
// expose everything a server advertises (binding a server to an agent is an
// explicit act); the legacy env contract still requires an explicit
// KERNEL_MCP_ALLOW. Approval-required tools are exposed to the model but only
// execute after a durable human approval signal.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/temporality-project/temporality/kernel/llm"
)

const prefix = "mcp__"
const maxResultBytes = 64 << 10

// Client adapts one MCP server. A client is stateless between calls: every
// Call (and every Refresh) opens a fresh session — stdio spawns the process,
// remote transports dial the endpoint — so a server restart is transparent.
type Client struct {
	cfg     ServerConfig
	tools   map[string]string // model-facing name -> bare MCP tool name
	defs    []llm.ToolDef
	approve map[string]bool
}

// NewClient builds an unconnected client. Call Refresh to discover tools.
func NewClient(cfg ServerConfig) *Client {
	return &Client{
		cfg:     cfg,
		tools:   map[string]string{},
		approve: map[string]bool{},
	}
}

// modelName maps a bare MCP tool name to the kernel-facing tool name. The
// legacy env server (empty ID) keeps mcp__<tool>; registered servers are
// namespaced mcp__<server>__<tool> so multiple servers never collide.
func (c *Client) modelName(bare string) string {
	if c.cfg.ID == "" {
		return prefix + bare
	}
	return prefix + c.cfg.ID + "__" + bare
}

// Refresh connects, lists tools and rebuilds the allowlisted tool set.
func (c *Client) Refresh(ctx context.Context) error {
	session, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list MCP tools: %w", err)
	}
	allowed := nameSet(c.cfg.Allow)
	next := map[string]string{}
	var defs []llm.ToolDef
	for _, tool := range result.Tools {
		if len(allowed) > 0 && !allowed[tool.Name] {
			continue
		}
		name := c.modelName(tool.Name)
		if len(name) > 64 {
			return fmt.Errorf("MCP tool name %q exceeds model tool name limit", tool.Name)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			return fmt.Errorf("MCP tool %q has unsupported input schema %T", tool.Name, tool.InputSchema)
		}
		next[name] = tool.Name
		defs = append(defs, llm.ToolDef{Name: name, Description: tool.Description, Parameters: schema})
	}
	approve := map[string]bool{}
	for _, bare := range c.cfg.Approval {
		approve[c.modelName(bare)] = true
	}
	c.tools = next
	c.defs = defs
	c.approve = approve
	return nil
}

// FromEnv builds the legacy single stdio server from KERNEL_MCP_* variables.
// Its tools keep the un-namespaced mcp__<tool> naming.
func FromEnv(ctx context.Context) (*Client, error) {
	command := strings.TrimSpace(os.Getenv("KERNEL_MCP_COMMAND"))
	if command == "" {
		return nil, nil
	}
	var args []string
	if value := os.Getenv("KERNEL_MCP_ARGS"); value != "" {
		if err := json.Unmarshal([]byte(value), &args); err != nil {
			return nil, fmt.Errorf("KERNEL_MCP_ARGS must be a JSON string array: %w", err)
		}
	}
	allow := parseNames(os.Getenv("KERNEL_MCP_ALLOW"))
	if len(allow) == 0 {
		return nil, errors.New("KERNEL_MCP_ALLOW must explicitly list permitted MCP tools")
	}
	approveNames := parseNames(os.Getenv("KERNEL_MCP_APPROVAL"))
	if err := validateApprovalNames(allow, approveNames); err != nil {
		return nil, err
	}
	cfg := ServerConfig{
		ID:       "",
		Name:     legacyServerName(command),
		Type:     TypeStdio,
		Command:  command,
		Args:     args,
		Allow:    allow,
		Approval: approveNames,
		Enabled:  true,
	}
	c := NewClient(cfg)
	if err := c.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("connect MCP server: %w", err)
	}
	// The env contract is strict: every allowlisted tool must exist.
	advertised := map[string]bool{}
	for bare := range allowedBares(c.tools) {
		advertised[bare] = true
	}
	for _, name := range allow {
		if !advertised[name] {
			return nil, fmt.Errorf("allowlisted MCP tool %q was not advertised by server", name)
		}
	}
	return c, nil
}

// allowedBares inverts the model-name map to bare names.
func allowedBares(tools map[string]string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, bare := range tools {
		result[bare] = struct{}{}
	}
	return result
}

func legacyServerName(command string) string {
	if name := strings.TrimSpace(os.Getenv("KERNEL_MCP_SERVER_NAME")); name != "" {
		return name
	}
	parts := strings.Split(command, "/")
	return parts[len(parts)-1]
}

// Tools returns the tools discovered by the last successful Refresh.
func (c *Client) Tools() []ToolInfo {
	result := make([]ToolInfo, 0, len(c.defs))
	for _, def := range c.defs {
		result = append(result, ToolInfo{
			Name:             c.tools[def.Name],
			Description:      def.Description,
			ModelName:        def.Name,
			RequiresApproval: c.approve[def.Name],
		})
	}
	return result
}

func (c *Client) ToolDefs() []llm.ToolDef {
	if c == nil {
		return nil
	}
	return append([]llm.ToolDef(nil), c.defs...)
}
func (c *Client) RequiresApproval(name string) bool { return c != nil && c.approve[name] }
func (c *Client) ApprovalTools() []string {
	if c == nil {
		return nil
	}
	var names []string
	for name := range c.approve {
		if c.HasTool(name) {
			names = append(names, name)
		}
	}
	return names
}
func (c *Client) HasTool(name string) bool {
	if c == nil {
		return false
	}
	_, ok := c.tools[name]
	return ok
}

// Call executes one MCP tool. idempotencyKey is propagated via the
// protocol's _meta field: servers that support idempotent execution can
// deduplicate retries of the same operation (the kernel always retries a
// consequential call as a NEW operation, so keys are never reused across
// distinct intents). Servers without support simply ignore it.
func (c *Client) Call(ctx context.Context, name string, args map[string]any, idempotencyKey string) (string, error) {
	if c == nil {
		return "", errors.New("MCP is not configured")
	}
	actual, ok := c.tools[name]
	if !ok {
		return "", fmt.Errorf("MCP tool %q is not allowed", name)
	}
	session, err := c.connect(ctx)
	if err != nil {
		return "", err
	}
	defer session.Close()
	params := &mcp.CallToolParams{Name: actual, Arguments: args}
	if idempotencyKey != "" {
		params.Meta = mcp.Meta{"idempotency_key": idempotencyKey}
	}
	result, err := session.CallTool(ctx, params)
	if err != nil {
		return "", err
	}
	if result.IsError {
		return "", errors.New(contentText(result.Content))
	}
	return contentText(result.Content), nil
}

func (c *Client) connect(ctx context.Context) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "temporality-agent-kernel", Version: "0.1.0"}, nil)
	var transport mcp.Transport
	switch c.cfg.Type {
	case TypeSSE:
		transport = &mcp.SSEClientTransport{Endpoint: c.cfg.URL, HTTPClient: headerClient(c.cfg.Headers)}
	case TypeHTTP:
		transport = &mcp.StreamableClientTransport{Endpoint: c.cfg.URL, HTTPClient: headerClient(c.cfg.Headers)}
	default: // stdio
		cmd := exec.CommandContext(ctx, c.cfg.Command, c.cfg.Args...)
		cmd.Stderr = os.Stderr // surface server panics/errors in kernel logs
		if len(c.cfg.Env) > 0 {
			cmd.Env = append(os.Environ(), c.cfg.Env...)
		}
		transport = &mcp.CommandTransport{Command: cmd}
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect MCP server %q: %w", c.cfg.Name, err)
	}
	return session, nil
}

func contentText(content []mcp.Content) string {
	var parts []string
	for _, item := range content {
		if text, ok := item.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
			continue
		}
		data, err := json.Marshal(item)
		if err == nil {
			parts = append(parts, string(data))
		}
	}
	result := strings.Join(parts, "\n")
	if len(result) > maxResultBytes {
		result = result[:maxResultBytes] + "\n[truncated by Agent Kernel]"
	}
	return result
}

func parseNames(value string) []string {
	var names []string
	for _, part := range strings.Split(value, ",") {
		if name := strings.TrimSpace(part); name != "" {
			names = append(names, name)
		}
	}
	return names
}
func nameSet(names []string) map[string]bool {
	result := map[string]bool{}
	for _, name := range names {
		result[name] = true
	}
	return result
}

func validateApprovalNames(allow, approval []string) error {
	allowed := nameSet(allow)
	for _, name := range approval {
		if !allowed[name] {
			return fmt.Errorf("approval-required MCP tool %q must also be in KERNEL_MCP_ALLOW", name)
		}
	}
	return nil
}
