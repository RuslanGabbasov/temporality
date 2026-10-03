// Package controlplane provides the minimal Enterprise Control Plane slice
// shared by the journal and the kernel API: static bearer tokens resolved
// from environment configuration, a small role ladder and project scoping.
//
// Tokens are configured as ;-separated entries
//
//	<token>:<subject>:<role>:<projects>
//
// where role is one of reader, writer, operator, admin and projects is "*"
// or a comma-separated project list. When no tokens are configured the gate
// is disabled and every request passes as an anonymous admin — this keeps
// local development and existing fixtures working. Deployment for a team is
// expected to always configure tokens.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Role is the permission ladder. Higher roles include all lower rights.
type Role int

const (
	RoleNone Role = iota
	RoleReader
	RoleWriter
	RoleOperator
	RoleAdmin
)

func ParseRole(raw string) (Role, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "reader", "viewer": // viewer is the workspace Users-page alias for reader
		return RoleReader, nil
	case "writer":
		return RoleWriter, nil
	case "operator":
		return RoleOperator, nil
	case "admin":
		return RoleAdmin, nil
	default:
		return RoleNone, fmt.Errorf("unknown role %q (want reader, writer, operator or admin)", raw)
	}
}

func (r Role) String() string {
	switch r {
	case RoleReader:
		return "reader"
	case RoleWriter:
		return "writer"
	case RoleOperator:
		return "operator"
	case RoleAdmin:
		return "admin"
	default:
		return "none"
	}
}

// Principal is one authenticated identity.
type Principal struct {
	Subject  string
	Role     Role
	Projects []string // contains "*" when the token spans all projects
	// UserID links the principal to its workspace_user record when the token
	// belongs to a database user. Empty for static environment tokens.
	UserID string
	// OrgUnitID is the user's org unit (docs/org-structure.md §3.3); empty for
	// unassigned users and service tokens.
	OrgUnitID string
	// VisibleUnits is the user's ancestor chain (self included) used to filter
	// org-bound resources. nil disables filtering: admins, unassigned users,
	// and service tokens (the runtime must not depend on one person's rights).
	VisibleUnits []string
}

// AllowsProject reports whether the principal may touch the project. The
// empty project (unscoped operational endpoints) is always allowed.
func (p Principal) AllowsProject(project string) bool {
	if project == "" {
		return true
	}
	for _, allowed := range p.Projects {
		if allowed == "*" || allowed == project {
			return true
		}
	}
	return false
}

func (p Principal) AllowsAllProjects() bool {
	for _, allowed := range p.Projects {
		if allowed == "*" {
			return true
		}
	}
	return false
}

// Gate resolves bearer tokens to principals. A nil *Gate (or one created
// from an empty token list) is disabled and authorizes everything.
//
// Static tokens from the environment live in byToken (immutable after
// startup). Tokens of workspace users live in the database and can change
// at runtime, so they are kept in a separate reloadable set guarded by a
// mutex.
type Gate struct {
	byToken   map[string]Principal
	dbMu      sync.RWMutex
	dbByToken map[string]Principal
}

// NewGate parses the token configuration. An empty raw value yields a
// disabled gate and no error.
func NewGate(raw string) (*Gate, error) {
	entries := strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == '\n' })
	if len(entries) == 0 {
		return nil, nil
	}
	gate := &Gate{byToken: make(map[string]Principal, len(entries))}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 4 {
			return nil, fmt.Errorf("auth token entry must be token:subject:role:projects, got %q", entry)
		}
		token := strings.TrimSpace(parts[0])
		subject := strings.TrimSpace(parts[1])
		role, err := ParseRole(parts[2])
		if err != nil {
			return nil, err
		}
		if token == "" || subject == "" {
			return nil, fmt.Errorf("auth token entry needs a token and a subject, got %q", entry)
		}
		projects := []string{}
		for _, project := range strings.Split(parts[3], ",") {
			if project = strings.TrimSpace(project); project != "" {
				projects = append(projects, project)
			}
		}
		if len(projects) == 0 {
			return nil, fmt.Errorf("auth token entry needs at least one project (or *), got %q", entry)
		}
		gate.byToken[token] = Principal{Subject: subject, Role: role, Projects: projects}
	}
	if len(gate.byToken) == 0 {
		return nil, nil
	}
	return gate, nil
}

// Enabled reports whether authentication is active.
func (g *Gate) Enabled() bool { return g != nil && len(g.byToken) > 0 }

// TokenCount is the number of configured tokens (for startup logs).
func (g *Gate) TokenCount() int {
	if g == nil {
		return 0
	}
	return len(g.byToken) + g.DBTokenCount()
}

// SetDBTokens atomically replaces the set of workspace-user tokens. The
// static environment tokens are never affected. A nil gate is disabled
// and ignores the call.
func (g *Gate) SetDBTokens(principals map[string]Principal) {
	if g == nil {
		return
	}
	tokens := make(map[string]Principal, len(principals))
	for token, p := range principals {
		if token = strings.TrimSpace(token); token != "" {
			tokens[token] = p
		}
	}
	g.dbMu.Lock()
	g.dbByToken = tokens
	g.dbMu.Unlock()
}

// DBTokenCount returns the number of live workspace-user tokens.
func (g *Gate) DBTokenCount() int {
	if g == nil {
		return 0
	}
	g.dbMu.RLock()
	defer g.dbMu.RUnlock()
	return len(g.dbByToken)
}

func (g *Gate) dbPrincipal(token string) (Principal, bool) {
	g.dbMu.RLock()
	defer g.dbMu.RUnlock()
	p, ok := g.dbByToken[token]
	return p, ok
}

func (g *Gate) dbTokensSnapshot() map[string]Principal {
	g.dbMu.RLock()
	defer g.dbMu.RUnlock()
	snapshot := make(map[string]Principal, len(g.dbByToken))
	for token, p := range g.dbByToken {
		snapshot[token] = p
	}
	return snapshot
}

type contextKey struct{}

// Authenticate is HTTP middleware. When the gate is enabled it requires a
// valid bearer token on every request except health checks, and stores the
// principal in the request context. A disabled gate passes everything
// through with an anonymous admin principal.
func (g *Gate) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.Enabled() {
			anonymous := Principal{Subject: "anonymous", Role: RoleAdmin, Projects: []string{"*"}}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, anonymous)))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/healthz") {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := BearerToken(r)
		principal, known := g.byToken[token]
		if !known {
			principal, known = g.dbPrincipal(token)
		}
		if !ok || !known {
			writeError(w, http.StatusUnauthorized, "a valid bearer token is required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, principal)))
	})
}

// BearerToken extracts the bearer token from the request.
func BearerToken(r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header != "" {
		if token, ok := strings.CutPrefix(header, "Bearer "); ok {
			return strings.TrimSpace(token), true
		}
	}
	// Also accept ?token= query parameter for SSE / EventSource connections
	// which cannot set custom headers.
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		return token, true
	}
	return "", false
}

// Allow authorizes the request for at least min role over every listed
// project. It writes the 401/403 response itself and returns false when the
// handler must stop. A request that was not authenticated can only mean the
// gate is disabled (the middleware injects an anonymous principal when it
// is), so it is allowed.
func (g *Gate) Allow(w http.ResponseWriter, r *http.Request, min Role, projects ...string) bool {
	principal, authenticated := r.Context().Value(contextKey{}).(Principal)
	if !authenticated {
		if g.Enabled() {
			// Enabled gate but no principal: the handler was mounted without
			// the Authenticate middleware. Fail closed.
			writeError(w, http.StatusUnauthorized, "a valid bearer token is required")
			return false
		}
		return true
	}
	if !g.Enabled() {
		return true
	}
	for _, project := range projects {
		// An unfiltered cross-project query is only valid for tokens that
		// explicitly span all projects.
		if project == "" {
			if !principal.AllowsAllProjects() {
				writeError(w, http.StatusForbidden, fmt.Sprintf("token %q may not query across projects; set a project filter", principal.Subject))
				return false
			}
			continue
		}
		if !principal.AllowsProject(project) {
			writeError(w, http.StatusForbidden, fmt.Sprintf("token %q has no access to project %q", principal.Subject, project))
			return false
		}
	}
	if principal.Role < min {
		writeError(w, http.StatusForbidden, fmt.Sprintf("token %q has role %s, need %s", principal.Subject, principal.Role, min))
		return false
	}
	return true
}

// FromContext returns the authenticated principal when the gate is enabled.
func FromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(contextKey{}).(Principal)
	return principal, ok && principal.Subject != "anonymous"
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// LoadFileSecrets resolves the <name>_FILE convention into the plain env
// vars before any configuration reads them: when name itself is unset and
// <name>_FILE points at a readable file, the file's trimmed content becomes
// the value. This is how docker compose secrets (and most orchestration
// systems) hand secrets to a process without putting them in the
// environment. A plain env value always wins; an unreadable file is an
// error, not a silent skip.
func LoadFileSecrets(names ...string) error {
	for _, name := range names {
		if os.Getenv(name) != "" {
			continue
		}
		path := strings.TrimSpace(os.Getenv(name + "_FILE"))
		if path == "" {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read secret file for %s: %w", name, err)
		}
		value := strings.TrimSpace(string(content))
		if value == "" {
			return fmt.Errorf("secret file for %s is empty", name)
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set %s from secret file: %w", name, err)
		}
	}
	return nil
}

// Merge returns a new Gate that accepts tokens from both g and other.
// If either gate is nil or disabled, the other is returned as-is.
func (g *Gate) Merge(other *Gate) *Gate {
	if !g.Enabled() && !other.Enabled() {
		return nil
	}
	if !g.Enabled() {
		return other
	}
	if !other.Enabled() {
		return g
	}
	merged := &Gate{byToken: make(map[string]Principal)}
	for token, p := range g.byToken {
		merged.byToken[token] = p
	}
	for token, p := range other.byToken {
		merged.byToken[token] = p
	}
	// Preserve the reloadable database tokens of the receiver: Merge builds
	// a fresh gate and must not silently drop them.
	merged.SetDBTokens(g.dbTokensSnapshot())
	return merged
}
