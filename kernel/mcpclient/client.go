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
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/temporality-project/temporality/kernel/llm"
)

const prefix = "mcp__"
const maxResultBytes = 64 << 10

// Client adapts one MCP server. Sessions are persistent: Call and Refresh
// reuse one connected session and only redial when the transport is detected
// broken, so a stdio server is not respawned (and remote endpoints do not redo
// TCP+TLS) on every tool call. The stdio process living between calls is the
// intended MCP lifecycle — if the server dies, the first Call detects the dead
// transport, redials once and retries the call once. Close tears the session
// down explicitly, but a Client may simply live for the process lifetime.
type Client struct {
	cfg     ServerConfig
	tools   map[string]string // model-facing name -> bare MCP tool name
	defs    []llm.ToolDef
	approve map[string]bool

	// mu guards the persistent session and its lazy dependencies. The RPCs
	// themselves (CallTool, ListTools) run OUTSIDE mu: per go-sdk mcp v1.8.0
	// a ClientSession is safe for concurrent use — jsonrpc2.Connection.Call
	// allocates request IDs with an atomic counter and tracks in-flight calls
	// under a mutex (internal/jsonrpc2/conn.go), and the stdio transport
	// serializes frame writes under ioConn.writeMu ("guards Write, which must
	// be concurrency safe", mcp/transport.go). Only ensureSession/redial —
	// i.e. connect and teardown — are serialized.
	mu      sync.Mutex
	session *mcp.ClientSession
	http    *http.Client // shared keep-alive client for sse/http transports

	// connectFn is the transport seam used by tests to stand up in-memory
	// servers; production clients always use (*Client).connect.
	connectFn func(ctx context.Context) (*mcp.ClientSession, error)
}

// NewClient builds an unconnected client. Call Refresh to discover tools.
func NewClient(cfg ServerConfig) *Client {
	c := &Client{
		cfg:     cfg,
		tools:   map[string]string{},
		approve: map[string]bool{},
	}
	c.connectFn = c.connect
	return c
}

// ensureSession returns the persistent session, connecting lazily on first use
// and after a redial. The connection outlives any single call, so it is
// detached from the caller's cancellation: a per-request context must not kill
// the shared stdio server process (exec.CommandContext) or remote stream. A
// genuinely dead server is recovered by redial on the next Call.
func (c *Client) ensureSession(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		return c.session, nil
	}
	session, err := c.connectFn(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	c.session = session
	return session, nil
}

// redial replaces a session whose transport was detected broken. If another
// goroutine already reconnected, its fresh session is reused instead of
// dialing again — and never closed by the loser of the race.
func (c *Client) redial(ctx context.Context, dead *mcp.ClientSession) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil && c.session != dead {
		return c.session, nil
	}
	if dead != nil {
		_ = dead.Close()
	}
	c.session = nil
	session, err := c.connectFn(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	c.session = session
	return session, nil
}

// Close tears down the persistent session, if any. Idempotent, and nobody is
// required to call it: Clients typically live as long as the process.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return nil
	}
	session := c.session
	c.session = nil
	return session.Close()
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

// Refresh lists tools over the persistent session (redialing once if the
// transport broke since the last use) and rebuilds the allowlisted tool set.
// The session is left alive for subsequent calls.
func (c *Client) Refresh(ctx context.Context) error {
	session, err := c.ensureSession(ctx)
	if err != nil {
		return err
	}
	result, err := session.ListTools(ctx, nil)
	if isBrokenTransport(err) {
		session, err = c.redial(ctx, session)
		if err == nil {
			result, err = session.ListTools(ctx, nil)
		}
	}
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
	session, err := c.ensureSession(ctx)
	if err != nil {
		return "", err
	}
	result, err := callTool(ctx, session, actual, args, idempotencyKey)
	if isBrokenTransport(err) {
		// The session died between calls (server restart, idle teardown,
		// network loss). Redial once and retry the call exactly once; the
		// idempotency key lets servers that track it deduplicate an operation
		// executed before the transport broke. A second failure is returned
		// as is.
		session, err = c.redial(ctx, session)
		if err == nil {
			result, err = callTool(ctx, session, actual, args, idempotencyKey)
		}
	}
	if err != nil {
		return "", err
	}
	if result.IsError {
		return "", errors.New(contentText(result.Content))
	}
	return contentText(result.Content), nil
}

func callTool(ctx context.Context, session *mcp.ClientSession, name string, args map[string]any, idempotencyKey string) (*mcp.CallToolResult, error) {
	params := &mcp.CallToolParams{Name: name, Arguments: args}
	if idempotencyKey != "" {
		params.Meta = mcp.Meta{"idempotency_key": idempotencyKey}
	}
	return session.CallTool(ctx, params)
}

// isBrokenTransport reports whether err means the session's transport is dead
// and a redial can help: closed connections, EOF from a exited peer, websocket
// close frames. Context cancellation and deadlines are not transport damage —
// the session may still be perfectly healthy. mcp.ErrConnectionClosed is the
// go-sdk v1.8.0 sentinel for calls on a connection the peer (or we) closed;
// the textual markers cover transports whose dead-connection errors are not
// wrapped in that sentinel.
func isBrokenTransport(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, mcp.ErrConnectionClosed) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, os.ErrClosed) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection closed",
		"session closed",
		"use of closed network connection",
		"broken pipe",
		"connection reset",
		"websocket: close",
		"client is closing", // go-sdk jsonrpc2 text for a broken/closed connection
		"server is closing",
		"eof",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// sharedHTTPClient returns the one keep-alive HTTP client for this server, so
// TCP connections and TLS sessions survive reconnects. Only called with c.mu
// held (from connect, i.e. ensureSession/redial).
func (c *Client) sharedHTTPClient() *http.Client {
	if c.http == nil {
		c.http = headerClient(&http.Client{Timeout: 30 * time.Second}, c.cfg.Headers)
	}
	return c.http
}

func (c *Client) connect(ctx context.Context) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "temporality-agent-kernel", Version: "0.1.0"}, nil)
	var transport mcp.Transport
	switch c.cfg.Type {
	case TypeSSE:
		transport = &mcp.SSEClientTransport{Endpoint: c.cfg.URL, HTTPClient: c.sharedHTTPClient()}
	case TypeHTTP:
		transport = &mcp.StreamableClientTransport{Endpoint: c.cfg.URL, HTTPClient: c.sharedHTTPClient()}
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
		// A legacy SSE client against a Streamable HTTP server either waits
		// for an endpoint event that never comes or dies mid-handshake — both
		// surface as cryptic EOF. Name the likely mismatch so the operator can
		// fix the type.
		if c.cfg.Type == TypeSSE && strings.Contains(err.Error(), "EOF") {
			return nil, fmt.Errorf("connect MCP server %q: %w — endpoint did not behave as a legacy SSE server; if it speaks Streamable HTTP, set the server type to http", c.cfg.Name, err)
		}
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
