package agent

import (
	"context"
	"errors"
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
	canary    bool
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
// errDisabledSecret marks an operator-DISABLED secret (the deliberate
// «run without it» switch): distinct from a grant denial (404/403 = not
// granted / not visible), because the two must behave differently — a
// disabled secret skips injection with a visible note and the run continues,
// while a grant problem stops the tool call (the agent is asking for
// something that was never granted to it).
var errDisabledSecret = fmt.Errorf("secret is disabled by an operator")

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
	if resp.StatusCode == http.StatusConflict {
		// The workspace's disabled-secret answer (deliberate switch): the
		// typed signal — not a denial, not a transport failure.
		return "", false, errDisabledSecret
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return "", true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("resolve secret %q: HTTP %d: %s", name, resp.StatusCode, truncate(string(body), 200))
	}
	var payload struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Canary bool   `json:"canary"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false, fmt.Errorf("resolve secret %q: %w", name, err)
	}
	if payload.Value == "" {
		return "", true, nil
	}
	b.store(name, payload.Value, payload.Canary)
	return payload.Value, false, nil
}

// CachedCanary is one cached canary secret: name and value.
type CachedCanary struct {
	Name  string
	Value string
}

// CachedCanaries returns the canary secrets currently cached (i.e. resolved
// at least once). Planting uses them to seed honeypot files.
func (b *SecretBroker) CachedCanaries() []CachedCanary {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []CachedCanary
	for name, entry := range b.values {
		if entry.canary && time.Now().Before(entry.expiresAt) {
			out = append(out, CachedCanary{Name: name, Value: entry.value})
		}
	}
	return out
}

// Canary reports whether the named secret is a honeypot credential. Unknown
// names are not canaries.
func (b *SecretBroker) Canary(name string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.values[name].canary
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

func (b *SecretBroker) store(name, value string, canary bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.values == nil {
		b.values = make(map[string]secretCacheEntry)
	}
	b.values[name] = secretCacheEntry{value: value, canary: canary, expiresAt: time.Now().Add(b.ttl())}
}

// SecretEnvVarName converts a secret name into the environment variable the
// sandbox receives: SECRET_<UPPERCASE_NAME>, every character outside
// [A-Za-z0-9_] becoming an underscore (docs/secrets-management.md §8). A
// workspace name like "azure devops project hrpro" must still inject — a
// raw space in the key used to fail docker --env and kill the whole run.
func SecretEnvVarName(name string) string {
	replaced := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, name)
	return "SECRET_" + strings.ToUpper(replaced)
}

// sandboxSecretEnv resolves the agent's granted secrets into KEY=VALUE env
// entries. It returns the entries and the names that were denied so the caller
// can record secret.denied events; transport failures are hard errors.
// secretsForScan carries the same values as behaviorSecret descriptors for the
// behavior guard (docs/agent-behavior-monitoring.md): the scan needs the
// values it must recognize in arguments and output.
// skipped reports names whose sanitized env key collides with an earlier
// granted secret ("a-b" and "a_b" both map to SECRET_A_B): the first name
// wins deterministically, the rest are left out of the environment — a soft,
// model-visible condition instead of a run-killing docker error.
// disabled reports operator-switched-off secrets: they skip injection with a
// visible note and the run continues — a deliberate switch must not kill
// every run that references the secret (the old behavior).
func (a *Activities) sandboxSecretEnv(ctx context.Context, request ToolRequest) (env []string, denied []string, err error, secretsForScan []behaviorSecret, skipped []string, disabled []string) {
	if len(request.Secrets) == 0 || a.Secrets == nil {
		return nil, nil, nil, nil, nil, nil
	}
	env = make([]string, 0, len(request.Secrets))
	denied = make([]string, 0)
	skipped = make([]string, 0)
	disabled = make([]string, 0)
	resolved := make([]string, 0, len(request.Secrets))
	usedKeys := make(map[string]string, len(request.Secrets))
	for _, name := range request.Secrets {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		value, deniedFlag, err := a.Secrets.Resolve(ctx, name)
		if err != nil {
			if errors.Is(err, errDisabledSecret) {
				// An operator switched the secret off: the run continues without
				// the entry, the model is told, the denial is journaled. Not a
				// grant problem — never worth killing the call over.
				disabled = append(disabled, name)
				continue
			}
			return nil, nil, fmt.Errorf("resolve secret %q: %w", name, err), nil, nil, nil
		}
		if deniedFlag {
			denied = append(denied, name)
			continue
		}
		key := SecretEnvVarName(name)
		if _, taken := usedKeys[key]; taken {
			skipped = append(skipped, name)
			continue
		}
		usedKeys[key] = name
		env = append(env, key+"="+value)
		resolved = append(resolved, name)
		secretsForScan = append(secretsForScan, newBehaviorSecret(name, value, a.Secrets.Canary(name)))
	}
	a.emitSecretIssued(ctx, request, resolved, append(denied, disabled...))
	return env, denied, nil, secretsForScan, skipped, disabled
}

// resolveRunSecrets resolves granted secrets for scanning only — no env, no
// secret.issued emission (the file tools do not consume secrets; the scan
// just needs to recognize their values inside payloads).
func (a *Activities) resolveRunSecrets(ctx context.Context, request ToolRequest) []behaviorSecret {
	if len(request.Secrets) == 0 || a.Secrets == nil {
		return nil
	}
	out := make([]behaviorSecret, 0, len(request.Secrets))
	for _, name := range request.Secrets {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		value, denied, err := a.Secrets.Resolve(ctx, name)
		if err != nil || denied || value == "" {
			continue
		}
		out = append(out, newBehaviorSecret(name, value, a.Secrets.Canary(name)))
	}
	return out
}

// emitSecretIssued records secret.issued for granted resolutions and
// secret.denied for refused ones (docs/secrets-management.md §7). Best-effort:
// observability must not fail the tool call that already has its values.
// Issuance is journaled once per run per secret — the grant happens at the
// first command that needs it; later commands reuse the same access and
// re-emitting per call turns a six-command run into six "issued" rows.
func (a *Activities) emitSecretIssued(ctx context.Context, request ToolRequest, resolved, denied []string) {
	if a.Events == nil || (len(resolved) == 0 && len(denied) == 0) {
		return
	}
	now := time.Now().UTC()
	for _, name := range resolved {
		if !a.claimSecretIssued(request.RunID, name) {
			continue
		}
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

// secretIssuedWindow bounds how long a run's issuance claim lives. Runs are
// hours long at most (the approval ceiling is a day); after the window the
// pair can emit again — an honest re-issuance for a run that outlived it.
const secretIssuedWindow = 24 * time.Hour

// claimSecretIssued marks run+secret as journaled and reports whether this
// call is the first claim for the pair. The map lives for the worker's
// lifetime; a lazy sweep on overflow keeps it bounded.
func (a *Activities) claimSecretIssued(runID, name string) bool {
	key := runID + "\x00" + name
	a.secretIssuedMu.Lock()
	defer a.secretIssuedMu.Unlock()
	now := time.Now()
	if a.secretIssued == nil {
		a.secretIssued = make(map[string]time.Time)
	}
	if issuedAt, ok := a.secretIssued[key]; ok && now.Sub(issuedAt) < secretIssuedWindow {
		return false
	}
	if len(a.secretIssued) >= 4096 {
		for k, at := range a.secretIssued {
			if now.Sub(at) >= secretIssuedWindow {
				delete(a.secretIssued, k)
			}
		}
	}
	a.secretIssued[key] = now
	return true
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
