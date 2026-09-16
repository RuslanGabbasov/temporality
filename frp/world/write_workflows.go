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
		schemaDefinition(AffordanceWriteFile, pathSchema(map[string]any{"content": map[string]any{"type": "string", "description": "full file content to write (overwrites)"}}), "filesystem.write"),
		schemaDefinition(AffordanceCreateFile, pathSchema(map[string]any{"content": map[string]any{"type": "string", "description": "file content; fails if the file already exists"}}), "filesystem.write"),
		schemaDefinition(AffordancePatchFile, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "path relative to the declared resource root"},
				"find":    map[string]any{"type": "string", "description": "exact substring to replace"},
				"replace": map[string]any{"type": "string", "description": "replacement substring (default empty)"},
				"all":     map[string]any{"type": "boolean", "description": "replace every occurrence (default false)"},
			},
			"required": []string{"path", "find"},
		}, "filesystem.write"),
		schemaDefinition(AffordanceDeleteFile, pathSchema(nil), "filesystem.write"),
		schemaDefinition(AffordanceMoveFile, pathSchema(map[string]any{"target": map[string]any{"type": "string", "description": "destination path relative to the resource root"}}), "filesystem.write"),
		schemaDefinition(AffordanceCreateDir, pathSchema(nil), "filesystem.write"),
		schemaDefinition(AffordanceRunCommand, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "executable to run, e.g. grep, go, git; prefer grep -rn to locate code instead of reading many files"},
				"args":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "command arguments"},
				"cwd":      map[string]any{"type": "string", "description": "working directory relative to the resource root"},
				"env":      map[string]any{"type": "object", "description": "extra environment variables"},
			},
			"required": []string{"command"},
		}, "process.execute"),
		schemaDefinition(AffordanceGitCreateBranch, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "repository path relative to the declared resource root"},
				"name": map[string]any{"type": "string", "description": "new branch name"},
				"from": map[string]any{"type": "string", "description": "start point ref (default HEAD)"},
			},
			"required": []string{"path", "name"},
		}, "git.write"),
		schemaDefinition(AffordanceGitCommit, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "repository path relative to the declared resource root"},
				"message": map[string]any{"type": "string", "description": "commit message"},
				"author":  map[string]any{"type": "string", "description": "author in Name <email> form"},
			},
			"required": []string{"path", "message"},
		}, "git.write"),
		schemaDefinition(AffordanceHTTPPost, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":          map[string]any{"type": "string", "description": "target endpoint URL"},
				"body":         map[string]any{"type": "string", "description": "request body"},
				"content_type": map[string]any{"type": "string", "description": "body content type"},
			},
			"required": []string{"url"},
		}, "http.write"),
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
