package ingest

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/world"
)

// skipDirectories are never walked: they are dependency caches, VCS internals,
// or build outputs that would burn the observation budget without teaching the
// substrate anything about the project.
var skipDirectories = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {}, "node_modules": {}, "vendor": {},
	"__pycache__": {}, ".venv": {}, "venv": {}, ".cache": {}, "dist": {},
	"build": {}, "target": {}, ".idea": {}, ".vscode": {},
}

// manifestFiles are recognized project descriptors; their content is read and
// offered to the deterministic extractors.
var manifestFiles = map[string]struct{}{
	"go.mod": {}, "package.json": {}, "Cargo.toml": {}, "pyproject.toml": {}, "README.md": {},
}

// observeResource dispatches ingestion by resource type and returns every
// committed observation. Filesystem roots are walked breadth-first with a
// bounded depth and event budget; git repositories add status/log reads;
// HTTP endpoints are fetched once.
func (r *Runner) observeResource(ctx context.Context, result *Result, resource world.Resource, request Request) ([]observation, error) {
	adapter := world.NewAdapter(r.World)
	switch resource.Type {
	case world.ResourceFilesystem, world.ResourceGitRepository:
		return r.walkFilesystem(ctx, adapter, result, resource, request)
	case world.ResourceHTTPEndpoint:
		return r.observeHTTPEndpoint(ctx, adapter, result, resource, request)
	default:
		return nil, fmt.Errorf("%w: resource type %q cannot be ingested", ErrInvalidRequest, resource.Type)
	}
}

// resolveRoot mirrors the adapter's root resolution so ingestion passes
// already-resolved absolute paths and stays contained.
func resolveRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		return resolved, nil
	}
	return filepath.Clean(absolute), nil
}

func (r *Runner) walkFilesystem(ctx context.Context, adapter *world.Adapter, result *Result, resource world.Resource, request Request) ([]observation, error) {
	root, err := resolveRoot(resource.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	observations := []observation{}
	rootStat, statErr := r.observeStep(ctx, adapter, result, request, execution.Step{ID: "stat-root", Capability: "filesystem.read", Operation: "stat", Input: map[string]any{"path": root}}, ".")
	if statErr != nil {
		return observations, statErr
	}
	observations = append(observations, *rootStat)
	if directory, _ := rootStat.payload["directory"].(bool); !directory {
		// The resource points at a single file; read it as content.
		content, readErr := r.observeStep(ctx, adapter, result, request, execution.Step{ID: "read-root", Capability: "filesystem.read", Operation: "read_file", Input: map[string]any{"path": root}}, filepath.Base(root))
		if readErr != nil {
			return observations, readErr
		}
		return append(observations, *content), nil
	}

	type pending struct {
		path  string
		depth int
	}
	queue := []pending{{path: root}}
	for len(queue) > 0 && len(result.Observations) < request.MaxEvents {
		item := queue[0]
		queue = queue[1:]
		rel := relativeTo(root, item.path)
		listing, listErr := r.observeStep(ctx, adapter, result, request, execution.Step{ID: "list-" + rel, Capability: "filesystem.read", Operation: "list_dir", Input: map[string]any{"path": item.path}}, rel)
		if listErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("list %s: %v", rel, listErr))
			continue
		}
		observations = append(observations, *listing)
		items := anySlice(listing.payload["items"])
		for _, raw := range items {
			entry, _ := raw.(map[string]any)
			name, _ := entry["name"].(string)
			if name == "" {
				continue
			}
			child := filepath.Join(item.path, name)
			if isDir, _ := entry["directory"].(bool); isDir {
				if item.depth < request.Depth {
					if _, skipped := skipDirectories[name]; !skipped {
						queue = append(queue, pending{path: child, depth: item.depth + 1})
					}
				}
				continue
			}
			if _, recognized := manifestFiles[name]; !recognized || len(result.Observations) >= request.MaxEvents {
				continue
			}
			content, readErr := r.observeStep(ctx, adapter, result, request, execution.Step{ID: "read-" + relativeTo(root, child), Capability: "filesystem.read", Operation: "read_file", Input: map[string]any{"path": child}}, relativeTo(root, child))
			if readErr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("read %s: %v", relativeTo(root, child), readErr))
				continue
			}
			observations = append(observations, *content)
		}
	}
	if len(result.Observations) >= request.MaxEvents {
		result.Truncated = true
		result.Warnings = append(result.Warnings, fmt.Sprintf("observation budget reached (%d events); walk truncated", request.MaxEvents))
	}
	if resource.Type == world.ResourceGitRepository {
		if status, statusErr := r.observeStep(ctx, adapter, result, request, execution.Step{ID: "git-status", Capability: "git.read", Operation: "status", Input: map[string]any{"path": root}}, "."); statusErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("git status: %v", statusErr))
		} else {
			observations = append(observations, *status)
		}
		if log, logErr := r.observeStep(ctx, adapter, result, request, execution.Step{ID: "git-log", Capability: "git.read", Operation: "log", Input: map[string]any{"path": root, "limit": float64(request.GitLogLimit)}}, "."); logErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("git log: %v", logErr))
		} else {
			observations = append(observations, *log)
		}
	}
	return observations, nil
}

func (r *Runner) observeHTTPEndpoint(ctx context.Context, adapter *world.Adapter, result *Result, resource world.Resource, request Request) ([]observation, error) {
	observed, err := r.observeStep(ctx, adapter, result, request, execution.Step{ID: "http-get", Capability: "http.read", Operation: "get", Input: map[string]any{"url": resource.Endpoint}}, strings.TrimPrefix(resource.Endpoint, "http://"))
	if err != nil {
		return nil, err
	}
	if observed.record.EventID == "" {
		return nil, fmt.Errorf("%w: endpoint %q produced no observation", ErrInvalidRequest, resource.Endpoint)
	}
	return []observation{*observed}, nil
}

// relativeTo formats an observed path relative to the walk root with forward
// slashes; the root itself is ".".
func relativeTo(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "" {
		return strings.ReplaceAll(target, string(filepath.Separator), "/")
	}
	return strings.ReplaceAll(rel, string(filepath.Separator), "/")
}
