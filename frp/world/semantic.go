package world

import (
	"fmt"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
)

// Semantic world affordances (M12.4): meaningful agent actions composed from
// primitive capabilities. The primitives remain Executor building blocks —
// the agent emits semantic intent and deterministic workflows compile it into
// steps, which may mix observations and effects in one atomic execution.
const (
	AffordanceInspectRepository   = "inspect_repository"
	AffordanceRunTests            = "run_tests"
	AffordanceReproduceIssue      = "reproduce_issue"
	AffordanceUpdateConfiguration = "update_configuration"
)

// StandardSemanticDefinitions returns the canonical semantic affordance
// definitions. Each declares the union of capabilities its workflow may plan
// plus the input schema its workflow plans from.
func StandardSemanticDefinitions() []affordance.Definition {
	return []affordance.Definition{
		schemaDefinition(AffordanceInspectRepository, pathSchema(map[string]any{"log_limit": map[string]any{"type": "number"}}), "filesystem.read", "git.read"),
		schemaDefinition(AffordanceRunTests, map[string]any{"type": "object", "properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "test runner binary, default go"},
			"args":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "runner arguments, default [test ./...]"},
			"path":    map[string]any{"type": "string", "description": "working directory inside the resource"},
		}}, "process.execute"),
		schemaDefinition(AffordanceReproduceIssue, map[string]any{"type": "object", "required": []string{"command"}, "properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "binary to run"},
			"args":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"path":    map[string]any{"type": "string", "description": "working directory inside the resource"},
		}}, "process.execute"),
		schemaDefinition(AffordanceUpdateConfiguration, map[string]any{"type": "object", "required": []string{"path", "find", "replace"}, "properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"find":    map[string]any{"type": "string", "description": "exact text to find"},
			"replace": map[string]any{"type": "string", "description": "replacement text"},
			"all":     map[string]any{"type": "boolean"},
		}}, "filesystem.read", "filesystem.write"),
	}
}

// InspectRepositoryWorkflow plans a repository orientation sweep: stat the
// root, list its top level, then read git status and recent history.
type InspectRepositoryWorkflow struct{}

func (InspectRepositoryWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	limit := 10.0
	if raw, ok := request.Arguments["log_limit"].(float64); ok && raw > 0 {
		limit = raw
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "stat-repository", Capability: "filesystem.read", Operation: "stat", Input: map[string]any{"path": path}},
		{ID: "list-repository", Capability: "filesystem.read", Operation: "list_dir", Input: map[string]any{"path": path}},
		{ID: "git-status", Capability: "git.read", Operation: "status", Input: map[string]any{"path": path}},
		{ID: "git-log", Capability: "git.read", Operation: "log", Input: map[string]any{"path": path, "limit": limit}},
	}}, nil
}

// RunTestsWorkflow plans a bounded test run. The command defaults to the Go
// toolchain; explicit command/args override the default for other stacks.
type RunTestsWorkflow struct{}

func (RunTestsWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	command := "go"
	if raw, ok := request.Arguments["command"].(string); ok && raw != "" {
		command = raw
	}
	arguments := []any{"test", "./..."}
	if raw, ok := request.Arguments["args"].([]any); ok && len(raw) > 0 {
		arguments = raw
	}
	input := map[string]any{"command": command, "args": arguments}
	if path, ok := request.Arguments["path"].(string); ok && path != "" {
		input["cwd"] = path
	}
	if env, ok := request.Arguments["env"].(map[string]any); ok && len(env) > 0 {
		input["env"] = env
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "run-tests", Capability: "process.execute", Operation: "run", Input: input},
	}}, nil
}

// ReproduceIssueWorkflow plans a reproduction command whose captured output
// becomes evidence for diagnosing a failure.
type ReproduceIssueWorkflow struct{}

func (ReproduceIssueWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	command, err := argumentString(request, "command")
	if err != nil {
		return execution.Plan{}, err
	}
	input := map[string]any{"command": command, "args": argumentStringSlice(request, "args")}
	if path, ok := request.Arguments["path"].(string); ok && path != "" {
		input["cwd"] = path
	}
	if env, ok := request.Arguments["env"].(map[string]any); ok && len(env) > 0 {
		input["env"] = env
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "reproduce", Capability: "process.execute", Operation: "run", Input: input},
	}}, nil
}

// UpdateConfigurationWorkflow plans a verified configuration change: the file
// is read first (the agent observes current content), then patched. One
// execution therefore commits both a world.observation and a world.effect.
type UpdateConfigurationWorkflow struct{}

func (UpdateConfigurationWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	find, err := argumentString(request, "find")
	if err != nil {
		return execution.Plan{}, err
	}
	replace, ok := request.Arguments["replace"].(string)
	if !ok {
		return execution.Plan{}, fmt.Errorf("replace argument is required")
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "read-configuration", Capability: "filesystem.read", Operation: "read_file", Input: map[string]any{"path": path}},
		{ID: "patch-configuration", Capability: "filesystem.write", Operation: "patch_file", Input: map[string]any{"path": path, "find": find, "replace": replace, "all": argumentBool(request, "all")}},
	}}, nil
}

func semanticWorkflows() map[string]execution.DeterministicWorkflow {
	return map[string]execution.DeterministicWorkflow{
		AffordanceInspectRepository:   InspectRepositoryWorkflow{},
		AffordanceRunTests:            RunTestsWorkflow{},
		AffordanceReproduceIssue:      ReproduceIssueWorkflow{},
		AffordanceUpdateConfiguration: UpdateConfigurationWorkflow{},
	}
}
