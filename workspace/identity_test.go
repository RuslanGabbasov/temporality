package workspace

import (
	"strings"
	"testing"
)

// IdentityAllows is the gate every automated run passes at fire time
// (docs/org-structure.md §21): each dimension must be explicitly permitted,
// and revoking an entry must stop future fires.
func TestIdentityAllows(t *testing.T) {
	base := func(mutate func(*ExecutionIdentity)) ExecutionIdentity {
		id := ExecutionIdentity{
			ID:               "ci-bot",
			AllowedAgents:    []string{"coder", "reviewer"},
			AllowedProjects:  []string{"temporality"},
			AllowedMCP:       []string{"github"},
			AllowedProviders: []string{"anthropic"},
		}
		if mutate != nil {
			mutate(&id)
		}
		return id
	}

	tests := []struct {
		name      string
		identity  ExecutionIdentity
		agentID   string
		projectID string
		mcp       []string
		provider  string
		wantErr   string // empty means success
	}{
		{
			name:      "all dimensions permitted",
			identity:  base(nil),
			agentID:   "coder",
			projectID: "temporality",
			mcp:       []string{"github"},
			provider:  "anthropic",
		},
		{
			name:      "agent not in allowed list",
			identity:  base(nil),
			agentID:   "qa",
			projectID: "temporality",
			mcp:       []string{"github"},
			provider:  "anthropic",
			wantErr:   `does not allow agent "qa"`,
		},
		{
			name:      "project not in allowed list",
			identity:  base(nil),
			agentID:   "coder",
			projectID: "lighthouse",
			mcp:       []string{"github"},
			provider:  "anthropic",
			wantErr:   `does not allow project "lighthouse"`,
		},
		{
			name:      "one of several mcp servers denied",
			identity:  base(nil),
			agentID:   "coder",
			projectID: "temporality",
			mcp:       []string{"github", "graphmap"},
			provider:  "anthropic",
			wantErr:   `does not allow MCP server "graphmap"`,
		},
		{
			name:      "provider not in allowed list",
			identity:  base(nil),
			agentID:   "coder",
			projectID: "temporality",
			mcp:       []string{"github"},
			provider:  "openai",
			wantErr:   `does not allow provider "openai"`,
		},
		{
			name: "wildcard allows anything",
			identity: base(func(e *ExecutionIdentity) {
				e.AllowedAgents = []string{"*"}
				e.AllowedProjects = []string{"*"}
				e.AllowedMCP = []string{"*"}
				e.AllowedProviders = []string{"*"}
			}),
			agentID:   "qa",
			projectID: "lighthouse",
			mcp:       []string{"anything", "else"},
			provider:  "openai",
		},
		{
			// Unknown-at-save-time dimensions are skipped, not denied: the
			// check runs again at fire time once the value is known.
			name:      "empty dimension is skipped",
			identity:  base(nil),
			agentID:   "coder",
			projectID: "",
			mcp:       nil,
			provider:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := IdentityAllows(tt.identity, tt.agentID, tt.projectID, tt.mcp, tt.provider)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got success", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

// allowsAny is the single-entry predicate behind IdentityAllows: "*" permits
// everything, everything else is an exact match.
func TestAllowsAny(t *testing.T) {
	if !allowsAny([]string{"*"}, "whatever") {
		t.Error(`"*" should allow any value`)
	}
	if !allowsAny([]string{"a", "b"}, "b") {
		t.Error("exact member should be allowed")
	}
	if allowsAny([]string{"a", "b"}, "c") {
		t.Error("non-member should be denied")
	}
	if allowsAny(nil, "a") {
		t.Error("empty list should deny (normalized identities use * instead)")
	}
}
