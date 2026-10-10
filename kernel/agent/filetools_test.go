package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/sandbox"
	"go.temporal.io/sdk/temporal"
)

// fileToolSandbox fakes the runner for host-side file tools: ResolveWorkspace
// mirrors the real containment contract against a temp root.
type fileToolSandbox struct{ root string }

func (f *fileToolSandbox) SandboxRoot() string { return f.root }
func (f *fileToolSandbox) ResolveWorkspace(path string) (string, error) {
	if path == "" {
		return "", errors.New("workspace is required")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if resolved != f.root && !strings.HasPrefix(resolved, f.root+string(filepath.Separator)) {
		return "", errors.New("workspace must be within the sandbox root")
	}
	return resolved, nil
}

func (f *fileToolSandbox) Execute(context.Context, sandbox.Request) (sandbox.Result, error) {
	return sandbox.Result{}, nil
}

func newFileToolActivities(t *testing.T) (*Activities, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "projects", "demo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Activities{Sandbox: &fileToolSandbox{root: root}}, workspace
}

func fileToolRequest(name, workspace string, readOnly bool, args map[string]any) ToolRequest {
	return ToolRequest{RunID: "run-1", OperationID: "frame-1/call-1", Name: name, WorkspacePath: workspace, Arguments: args, ReadOnly: readOnly}
}

func nonRetryableType(t *testing.T, err error) (string, string) {
	t.Helper()
	var application *temporal.ApplicationError
	if !errors.As(err, &application) {
		t.Fatalf("expected a non-retryable application error, got %T: %v", err, err)
	}
	if !application.NonRetryable() {
		t.Fatal("file tool argument errors must be non-retryable")
	}
	return application.Type(), application.Message()
}

func TestFileToolsWriteReadEdit(t *testing.T) {
	activities, workspace := newFileToolActivities(t)

	write, err := activities.RunTool(context.Background(), fileToolRequest("write_file", workspace, false, map[string]any{
		"path": "pkg/deep/nested/file.txt", "content": "alpha\nbeta\ngamma\n",
	}))
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !strings.Contains(write.Content, "wrote") {
		t.Fatalf("unexpected write result: %q", write.Content)
	}

	read, err := activities.RunTool(context.Background(), fileToolRequest("read_file", workspace, false, map[string]any{
		"path": "/workspace/pkg/deep/nested/file.txt",
	}))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if read.Content != "alpha\nbeta\ngamma\n" {
		t.Fatalf("unexpected read result: %q", read.Content)
	}

	edit, err := activities.RunTool(context.Background(), fileToolRequest("edit_file", workspace, false, map[string]any{
		"path": "pkg/deep/nested/file.txt", "old_text": "beta", "new_text": "BETA + line",
	}))
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if !strings.Contains(edit.Content, "1 occurrence") {
		t.Fatalf("unexpected edit result: %q", edit.Content)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "pkg/deep/nested/file.txt"))
	if err != nil || string(data) != "alpha\nBETA + line\ngamma\n" {
		t.Fatalf("file after edit = %q, %v", data, err)
	}
}

func TestFileToolsEditRejectsMissingAndAmbiguous(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	if _, err := activities.RunTool(context.Background(), fileToolRequest("edit_file", workspace, false, map[string]any{
		"path": "notes.md", "old_text": "x", "new_text": "y",
	})); err == nil {
		t.Fatal("edit of a missing file must fail")
	}
	if _, err := activities.RunTool(context.Background(), fileToolRequest("write_file", workspace, false, map[string]any{
		"path": "notes.md", "content": "a\nb\na\n", // duplicate fragment
	})); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	_, err := activities.RunTool(context.Background(), fileToolRequest("edit_file", workspace, false, map[string]any{
		"path": "notes.md", "old_text": "a", "new_text": "c",
	}))
	kind, message := nonRetryableType(t, err)
	if kind != "InvalidToolArguments" || !strings.Contains(message, "2 times") {
		t.Fatalf("ambiguous edit must explain occurrences, got %s: %s", kind, message)
	}
	// The failed edit must not have changed the file.
	data, _ := os.ReadFile(filepath.Join(workspace, "notes.md"))
	if string(data) != "a\nb\na\n" {
		t.Fatalf("failed edit changed the file: %q", data)
	}
}

func TestFileToolsReadOnlyRunsCannotWrite(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	for _, request := range []ToolRequest{
		fileToolRequest("write_file", workspace, true, map[string]any{"path": "ro.txt", "content": "x"}),
		{RunID: "r", OperationID: "o", Role: "reviewer", WorkspacePath: workspace, Arguments: map[string]any{"path": "ro.txt", "content": "x"}},
		{RunID: "r", OperationID: "o", Role: "qa", WorkspacePath: workspace, Arguments: map[string]any{"path": "ro.txt", "content": "x"}},
	} {
		if _, err := activities.RunTool(context.Background(), request); err == nil {
			t.Fatalf("write into a read-only run (role=%s) must fail", request.Role)
		}
	}
	// Reading stays available in read-only runs.
	if _, err := activities.RunTool(context.Background(), fileToolRequest("write_file", workspace, false, map[string]any{
		"path": "notes.md", "content": "hello",
	})); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	read, err := activities.RunTool(context.Background(), fileToolRequest("read_file", workspace, true, map[string]any{"path": "notes.md"}))
	if err != nil || read.Content != "hello" {
		t.Fatalf("read in read-only run = %q, %v", read.Content, err)
	}
}

func TestFileToolsDeniedToolsAreRejected(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	request := fileToolRequest("write_file", workspace, false, map[string]any{"path": "denied.txt", "content": "x"})
	request.DeniedTools = []string{"write_file"}
	_, err := activities.RunTool(context.Background(), request)
	if err == nil {
		t.Fatal("denied tool must be rejected by the activity")
	}
}

func TestFileToolsPathsStayInsideWorkspace(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideTarget, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"../escape.txt", "/etc/passwd", "a/../../escape.txt"} {
		_, err := activities.RunTool(context.Background(), fileToolRequest("write_file", workspace, false, map[string]any{
			"path": path, "content": "x",
		}))
		if err == nil {
			t.Fatalf("escaping path %q accepted", path)
		}
	}

	// A symlink planted inside the workspace must not smuggle outside writes.
	if err := os.Symlink(outside, filepath.Join(workspace, "link-out")); err != nil {
		t.Fatal(err)
	}
	if _, err := activities.RunTool(context.Background(), fileToolRequest("write_file", workspace, false, map[string]any{
		"path": "link-out/new.txt", "content": "x",
	})); err == nil {
		t.Fatal("write through an escaping symlink accepted")
	}
	if _, err := activities.RunTool(context.Background(), fileToolRequest("read_file", workspace, false, map[string]any{
		"path": "link-out/secret.txt",
	})); err == nil {
		t.Fatal("read through an escaping symlink accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("escaping write landed outside the workspace")
	}
}

func TestFileToolsBounds(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	_, err := activities.RunTool(context.Background(), fileToolRequest("write_file", workspace, false, map[string]any{
		"path": "big.txt", "content": strings.Repeat("x", maxFileWriteBytes+1),
	}))
	if err == nil {
		t.Fatal("oversized write accepted")
	}
	if _, err := activities.RunTool(context.Background(), fileToolRequest("write_file", workspace, false, map[string]any{
		"path": "big.txt", "content": strings.Repeat("x", maxFileReadBytes+1000),
	})); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	read, err := activities.RunTool(context.Background(), fileToolRequest("read_file", workspace, false, map[string]any{"path": "big.txt"}))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read.Content) > maxFileReadBytes+200 || !strings.Contains(read.Content, "truncated") {
		t.Fatalf("read must truncate with a marker, got %d bytes", len(read.Content))
	}
}

func TestResolveWorkspacePathAllowsWorkspacePrefixAndNestedTargets(t *testing.T) {
	workspace := t.TempDir()
	for _, arg := range []string{"a.txt", "/workspace/a.txt", " ./a.txt "} {
		abs, err := resolveWorkspacePath(workspace, arg)
		if err != nil || abs != filepath.Join(workspace, "a.txt") {
			t.Fatalf("resolveWorkspacePath(%q) = %q, %v", arg, abs, err)
		}
	}
	if _, err := resolveWorkspacePath(workspace, "b/c/d.txt"); err != nil {
		t.Fatalf("non-existent nested write target must resolve: %v", err)
	}
}
