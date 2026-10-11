package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"io"

	"github.com/temporality-project/temporality/observation"
)

// SecretBroker resolves secret names into plaintext values at the execution
// boundary (docs/secrets-management.md §4). It talks to the kernel's own
// workspace API, whose resolve endpoint is service-principal-only, and keeps a
// short-lived in-memory cache so repeated tool calls in one run do not hammer
// the database. Values never leave this struct: callers inject them straight
// into the sandbox environment or MCP process config.
type SecretBroker struct {
	HTTP           HTTPDoer
	WorkspaceURL   string
	WorkspaceToken string
	// APIToken is the fallback credential when no internal token is set.
	APIToken string
	// TTL bounds how long a resolved value stays cached; a shorter window
	// picks up rotations faster at the cost of database reads. Zero means 60s.
	TTL time.Duration

	mu     sync.Mutex
	values map[string]secretCacheEntry
}

type secretCacheEntry struct {
	value     string
	expiresAt time.Time
}

// HTTPDoer decouples the broker from *http.Client in tests.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

func (b *SecretBroker) ttl() time.Duration {
	if b.TTL > 0 {
		return b.TTL
	}
	return time.Minute
}

// Resolve returns the plaintext value of a secret by name. denied=true marks a
// secret the caller is not granted (missing, org-invisible or provider
// failure): the caller reports it as a denial, never as a hard error, so the
// model sees an actionable message instead of a crashed tool call.
func (b *SecretBroker) Resolve(ctx context.Context, name string) (value string, denied bool, err error) {
	if b == nil || b.HTTP == nil || b.WorkspaceURL == "" {
		return "", true, fmt.Errorf("secret broker is not configured")
	}
	if cached, ok := b.cached(name); ok {
		return cached, false, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/secrets/%s/resolve", b.WorkspaceURL, name), nil)
	if err != nil {
		return "", false, err
	}
	token := b.WorkspaceToken
	if token == "" {
		token = b.APIToken
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return "", true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("resolve secret %q: HTTP %d: %s", name, resp.StatusCode, truncate(string(body), 200))
	}
	var payload struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false, fmt.Errorf("resolve secret %q: %w", name, err)
	}
	if payload.Value == "" {
		return "", true, nil
	}
	b.store(name, payload.Value)
	return payload.Value, false, nil
}

func (b *SecretBroker) cached(name string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.values[name]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.value, true
}

func (b *SecretBroker) store(name, value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.values == nil {
		b.values = make(map[string]secretCacheEntry)
	}
	b.values[name] = secretCacheEntry{value: value, expiresAt: time.Now().Add(b.ttl())}
}

// SecretEnvVarName converts a secret name into the environment variable the
// sandbox receives: SECRET_<UPPERCASE_NAME> with dashes and dots becoming
// underscores (docs/secrets-management.md §8).
func SecretEnvVarName(name string) string {
	replaced := strings.Map(func(r rune) rune {
		if r == '-' || r == '.' {
			return '_'
		}
		return r
	}, name)
	return "SECRET_" + strings.ToUpper(replaced)
}

// sandboxSecretEnv resolves the agent's granted secrets into KEY=VALUE env
// entries. It returns the entries and the names that were denied so the caller
// can record secret.denied events; transport failures are hard errors.
func (a *Activities) sandboxSecretEnv(ctx context.Context, request ToolRequest) ([]string, []string, error) {
	if len(request.Secrets) == 0 || a.Secrets == nil {
		return nil, nil, nil
	}
	env := make([]string, 0, len(request.Secrets))
	denied := make([]string, 0)
	resolved := make([]string, 0, len(request.Secrets))
	for _, name := range request.Secrets {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		value, deniedFlag, err := a.Secrets.Resolve(ctx, name)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve secret %q: %w", name, err)
		}
		if deniedFlag {
			denied = append(denied, name)
			continue
		}
		env = append(env, SecretEnvVarName(name)+"="+value)
		resolved = append(resolved, name)
	}
	a.emitSecretIssued(ctx, request, resolved, denied)
	return env, denied, nil
}

// emitSecretIssued records secret.issued for granted resolutions and
// secret.denied for refused ones (docs/secrets-management.md §7). Best-effort:
// observability must not fail the tool call that already has its values.
func (a *Activities) emitSecretIssued(ctx context.Context, request ToolRequest, resolved, denied []string) {
	if a.Events == nil || (len(resolved) == 0 && len(denied) == 0) {
		return
	}
	now := time.Now().UTC()
	for _, name := range resolved {
		event := observation.Event{
			Schema:     observation.Schema,
			EventID:    fmt.Sprintf("%s/secret-issued/%s/%d", request.RunID, name, now.UnixNano()),
			OccurredAt: now,
			Source:     observation.Source{ID: a.SourceID, Integration: "agent-kernel", Version: "1"},
			Context: observation.Context{
				Project: request.Project, Run: request.RunID,
				Actor: observation.Actor{ID: a.SourceID, Type: "agent"},
			},
			Type: "secret.issued",
			Data: map[string]any{"secret": name, "operation_id": request.OperationID},
		}
		_ = a.Events.Enqueue(ctx, event)
	}
	for _, name := range denied {
		event := observation.Event{
			Schema:     observation.Schema,
			EventID:    fmt.Sprintf("%s/secret-denied/%s/%d", request.RunID, name, now.UnixNano()),
			OccurredAt: now,
			Source:     observation.Source{ID: a.SourceID, Integration: "agent-kernel", Version: "1"},
			Context: observation.Context{
				Project: request.Project, Run: request.RunID,
				Actor: observation.Actor{ID: a.SourceID, Type: "agent"},
			},
			Type: "secret.denied",
			Data: map[string]any{"secret": name, "operation_id": request.OperationID, "reason": "not granted or not visible"},
		}
		_ = a.Events.Enqueue(ctx, event)
	}
}

// handleSecretList is the read-only secret discovery tool: names and
// descriptions of the secrets granted to this agent. Values are never listed
// (docs/secrets-management.md §8).
func (a *Activities) handleSecretList(ctx context.Context, request ToolRequest) (ToolResult, error) {
	body, status, err := a.workspaceDo(ctx, http.MethodGet, a.WorkspaceURL+"/v1/workspace/secrets", nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: list secrets failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	var payload struct {
		Secrets []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Provider    string `json:"provider"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return ToolResult{Content: "error: malformed secrets payload"}, nil
	}
	granted := make(map[string]bool, len(request.Secrets))
	for _, name := range request.Secrets {
		granted[strings.TrimSpace(name)] = true
	}
	if len(granted) == 0 {
		return ToolResult{Content: "No secrets are granted to this agent. Ask an administrator to grant them on the agent card."}, nil
	}
	var out strings.Builder
	out.WriteString("Secrets granted to this agent (values are injected automatically as SECRET_<NAME> env vars in run_command, never shown here):\n")
	for _, secret := range payload.Secrets {
		if !granted[secret.Name] {
			continue
		}
		out.WriteString("- " + secret.Name)
		if secret.Description != "" {
			out.WriteString(" — " + secret.Description)
		}
		out.WriteString("\n")
	}
	return ToolResult{Content: out.String()}, nil
}
