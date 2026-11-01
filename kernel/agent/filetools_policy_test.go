package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/workspace"
)

// The kernel filesystem gateway (docs/filesystem-policy.md §5.2): built-in
// file tools consult the run's frozen policy before every operation — the
// same snapshot the sandbox enforcement receives (§5.4).
func TestFileToolsEnforceFilesystemPolicy(t *testing.T) {
	activities, ws := newFileToolActivities(t)
	if err := os.WriteFile(filepath.Join(ws, "keep.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := &workspace.EffectiveFilesystem{
		Read:  [][]string{{"docs/**"}},
		Write: [][]string{{"docs/reports/**"}},
		Deny:  []string{"docs/secrets/**"},
	}
	request := fileToolRequest("read_file", ws, false, map[string]any{"path": "docs/a.md"})
	request.Filesystem = policy
	request.PolicyVersion = "fs-test"

	cases := []struct {
		name   string
		path   string
		op     fsOp
		denied bool
	}{
		{"read inside the read area", "docs/a.md", fsOpRead, false},
		{"read outside the read area", "keep.md", fsOpRead, true},
		{"write inside the write area", "docs/reports/r.md", fsOpWrite, false},
		{"write inside read but outside write", "docs/a.md", fsOpWrite, true},
		{"deny beats the read area", "docs/secrets/key.pem", fsOpRead, true},
		{"deny beats the write area", "docs/secrets/key.pem", fsOpWrite, true},
		{"write requires read (W⊆R)", "docs/reports/x/../r.md", fsOpWrite, false},
	}
	for _, c := range cases {
		_, err := activities.resolveToolTarget(request, c.path, c.op)
		if c.denied {
			if err == nil {
				t.Errorf("%s: expected denial", c.name)
				continue
			}
			kind, message := nonRetryableType(t, err)
			if kind != "FilesystemDenied" {
				t.Errorf("%s: error type = %q, want FilesystemDenied (%s)", c.name, kind, message)
			}
			if !strings.Contains(message, "policy fs-test") {
				t.Errorf("%s: denial must name the policy version, got %q", c.name, message)
			}
		} else if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}
	}

	// Escape attempts keep failing as escapes — the policy gate must not
	// shadow the containment contract.
	if _, err := activities.resolveToolTarget(request, "../outside.md", fsOpRead); err == nil {
		t.Error("path escape must fail")
	}

	// An unrestricted snapshot (nil rules) keeps the pre-filesystem behavior.
	open := fileToolRequest("read_file", ws, false, map[string]any{"path": "keep.md"})
	if _, err := activities.resolveToolTarget(open, "keep.md", fsOpRead); err != nil {
		t.Errorf("unrestricted run must read freely: %v", err)
	}

	// /scratch is run-scoped technical space (§4.3): outside the policy.
	scratchReq := fileToolRequest("write_file", ws, false, map[string]any{"path": "/scratch/x"})
	scratchReq.Filesystem = policy
	scratchReq.RunID = "run-scratch"
	if _, err := activities.resolveToolTarget(scratchReq, "/scratch/x", fsOpWrite); err != nil {
		t.Errorf("scratch must stay policy-free: %v", err)
	}
}

// End-to-end through the handler: a denied write_file fails before touching
// the filesystem, with the FilesystemDenied type the UI/audit surfaces.
func TestWriteFileDeniedByPolicy(t *testing.T) {
	activities, ws := newFileToolActivities(t)
	request := fileToolRequest("write_file", ws, false, map[string]any{
		"path":    "deploy/production/app.yaml",
		"content": "boom",
	})
	request.Filesystem = &workspace.EffectiveFilesystem{Write: [][]string{{"src/**"}}}
	_, err := activities.handleWriteFile(request)
	if err == nil {
		t.Fatal("expected the policy to deny the write")
	}
	kind, message := nonRetryableType(t, err)
	if kind != "FilesystemDenied" {
		t.Fatalf("error type = %q, want FilesystemDenied (%s)", kind, message)
	}
	if _, statErr := os.Stat(filepath.Join(ws, "deploy")); !os.IsNotExist(statErr) {
		t.Error("the denied write must not create anything")
	}
}
