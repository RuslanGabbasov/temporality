package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
	"go.temporal.io/sdk/temporal"
)

// Native workspace file tools. They exist because shell-based file mutation
// proved fragile: the sandbox caps command arguments (docker.go), so agents
// resorted to chunked heredocs that silently corrupt files. These tools run
// host-side against the resolved workspace — no shell, no argument caps, no
// quoting hazards — and are advertised whenever the sandbox is configured,
// exactly like run_command.

const (
	// maxFileReadBytes bounds one read_file result; larger files return the
	// head with an explicit truncation marker instead of flooding the context.
	maxFileReadBytes = 256 << 10
	// maxFileWriteBytes bounds one write; bigger artifacts belong in /scratch
	// via run_command, not in the model's tool arguments.
	maxFileWriteBytes = 2 << 20
)

var fileToolDefs = []llm.ToolDef{
	{
		Name:        "read_file",
		Description: "Read a text file from the workspace. Returns the content directly, with no shell quoting hazards. Prefer this over cat for reading files.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Workspace-relative path, e.g. kernel/agent/foo.go (a leading /workspace/ is accepted)"},
			},
			"required": []string{"path"},
		},
	},
	{
		Name:        "write_file",
		Description: "Create or fully replace a workspace file. Parent directories are created automatically and the write is atomic. Always prefer this over shell heredocs (cat > file <<'EOF'): no argument size limits, no quoting or truncation hazards. For targeted changes to an existing file prefer edit_file.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Workspace-relative path (a leading /workspace/ is accepted)"},
				"content": map[string]any{"type": "string", "description": "Complete new content of the file"},
			},
			"required": []string{"path", "content"},
		},
	},
	{
		Name:        "edit_file",
		Description: "Replace exactly one occurrence of old_text with new_text in a workspace file. old_text must match the file content exactly (including indentation) and be unique — include surrounding lines when it is not. The call fails without changing anything when the fragment is missing or occurs more than once.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":     map[string]any{"type": "string", "description": "Workspace-relative path (a leading /workspace/ is accepted)"},
				"old_text": map[string]any{"type": "string", "description": "Exact text fragment to replace, matching the file byte-for-byte"},
				"new_text": map[string]any{"type": "string", "description": "Replacement text"},
			},
			"required": []string{"path", "old_text", "new_text"},
		},
	},
}

// cleanPath renders a model-supplied path as the canonical workspace-relative
// form for tool messages.
func cleanPath(arg string) string {
	rel := strings.TrimSpace(arg)
	rel = strings.TrimPrefix(rel, "/workspace")
	rel = strings.TrimPrefix(rel, "/")
	return filepath.Clean(rel)
}

// resolveWorkspacePath maps a model-supplied path ("/workspace/a.go" or
// "a.go") onto the host-side project workspace and proves the location stays
// inside it even through symlinks: the deepest existing ancestor is resolved
// and containment is checked on the resolved result, so a symlinked tail
// cannot smuggle reads or writes outside the workspace. Non-existent tails
// are allowed (write targets); their deepest existing ancestor is checked.
func resolveWorkspacePath(workspacePath, arg string) (string, error) {
	rel := strings.TrimSpace(arg)
	if strings.HasPrefix(rel, "/") && !strings.HasPrefix(rel, "/workspace") {
		return "", fmt.Errorf("path %q is absolute; use workspace-relative paths (a leading /workspace/ is accepted)", arg)
	}
	rel = strings.TrimPrefix(rel, "/workspace")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "", errors.New("path is required")
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", arg)
	}
	if workspacePath == "" {
		return "", errors.New("run has no workspace")
	}
	abs := filepath.Join(workspacePath, clean)
	resolved := abs
	for {
		if target, err := filepath.EvalSymlinks(resolved); err == nil {
			resolved = target
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve %q: %w", clean, err)
		}
		parent := filepath.Dir(resolved)
		if parent == resolved {
			break // reached the filesystem root without an existing ancestor
		}
		resolved = parent
	}
	if resolved != workspacePath && !strings.HasPrefix(resolved, workspacePath+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", arg)
	}
	return abs, nil
}

func invalidToolArguments(err error) error {
	return temporal.NewNonRetryableApplicationError(err.Error(), "InvalidToolArguments", nil)
}

func fileToolFailure(err error) error {
	return temporal.NewNonRetryableApplicationError(err.Error(), "FileToolFailed", nil)
}

// requireWritableWorkspace enforces the read-only run contract for the file
// tools: reviewer/qa roles and read-only agents may read the workspace but
// never change it. run_command already mounts the workspace :ro for them;
// these tools bypass the container, so the check lives here.
func requireWritableWorkspace(request ToolRequest) error {
	if request.ReadOnly || request.Role == "reviewer" || request.Role == "qa" {
		return temporal.NewNonRetryableApplicationError(
			"workspace is read-only for this run; file changes are not permitted", "ToolNotAllowed", nil)
	}
	return nil
}

// resolveToolTarget resolves the run workspace and the model-supplied path
// inside it. Argument-shape failures are non-retryable: they are
// deterministic caller errors with no side effect.
func (a *Activities) resolveToolTarget(request ToolRequest, pathArg string) (string, error) {
	if a.Sandbox == nil {
		return "", fileToolFailure(errors.New("sandbox is not configured"))
	}
	workspace, err := a.Sandbox.ResolveWorkspace(request.WorkspacePath)
	if err != nil {
		return "", fileToolFailure(err)
	}
	abs, err := resolveWorkspacePath(workspace, pathArg)
	if err != nil {
		return "", invalidToolArguments(err)
	}
	return abs, nil
}

// isProbablyBinary rejects NUL-containing payloads early: reading a binary
// file as text wastes the context window and corrupts the conversation.
func isProbablyBinary(data []byte) bool {
	sample := data
	if len(sample) > 8000 {
		sample = sample[:8000]
	}
	return strings.IndexByte(string(sample), 0) >= 0
}

// writeFileAtomic writes through a temp file + rename so a failed write never
// leaves a truncated target, preserving the previous mode when overwriting.
func writeFileAtomic(abs string, data []byte) error {
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(abs); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".temporality-write-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, abs)
}

func (a *Activities) handleReadFile(request ToolRequest) (ToolResult, error) {
	pathArg, _ := request.Arguments["path"].(string)
	if strings.TrimSpace(pathArg) == "" {
		return ToolResult{}, invalidToolArguments(errors.New("read_file requires path"))
	}
	abs, err := a.resolveToolTarget(request, pathArg)
	if err != nil {
		return ToolResult{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("read %q: %w", cleanPath(pathArg), err))
	}
	if info.IsDir() {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("%q is a directory; list it with run_command instead", cleanPath(pathArg)))
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return ToolResult{}, fileToolFailure(fmt.Errorf("read %q: %w", cleanPath(pathArg), err))
	}
	if isProbablyBinary(data) {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("%q looks like a binary file; read_file only supports text", cleanPath(pathArg)))
	}
	content := string(data)
	if len(data) > maxFileReadBytes {
		content = string(data[:maxFileReadBytes]) +
			fmt.Sprintf("\n[truncated by Agent Kernel: file is %d bytes, returned the first %d]", len(data), maxFileReadBytes)
	}
	return ToolResult{Content: content}, nil
}

func (a *Activities) handleWriteFile(request ToolRequest) (ToolResult, error) {
	if err := requireWritableWorkspace(request); err != nil {
		return ToolResult{}, err
	}
	pathArg, _ := request.Arguments["path"].(string)
	content, _ := request.Arguments["content"].(string)
	if strings.TrimSpace(pathArg) == "" {
		return ToolResult{}, invalidToolArguments(errors.New("write_file requires path"))
	}
	if len(content) > maxFileWriteBytes {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("content is %d bytes, the write cap is %d; split the file or produce it with run_command", len(content), maxFileWriteBytes))
	}
	abs, err := a.resolveToolTarget(request, pathArg)
	if err != nil {
		return ToolResult{}, err
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("%q is a directory", cleanPath(pathArg)))
	}
	if err := writeFileAtomic(abs, []byte(content)); err != nil {
		return ToolResult{}, fileToolFailure(fmt.Errorf("write %q: %w", cleanPath(pathArg), err))
	}
	return ToolResult{Content: fmt.Sprintf("wrote %d bytes to %s", len(content), cleanPath(pathArg))}, nil
}

func (a *Activities) handleEditFile(request ToolRequest) (ToolResult, error) {
	if err := requireWritableWorkspace(request); err != nil {
		return ToolResult{}, err
	}
	pathArg, _ := request.Arguments["path"].(string)
	oldText, _ := request.Arguments["old_text"].(string)
	newText, _ := request.Arguments["new_text"].(string)
	if strings.TrimSpace(pathArg) == "" {
		return ToolResult{}, invalidToolArguments(errors.New("edit_file requires path"))
	}
	if oldText == "" {
		return ToolResult{}, invalidToolArguments(errors.New("edit_file requires a non-empty old_text"))
	}
	if oldText == newText {
		return ToolResult{}, invalidToolArguments(errors.New("old_text and new_text are identical"))
	}
	abs, err := a.resolveToolTarget(request, pathArg)
	if err != nil {
		return ToolResult{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("read %q: %w", cleanPath(pathArg), err))
	}
	occurrences := strings.Count(string(data), oldText)
	switch occurrences {
	case 0:
		return ToolResult{}, invalidToolArguments(fmt.Errorf("old_text not found in %q; re-read the file and copy the fragment exactly, including indentation", cleanPath(pathArg)))
	case 1:
	default:
		return ToolResult{}, invalidToolArguments(fmt.Errorf("old_text occurs %d times in %q; extend the fragment with surrounding lines so it is unique", occurrences, cleanPath(pathArg)))
	}
	updated := strings.Replace(string(data), oldText, newText, 1)
	if len(updated) > maxFileWriteBytes {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("edited content would be %d bytes, the write cap is %d", len(updated), maxFileWriteBytes))
	}
	if err := writeFileAtomic(abs, []byte(updated)); err != nil {
		return ToolResult{}, fileToolFailure(fmt.Errorf("write %q: %w", cleanPath(pathArg), err))
	}
	return ToolResult{Content: fmt.Sprintf("replaced 1 occurrence in %s (file is now %d bytes)", cleanPath(pathArg), len(updated))}, nil
}
