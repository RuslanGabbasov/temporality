package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/temporality-project/temporality/workspace"
)

// Filesystem policy inheritance (docs/filesystem-policy.md §6.2): a delegated
// child run works on the initiator's FROZEN snapshot — whatever the run-config
// endpoint re-resolved loses to it. Delegation must never widen (or silently
// re-narrow) the file permissions of the running tree.
func TestResolveAgentInheritsFilesystemSnapshot(t *testing.T) {
	// The child's freshly resolved policy — LOOSER than the parent snapshot
	// (write everywhere). If inheritance is broken, the assertions below see
	// this value instead of the parent's.
	resolved := workspace.EffectiveFilesystem{Write: [][]string{{"**"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":          "mimo-x",
			"filesystem":     resolved,
			"policy_version": "fs-resolved",
		})
	}))
	defer server.Close()

	activities := &Activities{HTTP: &http.Client{}, WorkspaceURL: server.URL}

	parentSnapshot := &workspace.EffectiveFilesystem{
		Write: [][]string{{"src/**"}},
		Deny:  []string{"secrets/**"},
	}
	child, err := activities.ResolveAgent(context.Background(), ResolveAgentRequest{
		Project: "proj-1", AgentID: "coder", RunID: "run/child", Prompt: "do things",
		ActorID: "user-1", DelegationDepth: 1,
		Filesystem: parentSnapshot, PolicyVersion: "fs-parent",
	})
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if !reflect.DeepEqual(child.Filesystem, parentSnapshot) {
		t.Fatalf("child filesystem = %+v, want the parent snapshot %+v", child.Filesystem, parentSnapshot)
	}
	if child.PolicyVersion != "fs-parent" {
		t.Fatalf("child policy version = %q, want fs-parent", child.PolicyVersion)
	}
	// The inherited snapshot must not grant what it does not cover.
	if child.Filesystem.AllowWrite("docs/x.md") {
		t.Error("inherited snapshot must not allow docs/x.md")
	}
	if !child.Filesystem.AllowWrite("src/a.go") {
		t.Error("inherited snapshot must allow src/a.go")
	}

	// Legacy parents (workflows replaying from before the snapshot existed)
	// carry no snapshot: the child keeps its freshly resolved policy.
	legacy, err := activities.ResolveAgent(context.Background(), ResolveAgentRequest{
		Project: "proj-1", AgentID: "coder", RunID: "run/legacy", Prompt: "do things",
		ActorID: "user-1", DelegationDepth: 1,
	})
	if err != nil {
		t.Fatalf("ResolveAgent (legacy): %v", err)
	}
	if !reflect.DeepEqual(legacy.Filesystem, &resolved) {
		t.Fatalf("legacy child filesystem = %+v, want the resolved %+v", legacy.Filesystem, resolved)
	}
	if legacy.PolicyVersion != "fs-resolved" {
		t.Fatalf("legacy child policy version = %q, want fs-resolved", legacy.PolicyVersion)
	}
}
