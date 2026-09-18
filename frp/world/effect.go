package world

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/execution"
)

// Write adapters (M12). Every handler performs a physical change and returns a
// Result whose EffectType records what changed; the executor worker persists
// that as a canonical world.effect event so replay can always answer what
// happened in the world and why.

func writeDenied(message string) error {
	return &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: message, Retryable: false}
}

func writeInvalid(message string) error {
	return &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: message, Retryable: false}
}

// resolveWriteTarget resolves a path inside a declared filesystem root and
// additionally refuses read-only resources: writes may only land in resources
// the world explicitly declared writable.
func resolveWriteTarget(world World, input map[string]any) (Resource, string, string, error) {
	resource, root, relative, err := resolveRootResource(world, input)
	if err != nil {
		return Resource{}, "", "", err
	}
	if resource.ReadOnly {
		return Resource{}, "", "", writeDenied(fmt.Sprintf("resource %q is read-only", resource.ID))
	}
	return resource, root, relative, nil
}

func inputString(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

func (a *Adapter) effectFilesystem(world World, step execution.Step) (Result, error) {
	switch step.Operation {
	case "write_file":
		_, root, relative, err := resolveWriteTarget(world, step.Input)
		if err != nil {
			return Result{}, err
		}
		content := inputString(step.Input, "content")
		if int64(len(content)) > world.Limits.MaxReadBytes {
			return Result{}, &execution.ExecutionError{Class: execution.ErrorResourceExhausted, Message: fmt.Sprintf("content exceeds max_read_bytes (%d)", world.Limits.MaxReadBytes), Retryable: false}
		}
		target := filepath.Join(root, relative)
		if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return Result{}, writeInvalid(err.Error())
		}
		_, statErr := os.Stat(target)
		overwritten := statErr == nil
		if err = os.WriteFile(target, []byte(content), 0o644); err != nil {
			return Result{}, writeInvalid(err.Error())
		}
		return Result{Output: map[string]any{"path": target, "bytes": len(content), "overwritten": overwritten}, Resource: "filesystem:" + root, EffectType: EffectFileWritten}, nil
	case "create_file":
		_, root, relative, err := resolveWriteTarget(world, step.Input)
		if err != nil {
			return Result{}, err
		}
		content := inputString(step.Input, "content")
		if int64(len(content)) > world.Limits.MaxReadBytes {
			return Result{}, &execution.ExecutionError{Class: execution.ErrorResourceExhausted, Message: fmt.Sprintf("content exceeds max_read_bytes (%d)", world.Limits.MaxReadBytes), Retryable: false}
		}
		target := filepath.Join(root, relative)
		handle, openErr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if openErr != nil {
			return Result{}, writeInvalid(openErr.Error())
		}
		_, writeErr := handle.WriteString(content)
		closeErr := handle.Close()
		if writeErr != nil {
			return Result{}, writeInvalid(writeErr.Error())
		}
		if closeErr != nil {
			return Result{}, writeInvalid(closeErr.Error())
		}
		return Result{Output: map[string]any{"path": target, "bytes": len(content)}, Resource: "filesystem:" + root, EffectType: EffectFileCreated}, nil
	case "patch_file":
		_, root, relative, err := resolveWriteTarget(world, step.Input)
		if err != nil {
			return Result{}, err
		}
		find := inputString(step.Input, "find")
		replace := inputString(step.Input, "replace")
		if find == "" {
			return Result{}, writeInvalid("find is required")
		}
		target := filepath.Join(root, relative)
		raw, readErr := os.ReadFile(target)
		if readErr != nil {
			return Result{}, writeInvalid(readErr.Error())
		}
		if int64(len(raw)) > world.Limits.MaxReadBytes {
			return Result{}, &execution.ExecutionError{Class: execution.ErrorResourceExhausted, Message: fmt.Sprintf("file exceeds max_read_bytes (%d)", world.Limits.MaxReadBytes), Retryable: false}
		}
		count := strings.Count(string(raw), find)
		if count == 0 {
			return Result{}, writeInvalid("find pattern not present in file")
		}
		updated := string(raw)
		replaceAll := false
		if value, ok := step.Input["all"].(bool); ok {
			replaceAll = value
		}
		if replaceAll {
			updated = strings.ReplaceAll(updated, find, replace)
		} else {
			updated = strings.Replace(updated, find, replace, 1)
			count = 1
		}
		if err = os.WriteFile(target, []byte(updated), 0o644); err != nil {
			return Result{}, writeInvalid(err.Error())
		}
		return Result{Output: map[string]any{"path": target, "replacements": count}, Resource: "filesystem:" + root, EffectType: EffectFilePatched}, nil
	case "move_file":
		_, root, relative, err := resolveWriteTarget(world, step.Input)
		if err != nil {
			return Result{}, err
		}
		targetResource, targetRoot, targetRelative, err := resolveWriteTarget(world, map[string]any{"path": inputString(step.Input, "target")})
		if err != nil {
			return Result{}, err
		}
		_ = targetResource // both roots are validated writable; cross-root moves are permitted
		source := filepath.Join(root, relative)
		destination := filepath.Join(targetRoot, targetRelative)
		if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return Result{}, writeInvalid(err.Error())
		}
		if err = os.Rename(source, destination); err != nil {
			return Result{}, writeInvalid(err.Error())
		}
		return Result{Output: map[string]any{"path": source, "target": destination}, Resource: "filesystem:" + targetRoot, EffectType: EffectFileMoved}, nil
	case "delete_file":
		_, root, relative, err := resolveWriteTarget(world, step.Input)
		if err != nil {
			return Result{}, err
		}
		if relative == "." {
			return Result{}, writeDenied("cannot delete a declared resource root")
		}
		target := filepath.Join(root, relative)
		if err = os.Remove(target); err != nil {
			return Result{}, writeInvalid(err.Error())
		}
		return Result{Output: map[string]any{"path": target, "deleted": true}, Resource: "filesystem:" + root, EffectType: EffectFileDeleted}, nil
	case "create_dir":
		_, root, relative, err := resolveWriteTarget(world, step.Input)
		if err != nil {
			return Result{}, err
		}
		target := filepath.Join(root, relative)
		if err = os.MkdirAll(target, 0o755); err != nil {
			return Result{}, writeInvalid(err.Error())
		}
		return Result{Output: map[string]any{"path": target}, Resource: "filesystem:" + root, EffectType: EffectDirCreated}, nil
	default:
		return Result{}, unsupported(step)
	}
}

// limitedBuffer captures up to max bytes and reports whether more was written.
type limitedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
	total    int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.total += len(p)
	room := l.max - l.buf.Len()
	if room > 0 {
		if len(p) <= room {
			l.buf.Write(p)
		} else {
			l.buf.Write(p[:room])
			l.overflow = true
		}
	} else if len(p) > 0 {
		l.overflow = true
	}
	return len(p), nil
}

func (a *Adapter) effectProcess(ctx context.Context, world World, step execution.Step) (Result, error) {
	if step.Operation != "run" {
		return Result{}, unsupported(step)
	}
	command := inputString(step.Input, "command")
	if command == "" {
		return Result{}, writeInvalid("command is required")
	}
	if err := world.AuthorizeProcess(command); err != nil {
		return Result{}, writeDenied(err.Error())
	}
	arguments := make([]string, 0)
	if raw, ok := step.Input["args"].([]any); ok {
		for _, item := range raw {
			if value, ok := item.(string); ok {
				arguments = append(arguments, value)
			}
		}
	}
	workingDir := inputString(step.Input, "cwd")
	if workingDir != "" {
		// A relative cwd (and an absolute one inside a declared root) must land
		// inside the world's resource, never in the executor's own working
		// directory: cmd.Dir is process-relative, and a bare "." would silently
		// run the command wherever the executor happened to start — the executor
		// binary often lives in the runtime's own repository, whose tests then
		// masquerade as the target repo's results.
		root, relative, _, err := a.resolvePath(world, map[string]any{"path": workingDir})
		if err != nil {
			return Result{}, err
		}
		workingDir = filepath.Join(root, relative)
	} else if roots := world.Roots(); len(roots) > 0 {
		resolved, resolveErr := resolveRoot(roots[0].Path)
		if resolveErr == nil {
			workingDir = resolved
		}
	}
	env := os.Environ()
	if overrides, ok := step.Input["env"].(map[string]any); ok {
		for key, value := range overrides {
			if key == "" {
				return Result{}, writeInvalid("env keys must be non-empty")
			}
			env = append(env, fmt.Sprintf("%s=%v", key, value))
		}
	}
	timeout := time.Duration(world.Limits.TimeoutSec) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(runCtx, command, arguments...)
	cmd.Dir = workingDir
	cmd.Env = env
	stdout := &limitedBuffer{max: int(world.Limits.MaxReadBytes)}
	stderr := &limitedBuffer{max: int(world.Limits.MaxReadBytes)}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	duration := time.Since(start).Milliseconds()
	if runCtx.Err() == context.DeadlineExceeded {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorTimeout, Message: fmt.Sprintf("command %q exceeded timeout (%ds)", command, world.Limits.TimeoutSec), Retryable: true, Diagnostics: map[string]any{"exit_code": exitCode(runErr), "stdout": stdout.buf.String(), "stderr": stderr.buf.String()}}
	}
	if runErr != nil {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorProcessFailed, Message: fmt.Sprintf("command %q failed: %v", command, runErr), Retryable: false, Diagnostics: map[string]any{"exit_code": exitCode(runErr), "stdout": stdout.buf.String(), "stderr": stderr.buf.String()}}
	}
	return Result{
		Output:     map[string]any{"command": command, "args": arguments, "cwd": cmd.Dir, "exit_code": 0, "duration_ms": duration, "stdout": stdout.buf.String(), "stderr": stderr.buf.String()},
		Resource:   "process:" + command,
		EffectType: EffectProcessRun,
		Truncated:  stdout.overflow || stderr.overflow,
	}, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func (a *Adapter) effectGit(world World, step execution.Step) (Result, error) {
	resource, root, _, err := resolveWriteTarget(world, step.Input)
	if err != nil {
		return Result{}, err
	}
	if resource.Type != ResourceGitRepository && resource.Type != ResourceFilesystem {
		return Result{}, writeDenied(fmt.Sprintf("resource %q cannot host a git effect", resource.ID))
	}
	switch step.Operation {
	case "create_branch":
		name := inputString(step.Input, "name")
		sha, branchErr := CreateBranch(root, name, inputString(step.Input, "from"))
		if branchErr != nil {
			return Result{}, writeInvalid(branchErr.Error())
		}
		return Result{Output: map[string]any{"path": root, "branch": name, "sha": sha}, Resource: "git:" + root, EffectType: EffectGitBranch}, nil
	case "commit":
		message := inputString(step.Input, "message")
		author := inputString(step.Input, "author")
		result, commitErr := CommitWorktree(root, message, author, world.Limits.MaxEntries)
		if commitErr != nil {
			return Result{}, writeInvalid(commitErr.Error())
		}
		return Result{Output: map[string]any{"path": root, "branch": result.Branch, "commit": result.Commit, "parent": result.Parent, "tree": result.Tree, "files": result.Files}, Resource: "git:" + root, EffectType: EffectGitCommit}, nil
	default:
		return Result{}, unsupported(step)
	}
}

func (a *Adapter) effectHTTP(ctx context.Context, world World, step execution.Step) (Result, error) {
	if step.Operation != "post" {
		return Result{}, unsupported(step)
	}
	raw := inputString(step.Input, "url")
	if raw == "" {
		return Result{}, writeInvalid("url is required")
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return Result{}, writeInvalid("url must be absolute")
	}
	allowed := false
	for _, resource := range world.HTTPEndpoints() {
		if resource.ReadOnly {
			continue
		}
		endpoint, parseErr := url.Parse(resource.Endpoint)
		if parseErr != nil {
			continue
		}
		if target.Scheme == endpoint.Scheme && target.Host == endpoint.Host && strings.HasPrefix(target.Path, endpoint.Path) {
			allowed = true
			break
		}
	}
	if !allowed {
		return Result{}, writeDenied(fmt.Sprintf("url %q is outside declared writable http_endpoint resources", raw))
	}
	body := inputString(step.Input, "body")
	if int64(len(body)) > world.Limits.MaxReadBytes {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorResourceExhausted, Message: fmt.Sprintf("body exceeds max_read_bytes (%d)", world.Limits.MaxReadBytes), Retryable: false}
	}
	contentType := inputString(step.Input, "content_type")
	if contentType == "" {
		contentType = "application/json"
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: time.Duration(world.Limits.TimeoutSec) * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, strings.NewReader(body))
	if err != nil {
		return Result{}, writeInvalid(err.Error())
	}
	request.Header.Set("Content-Type", contentType)
	response, err := client.Do(request)
	if err != nil {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorUnavailable, Message: err.Error(), Retryable: true}
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, world.Limits.MaxReadBytes+1))
	if err != nil {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorUnavailable, Message: err.Error(), Retryable: true}
	}
	truncated := int64(len(payload)) > world.Limits.MaxReadBytes
	if truncated {
		payload = payload[:world.Limits.MaxReadBytes]
	}
	return Result{Output: map[string]any{"url": raw, "status": response.StatusCode, "content_type": response.Header.Get("Content-Type"), "size": len(payload), "body": string(payload)}, Resource: "http://" + target.Host, EffectType: EffectHTTPPosted, Truncated: truncated}, nil
}
