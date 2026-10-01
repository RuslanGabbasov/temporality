package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
)

// Transport kinds supported by the registry. stdio spawns a local process;
// sse and http connect to a remote MCP endpoint (legacy SSE and Streamable
// HTTP protocol versions respectively).
const (
	TypeStdio = "stdio"
	TypeSSE   = "sse"
	TypeHTTP  = "http"
)

// ServerConfig describes one MCP server. It is persisted by the workspace
// store (DB servers) or derived from environment variables (the legacy
// single-server setup, ID == "").
type ServerConfig struct {
	ID       string            `json:"id"` // "" = legacy env server
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      []string          `json:"env,omitempty"` // KEY=VALUE entries, stdio only
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"` // sse/http only
	Allow    []string          `json:"allowed_tools,omitempty"`
	Approval []string          `json:"approval_tools,omitempty"`
	Enabled  bool              `json:"enabled"`
}

// Validate checks structural constraints before any connection attempt.
func (c ServerConfig) Validate() error {
	switch c.Type {
	case TypeStdio:
		if strings.TrimSpace(c.Command) == "" {
			return errors.New("command is required for stdio servers")
		}
	case TypeSSE, TypeHTTP:
		if strings.TrimSpace(c.URL) == "" {
			return errors.New("url is required for sse/http servers")
		}
	default:
		return fmt.Errorf("unsupported server type %q (stdio, sse or http)", c.Type)
	}
	for _, pair := range c.Env {
		if !strings.Contains(pair, "=") {
			return fmt.Errorf("env entry %q must be KEY=VALUE", pair)
		}
	}
	for _, name := range c.Approval {
		// Empty Allow means "every advertised tool", so containment is only
		// checkable against an explicit allowlist.
		if len(c.Allow) > 0 && !containsName(c.Allow, name) {
			return fmt.Errorf("approval-required tool %q must also be in the allowlist", name)
		}
	}
	return nil
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// ToolInfo is one tool advertised by a server.
type ToolInfo struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	ModelName        string `json:"model_name"`
	RequiresApproval bool   `json:"requires_approval"`
}

// Registry keeps the set of configured MCP servers and dispatches model tool
// calls (mcp__*) to the owning server. The legacy env server registers under
// ID "" and keeps the un-namespaced mcp__<tool> naming so existing runs,
// fixtures and drills are unaffected.
type Registry struct {
	mu      sync.RWMutex
	servers map[string]*Client
}

func NewRegistry() *Registry {
	return &Registry{servers: map[string]*Client{}}
}

// Apply reconciles one server in the registry. Disabled configs are removed.
// For a config equal to the registered one a failed refresh keeps the stale
// tool set (a transient outage must not silently strip tools); a changed
// config that fails to connect removes the previous tools instead.
func (r *Registry) Apply(ctx context.Context, cfg ServerConfig) error {
	if !cfg.Enabled {
		r.Remove(cfg.ID)
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	client := NewClient(cfg)
	if err := client.Refresh(ctx); err != nil {
		r.mu.Lock()
		if previous, ok := r.servers[cfg.ID]; ok && !sameConfig(previous.cfg, cfg) {
			delete(r.servers, cfg.ID)
		}
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	r.servers[cfg.ID] = client
	r.mu.Unlock()
	return nil
}

// Remove drops a server and its tools from the registry.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	delete(r.servers, id)
	r.mu.Unlock()
}

// AdoptLegacy registers the pre-connected env server under ID "".
func (r *Registry) AdoptLegacy(client *Client) {
	if client == nil {
		return
	}
	r.mu.Lock()
	r.servers[""] = client
	r.mu.Unlock()
}

// Discover connects with the given config, lists tools and returns them
// without registering the server. Used by the UI before saving a server.
func (r *Registry) Discover(ctx context.Context, cfg ServerConfig) ([]ToolInfo, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client := NewClient(cfg)
	if err := client.Refresh(ctx); err != nil {
		return nil, err
	}
	return client.Tools(), nil
}

// ToolDefsFor returns the tool definitions for the given server IDs. An empty
// list keeps the previous behaviour: only the legacy env server's tools.
func (r *Registry) ToolDefsFor(ids []string) []llm.ToolDef {
	var defs []llm.ToolDef
	for _, client := range r.resolve(ids) {
		defs = append(defs, client.ToolDefs()...)
	}
	return defs
}

// ApprovalToolsFor returns approval-required tool names for the given servers.
func (r *Registry) ApprovalToolsFor(ids []string) []string {
	var names []string
	for _, client := range r.resolve(ids) {
		names = append(names, client.ApprovalTools()...)
	}
	return names
}

func (r *Registry) resolve(ids []string) []*Client {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(ids) == 0 {
		if legacy, ok := r.servers[""]; ok {
			return []*Client{legacy}
		}
		return nil
	}
	var clients []*Client
	for _, id := range ids {
		if client, ok := r.servers[id]; ok {
			clients = append(clients, client)
		}
	}
	return clients
}

// ServerTools describes every registered server and its tools — the input for
// the agent tools panel in the UI.
func (r *Registry) ServerTools() map[string]ServerToolsInfo {
	result := map[string]ServerToolsInfo{}
	if r == nil {
		return result
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id, client := range r.servers {
		result[id] = ServerToolsInfo{
			ID:    id,
			Name:  client.cfg.Name,
			Type:  client.cfg.Type,
			Tools: client.Tools(),
		}
	}
	return result
}

// ServerToolsInfo is the registry view of one server for the UI.
type ServerToolsInfo struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Type  string     `json:"type"`
	Tools []ToolInfo `json:"tools"`
}

// HasTool reports whether any registered server advertises the model tool.
func (r *Registry) HasTool(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, client := range r.servers {
		if client.HasTool(name) {
			return true
		}
	}
	return false
}

// RequiresApproval reports whether the tool was marked approval-required.
func (r *Registry) RequiresApproval(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, client := range r.servers {
		if client.RequiresApproval(name) {
			return true
		}
	}
	return false
}

// Call dispatches a model tool name to the owning server.
func (r *Registry) Call(ctx context.Context, name string, args map[string]any, idempotencyKey string) (string, error) {
	if r == nil {
		return "", errors.New("MCP is not configured")
	}
	r.mu.RLock()
	client := r.clientForTool(name)
	r.mu.RUnlock()
	if client == nil {
		return "", fmt.Errorf("MCP tool %q is not allowed", name)
	}
	return client.Call(ctx, name, args, idempotencyKey)
}

func (r *Registry) clientForTool(name string) *Client {
	for _, client := range r.servers {
		if client.HasTool(name) {
			return client
		}
	}
	return nil
}

// ServerIDForTool derives the owning server ID from a model tool name:
// mcp__<server>__<tool> → server; mcp__<tool> (legacy) → "".
func ServerIDForTool(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return "", false
	}
	if server, _, found := strings.Cut(rest, "__"); found {
		return server, true
	}
	return "", true
}

func sameConfig(a, b ServerConfig) bool {
	if a.ID != b.ID || a.Name != b.Name || a.Type != b.Type || a.Command != b.Command ||
		a.URL != b.URL || a.Enabled != b.Enabled {
		return false
	}
	if fmt.Sprint(a.Args) != fmt.Sprint(b.Args) || fmt.Sprint(a.Env) != fmt.Sprint(b.Env) ||
		fmt.Sprint(a.Allow) != fmt.Sprint(b.Allow) || fmt.Sprint(a.Approval) != fmt.Sprint(b.Approval) ||
		fmt.Sprint(a.Headers) != fmt.Sprint(b.Headers) {
		return false
	}
	return true
}

// headerClient builds an HTTP client that adds the configured static headers
// to every request (authentication for remote MCP endpoints).
func headerClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return &http.Client{Timeout: 30 * time.Second}
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: headerTransport{headers: headers, base: http.DefaultTransport}}
}

type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for key, value := range t.headers {
		clone.Header.Set(key, value)
	}
	return t.base.RoundTrip(clone)
}
