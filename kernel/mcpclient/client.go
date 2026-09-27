// Package mcpclient adapts one configured MCP stdio server to Kernel tools.
// Tools must be explicitly allowlisted; approval-required tools are exposed to
// the model but only execute after a durable human approval signal.
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

type Client struct {
	command string
	args    []string
	tools   map[string]string // model-facing name -> MCP server name
	defs    []llm.ToolDef
	approve map[string]bool
}

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
	allowed := nameSet(allow)
	approve := nameSet(approveNames)
	c := &Client{command: command, args: args, tools: map[string]string{}, approve: map[string]bool{}}
	for name := range approve {
		c.approve[prefix+name] = true
	}
	session, err := c.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect MCP server: %w", err)
	}
	defer session.Close()
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list MCP tools: %w", err)
	}
	for _, tool := range result.Tools {
		if !allowed[tool.Name] {
			continue
		}
		name := prefix + tool.Name
		if len(name) > 64 {
			return nil, fmt.Errorf("MCP tool name %q exceeds model tool name limit", tool.Name)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("MCP tool %q has unsupported input schema %T", tool.Name, tool.InputSchema)
		}
		c.tools[name] = tool.Name
		c.defs = append(c.defs, llm.ToolDef{Name: name, Description: tool.Description, Parameters: schema})
	}
	for name := range allowed {
		found := false
		for _, actual := range c.tools {
			if actual == name {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("allowlisted MCP tool %q was not advertised by server", name)
		}
	}
	return c, nil
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
	cmd := exec.CommandContext(ctx, c.command, c.args...)
	cmd.Stderr = os.Stderr // surface server panics/errors in kernel logs
	client := mcp.NewClient(&mcp.Implementation{Name: "temporality-agent-kernel", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s %v: %w", c.command, c.args, err)
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
