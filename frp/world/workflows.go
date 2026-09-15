package world

import (
	"fmt"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/protocol"
)

// Read-only world affordance ids (M11.3). These are semantic affordances:
// each one may combine several primitive capabilities, and the primitive
// filesystem/git/http operations remain Executor building blocks only.
const (
	AffordanceInspectEnvironment = "inspect_environment"
	AffordanceInspectWorkspace   = "inspect_workspace"
	AffordanceListFiles          = "list_files"
	AffordanceReadFile           = "read_file"
	AffordanceGitStatus          = "git_status"
	AffordanceGitLog             = "git_log"
	AffordanceInspectHTTP        = "inspect_http"
)

func standardDefinition(id string, capabilities ...string) affordance.Definition {
	return affordance.Definition{Protocol: protocol.Name, Version: protocol.Version, ID: id, ExecutionMode: affordance.ModeDeterministic, InputSchema: map[string]any{}, Capabilities: capabilities, Limits: affordance.Limits{TimeoutSec: 30, CPU: 1, MemoryMB: 128, DiskMB: 64}, Planner: affordance.Planner{}, FailurePolicy: affordance.FailurePolicy{}}
}

// StandardReadDefinitions returns the canonical M11 read-only affordance
// definitions. Values match the definitions already frozen in existing smoke
// flows so registries stay compatible.
func StandardReadDefinitions() []affordance.Definition {
	return []affordance.Definition{
		standardDefinition(AffordanceInspectEnvironment, "filesystem.read"),
		standardDefinition(AffordanceInspectWorkspace, "filesystem.read"),
		standardDefinition(AffordanceListFiles, "filesystem.read"),
		standardDefinition(AffordanceReadFile, "filesystem.read"),
		standardDefinition(AffordanceGitStatus, "git.read"),
		standardDefinition(AffordanceGitLog, "git.read"),
		standardDefinition(AffordanceInspectHTTP, "http.read"),
	}
}

// StandardWorkflows maps affordance ids to deterministic planners, covering
// the M11 read registry, the M12 write registry and the semantic registry.
func StandardWorkflows() map[string]execution.DeterministicWorkflow {
	workflows := map[string]execution.DeterministicWorkflow{
		AffordanceInspectEnvironment: InspectEnvironmentWorkflow{},
		AffordanceInspectWorkspace:   InspectWorkspaceWorkflow{},
		AffordanceListFiles:          ListFilesWorkflow{},
		AffordanceReadFile:           ReadFileWorkflow{},
		AffordanceGitStatus:          GitStatusWorkflow{},
		AffordanceGitLog:             GitLogWorkflow{},
		AffordanceInspectHTTP:        InspectHTTPWorkflow{},
	}
	for id, workflow := range writeWorkflows() {
		workflows[id] = workflow
	}
	for id, workflow := range semanticWorkflows() {
		workflows[id] = workflow
	}
	return workflows
}

func argumentString(request affordance.Request, key string) (string, error) {
	value, ok := request.Arguments[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%s argument is required", key)
	}
	return value, nil
}

// InspectEnvironmentWorkflow plans a filesystem stat plus an environment
// observation for a workspace path.
type InspectEnvironmentWorkflow struct{}

func (InspectEnvironmentWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "stat-path", Capability: "filesystem.read", Operation: "stat", Input: map[string]any{"path": path}},
	}}, nil
}

// InspectWorkspaceWorkflow plans a stat followed by a bounded top-level
// directory listing of the workspace.
type InspectWorkspaceWorkflow struct{}

func (InspectWorkspaceWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "stat-workspace", Capability: "filesystem.read", Operation: "stat", Input: map[string]any{"path": path}},
		{ID: "list-workspace", Capability: "filesystem.read", Operation: "list_dir", Input: map[string]any{"path": path}},
	}}, nil
}

// ListFilesWorkflow plans a bounded directory listing.
type ListFilesWorkflow struct{}

func (ListFilesWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "list-files", Capability: "filesystem.read", Operation: "list_dir", Input: map[string]any{"path": path}},
	}}, nil
}

// ReadFileWorkflow plans a bounded file read.
type ReadFileWorkflow struct{}

func (ReadFileWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "read-file", Capability: "filesystem.read", Operation: "read_file", Input: map[string]any{"path": path}},
	}}, nil
}

// GitStatusWorkflow plans a repository status observation.
type GitStatusWorkflow struct{}

func (GitStatusWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "git-status", Capability: "git.read", Operation: "status", Input: map[string]any{"path": path}},
	}}, nil
}

// GitLogWorkflow plans a bounded first-parent history walk.
type GitLogWorkflow struct{}

func (GitLogWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	limit := 20.0
	if raw, ok := request.Arguments["limit"].(float64); ok && raw > 0 {
		limit = raw
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "git-log", Capability: "git.read", Operation: "log", Input: map[string]any{"path": path, "limit": limit}},
	}}, nil
}

// InspectHTTPWorkflow plans a bounded HTTP GET against a declared endpoint.
type InspectHTTPWorkflow struct{}

func (InspectHTTPWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	target, err := argumentString(request, "url")
	if err != nil {
		return execution.Plan{}, err
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "http-get", Capability: "http.read", Operation: "get", Input: map[string]any{"url": target}},
	}}, nil
}
