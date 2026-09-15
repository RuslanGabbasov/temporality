package world

import (
	"fmt"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
)

// Write world affordance ids (M12). The primitive filesystem/process/git/http
// operations remain Executor building blocks: these affordances compose them
// into deterministic workflows, and semantic affordances (fix_bug, run_tests,
// reproduce_issue) will be layered on top in M15.
const (
	AffordanceWriteFile       = "write_file"
	AffordanceCreateFile      = "create_file"
	AffordancePatchFile       = "patch_file"
	AffordanceDeleteFile      = "delete_file"
	AffordanceMoveFile        = "move_file"
	AffordanceCreateDir       = "create_dir"
	AffordanceRunCommand      = "run_command"
	AffordanceGitCreateBranch = "git_create_branch"
	AffordanceGitCommit       = "git_commit"
	AffordanceHTTPPost        = "http_post"
)

// StandardWriteDefinitions returns the canonical M12 write affordance
// definitions.
func StandardWriteDefinitions() []affordance.Definition {
	return []affordance.Definition{
		standardDefinition(AffordanceWriteFile, "filesystem.write"),
		standardDefinition(AffordanceCreateFile, "filesystem.write"),
		standardDefinition(AffordancePatchFile, "filesystem.write"),
		standardDefinition(AffordanceDeleteFile, "filesystem.write"),
		standardDefinition(AffordanceMoveFile, "filesystem.write"),
		standardDefinition(AffordanceCreateDir, "filesystem.write"),
		standardDefinition(AffordanceRunCommand, "process.execute"),
		standardDefinition(AffordanceGitCreateBranch, "git.write"),
		standardDefinition(AffordanceGitCommit, "git.write"),
		standardDefinition(AffordanceHTTPPost, "http.write"),
	}
}

func argumentBool(request affordance.Request, key string) bool {
	value, _ := request.Arguments[key].(bool)
	return value
}

func argumentStringSlice(request affordance.Request, key string) []any {
	values, _ := request.Arguments[key].([]any)
	if values == nil {
		return []any{}
	}
	return values
}

// WriteFileWorkflow plans a bounded file write (creates or overwrites).
type WriteFileWorkflow struct{}

func (WriteFileWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	content, _ := request.Arguments["content"].(string)
	return execution.Plan{Steps: []execution.Step{
		{ID: "write-file", Capability: "filesystem.write", Operation: "write_file", Input: map[string]any{"path": path, "content": content}},
	}}, nil
}

// CreateFileWorkflow plans a file creation that refuses to clobber existing
// content.
type CreateFileWorkflow struct{}

func (CreateFileWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	content, _ := request.Arguments["content"].(string)
	return execution.Plan{Steps: []execution.Step{
		{ID: "create-file", Capability: "filesystem.write", Operation: "create_file", Input: map[string]any{"path": path, "content": content}},
	}}, nil
}

// PatchFileWorkflow plans an in-place string replacement.
type PatchFileWorkflow struct{}

func (PatchFileWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	find, err := argumentString(request, "find")
	if err != nil {
		return execution.Plan{}, err
	}
	replace, _ := request.Arguments["replace"].(string)
	return execution.Plan{Steps: []execution.Step{
		{ID: "patch-file", Capability: "filesystem.write", Operation: "patch_file", Input: map[string]any{"path": path, "find": find, "replace": replace, "all": argumentBool(request, "all")}},
	}}, nil
}

// DeleteFileWorkflow plans a file (or empty directory) removal.
type DeleteFileWorkflow struct{}

func (DeleteFileWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "delete-file", Capability: "filesystem.write", Operation: "delete_file", Input: map[string]any{"path": path}},
	}}, nil
}

// MoveFileWorkflow plans a rename inside declared writable roots.
type MoveFileWorkflow struct{}

func (MoveFileWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	target, err := argumentString(request, "target")
	if err != nil {
		return execution.Plan{}, err
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "move-file", Capability: "filesystem.write", Operation: "move_file", Input: map[string]any{"path": path, "target": target}},
	}}, nil
}

// CreateDirWorkflow plans a directory creation (including parents).
type CreateDirWorkflow struct{}

func (CreateDirWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "create-dir", Capability: "filesystem.write", Operation: "create_dir", Input: map[string]any{"path": path}},
	}}, nil
}

// RunCommandWorkflow plans a bounded command execution.
type RunCommandWorkflow struct{}

func (RunCommandWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	command, err := argumentString(request, "command")
	if err != nil {
		return execution.Plan{}, err
	}
	input := map[string]any{"command": command, "args": argumentStringSlice(request, "args")}
	if cwd, _ := request.Arguments["cwd"].(string); cwd != "" {
		input["cwd"] = cwd
	}
	if env, ok := request.Arguments["env"].(map[string]any); ok && len(env) > 0 {
		input["env"] = env
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "run-command", Capability: "process.execute", Operation: "run", Input: input},
	}}, nil
}

// GitCreateBranchWorkflow plans a branch creation pointing at HEAD or a named
// start point.
type GitCreateBranchWorkflow struct{}

func (GitCreateBranchWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	name, err := argumentString(request, "name")
	if err != nil {
		return execution.Plan{}, fmt.Errorf("name argument is required")
	}
	input := map[string]any{"path": path, "name": name}
	if from, _ := request.Arguments["from"].(string); from != "" {
		input["from"] = from
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "git-create-branch", Capability: "git.write", Operation: "create_branch", Input: input},
	}}, nil
}

// GitCommitWorkflow plans a worktree snapshot commit.
type GitCommitWorkflow struct{}

func (GitCommitWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, err := argumentString(request, "path")
	if err != nil {
		return execution.Plan{}, err
	}
	message, err := argumentString(request, "message")
	if err != nil {
		return execution.Plan{}, fmt.Errorf("message argument is required")
	}
	input := map[string]any{"path": path, "message": message}
	if author, _ := request.Arguments["author"].(string); author != "" {
		input["author"] = author
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "git-commit", Capability: "git.write", Operation: "commit", Input: input},
	}}, nil
}

// HTTPPostWorkflow plans a bounded HTTP POST against a declared writable
// endpoint.
type HTTPPostWorkflow struct{}

func (HTTPPostWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	target, err := argumentString(request, "url")
	if err != nil {
		return execution.Plan{}, err
	}
	input := map[string]any{"url": target}
	if body, _ := request.Arguments["body"].(string); body != "" {
		input["body"] = body
	}
	if contentType, _ := request.Arguments["content_type"].(string); contentType != "" {
		input["content_type"] = contentType
	}
	return execution.Plan{Steps: []execution.Step{
		{ID: "http-post", Capability: "http.write", Operation: "post", Input: input},
	}}, nil
}

func writeWorkflows() map[string]execution.DeterministicWorkflow {
	return map[string]execution.DeterministicWorkflow{
		AffordanceWriteFile:       WriteFileWorkflow{},
		AffordanceCreateFile:      CreateFileWorkflow{},
		AffordancePatchFile:       PatchFileWorkflow{},
		AffordanceDeleteFile:      DeleteFileWorkflow{},
		AffordanceMoveFile:        MoveFileWorkflow{},
		AffordanceCreateDir:       CreateDirWorkflow{},
		AffordanceRunCommand:      RunCommandWorkflow{},
		AffordanceGitCreateBranch: GitCreateBranchWorkflow{},
		AffordanceGitCommit:       GitCommitWorkflow{},
		AffordanceHTTPPost:        HTTPPostWorkflow{},
	}
}
