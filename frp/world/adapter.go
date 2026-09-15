package world

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/frp/execution"
)

// Observation types produced by the read-only world adapter (M11.3).
const (
	ObservationStat             = "stat"
	ObservationDirectoryListing = "directory_listing"
	ObservationFileContent      = "file_content"
	ObservationEnvironment      = "environment"
	ObservationGitStatus        = "git_status"
	ObservationGitLog           = "git_log"
	ObservationHTTPResponse     = "http_response"
)

// Result is the outcome of one adapter step. Whenever ObservationType is set
// the step produced a world observation that must be committed to the event
// log by the executor worker.
type Result struct {
	Output          map[string]any `json:"output"`
	Resource        string         `json:"resource,omitempty"`
	ObservationType string         `json:"observation_type,omitempty"`
	Truncated       bool           `json:"truncated,omitempty"`
}

// Adapter executes read-only world steps against the resources a World
// declares. It performs no inference and never writes to the filesystem.
type Adapter struct {
	Client *http.Client

	mu    sync.RWMutex
	world World
}

// NewAdapter binds an adapter to the initial world state.
func NewAdapter(value World) *Adapter {
	value.ApplyDefaults()
	return &Adapter{world: value}
}

// World returns the currently bound world snapshot.
func (a *Adapter) World() World {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.world
}

// SetWorld atomically rebinds the world snapshot used for subsequent steps.
func (a *Adapter) SetWorld(value World) {
	value.ApplyDefaults()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.world = value
}

// Execute satisfies the executor worker Adapter contract and discards
// observation metadata; Observe is the richer entry point.
func (a *Adapter) Execute(ctx context.Context, step execution.Step) (map[string]any, error) {
	result, err := a.Observe(ctx, step)
	if err != nil {
		return nil, err
	}
	return result.Output, nil
}

// Observe validates the step against the world, executes it, and returns the
// observation the executor must persist.
func (a *Adapter) Observe(ctx context.Context, step execution.Step) (Result, error) {
	world := a.World()
	if err := world.Authorize(step.Capability); err != nil {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: err.Error(), Retryable: false}
	}
	if step.Input == nil {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: "step input is required", Retryable: false}
	}
	switch step.Capability {
	case "filesystem.observe":
		return a.observeEnvironment(step)
	case "filesystem.read":
		return a.observeFilesystem(step)
	case "git.read":
		return a.observeGit(step)
	case "http.read":
		return a.observeHTTP(ctx, world, step)
	default:
		return Result{}, &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: fmt.Sprintf("capability %q has no read-only adapter", step.Capability), Retryable: false}
	}
}

func (a *Adapter) observeEnvironment(step execution.Step) (Result, error) {
	if step.Operation != "inspect" {
		return Result{}, unsupported(step)
	}
	world := a.World()
	entries := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok && key != "" {
			entries = append(entries, key)
		}
	}
	sort.Strings(entries)
	if len(entries) > world.Limits.MaxEntries {
		entries = entries[:world.Limits.MaxEntries]
	}
	workingDir, _ := os.Getwd()
	output := map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "workdir": workingDir, "env_var_names": entries}
	return Result{Output: output, Resource: "environment:" + hostname(), ObservationType: ObservationEnvironment}, nil
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return name
}

func (a *Adapter) observeFilesystem(step execution.Step) (Result, error) {
	world := a.World()
	root, relative, resource, err := a.resolvePath(world, step.Input)
	if err != nil {
		return Result{}, err
	}
	target := filepath.Join(root, relative)
	switch step.Operation {
	case "stat":
		info, statErr := os.Stat(target)
		if statErr != nil {
			return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: statErr.Error(), Retryable: false}
		}
		output := map[string]any{"path": target, "exists": true, "directory": info.IsDir(), "size": info.Size()}
		return Result{Output: output, Resource: resource, ObservationType: ObservationStat}, nil
	case "list_dir":
		entries, readErr := os.ReadDir(target)
		if readErr != nil {
			return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: readErr.Error(), Retryable: false}
		}
		truncated := false
		if len(entries) > world.Limits.MaxEntries {
			entries = entries[:world.Limits.MaxEntries]
			truncated = true
		}
		items := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			var size any
			if info, infoErr := entry.Info(); infoErr == nil && !entry.IsDir() {
				size = info.Size()
			}
			items = append(items, map[string]any{"name": entry.Name(), "directory": entry.IsDir(), "size": size})
		}
		sort.Slice(items, func(i, j int) bool { return items[i]["name"].(string) < items[j]["name"].(string) })
		output := map[string]any{"path": target, "entries": len(items), "items": items}
		return Result{Output: output, Resource: resource, ObservationType: ObservationDirectoryListing, Truncated: truncated}, nil
	case "read_file":
		raw, readErr := os.ReadFile(target)
		if readErr != nil {
			return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: readErr.Error(), Retryable: false}
		}
		truncated := int64(len(raw)) > world.Limits.MaxReadBytes
		if truncated {
			raw = raw[:world.Limits.MaxReadBytes]
		}
		output := map[string]any{"path": target, "size": len(raw), "content": string(raw)}
		return Result{Output: output, Resource: resource, ObservationType: ObservationFileContent, Truncated: truncated}, nil
	default:
		return Result{}, unsupported(step)
	}
}

func (a *Adapter) observeGit(step execution.Step) (Result, error) {
	world := a.World()
	root, relative, _, err := a.resolvePath(world, step.Input)
	if err != nil {
		return Result{}, err
	}
	target := filepath.Join(root, relative)
	switch step.Operation {
	case "status":
		status, statusErr := InspectRepository(target, world.Limits.MaxEntries)
		if statusErr != nil {
			if errors.Is(statusErr, ErrNotAGitRepository) {
				return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: statusErr.Error(), Retryable: false}
			}
			return Result{}, statusErr
		}
		resource := "git:" + target
		return Result{Output: map[string]any{"path": target, "status": status}, Resource: resource, ObservationType: ObservationGitStatus}, nil
	case "log":
		limit := 20
		if raw, ok := step.Input["limit"].(float64); ok && int(raw) > 0 {
			limit = int(raw)
		}
		if limit > world.Limits.MaxEntries {
			limit = world.Limits.MaxEntries
		}
		gitDir, gitErr := GitDir(target)
		if gitErr != nil {
			return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: gitErr.Error(), Retryable: false}
		}
		_, head, _, headErr := ReadHead(gitDir)
		if headErr != nil {
			return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: headErr.Error(), Retryable: false}
		}
		if head == "" {
			return Result{Output: map[string]any{"path": target, "commits": []any{}}, Resource: "git:" + target, ObservationType: ObservationGitLog}, nil
		}
		commits, complete, walkErr := WalkCommits(gitDir, head, limit)
		if walkErr != nil {
			return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: walkErr.Error(), Retryable: false}
		}
		values := make([]map[string]any, 0, len(commits))
		for _, commit := range commits {
			values = append(values, map[string]any{"sha": commit.SHA, "author": commit.Author, "summary": commit.Summary, "parents": commit.Parents})
		}
		output := map[string]any{"path": target, "commits": values, "complete": complete}
		return Result{Output: output, Resource: "git:" + target, ObservationType: ObservationGitLog, Truncated: !complete}, nil
	default:
		return Result{}, unsupported(step)
	}
}

func (a *Adapter) observeHTTP(ctx context.Context, world World, step execution.Step) (Result, error) {
	if step.Operation != "get" {
		return Result{}, unsupported(step)
	}
	raw, _ := step.Input["url"].(string)
	if raw == "" {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: "url is required", Retryable: false}
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: "url must be absolute", Retryable: false}
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
		if target.Scheme == endpoint.Scheme && target.Host == endpoint.Host {
			if strings.HasPrefix(target.Path, endpoint.Path) {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: fmt.Sprintf("url %q is outside declared http_endpoint resources", raw), Retryable: false}
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: time.Duration(world.Limits.TimeoutSec) * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: err.Error(), Retryable: false}
	}
	response, err := client.Do(request)
	if err != nil {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorUnavailable, Message: err.Error(), Retryable: true}
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, world.Limits.MaxReadBytes+1))
	if err != nil {
		return Result{}, &execution.ExecutionError{Class: execution.ErrorUnavailable, Message: err.Error(), Retryable: true}
	}
	truncated := int64(len(body)) > world.Limits.MaxReadBytes
	if truncated {
		body = body[:world.Limits.MaxReadBytes]
	}
	output := map[string]any{"url": raw, "status": response.StatusCode, "content_type": response.Header.Get("Content-Type"), "size": len(body), "body": string(body)}
	return Result{Output: output, Resource: "http://" + target.Host, ObservationType: ObservationHTTPResponse, Truncated: truncated}, nil
}

// resolvePath resolves a step path inside a declared filesystem root and
// rejects anything escaping it, including through symlinks.
func (a *Adapter) resolvePath(world World, input map[string]any) (root string, relative string, resource string, err error) {
	raw, _ := input["path"].(string)
	if strings.TrimSpace(raw) == "" {
		return "", "", "", &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: "path is required", Retryable: false}
	}
	if filepath.IsAbs(raw) {
		for _, candidate := range world.Roots() {
			resolvedRoot, resolveErr := resolveRoot(candidate.Path)
			if resolveErr != nil {
				continue
			}
			if rel, relErr := filepath.Rel(resolvedRoot, filepath.Clean(raw)); relErr == nil && !strings.HasPrefix(rel, "..") {
				return resolvedRoot, rel, "filesystem:" + resolvedRoot, nil
			}
		}
		return "", "", "", &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: fmt.Sprintf("path %q is outside declared world resources", raw), Retryable: false}
	}
	// Relative paths resolve against the single declared root when there is
	// exactly one; otherwise the caller must be explicit.
	roots := world.Roots()
	if len(roots) == 1 {
		resolvedRoot, resolveErr := resolveRoot(roots[0].Path)
		if resolveErr != nil {
			return "", "", "", &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: resolveErr.Error(), Retryable: false}
		}
		relative = filepath.Clean(raw)
		if strings.HasPrefix(relative, "..") {
			return "", "", "", &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: fmt.Sprintf("path %q escapes the declared resource root", raw), Retryable: false}
		}
		return resolvedRoot, relative, "filesystem:" + resolvedRoot, nil
	}
	return "", "", "", &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: "multiple filesystem resources declared: path must be absolute", Retryable: false}
}

func resolveRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved, nil
	}
	// Root may not exist yet; containment is still enforced against the
	// cleaned absolute path.
	return filepath.Clean(absolute), nil
}

func unsupported(step execution.Step) error {
	return &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: fmt.Sprintf("operation %q is not supported for capability %q", step.Operation, step.Capability), Retryable: false}
}
