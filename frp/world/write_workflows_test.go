package world

import (
	"testing"

	"github.com/temporality-project/temporality/frp/affordance"
)

func TestStandardWriteRegistryCoversWorkflows(t *testing.T) {
	definitions := StandardWriteDefinitions()
	workflows := StandardWorkflows()
	if len(definitions) != 10 {
		t.Fatalf("unexpected write definition count: %d", len(definitions))
	}
	definitionIDs := map[string]struct{}{}
	for _, definition := range definitions {
		definitionIDs[definition.ID] = struct{}{}
	}
	for id := range workflows {
		if _, ok := definitionIDs[id]; ok {
			continue
		}
		if _, ok := readDefinitionIDs()[id]; !ok {
			t.Fatalf("workflow %q has no definition", id)
		}
	}
	for id := range definitionIDs {
		if _, ok := workflows[id]; !ok {
			t.Fatalf("definition %q has no workflow", id)
		}
	}
}

func readDefinitionIDs() map[string]struct{} {
	ids := map[string]struct{}{}
	for _, definition := range StandardReadDefinitions() {
		ids[definition.ID] = struct{}{}
	}
	return ids
}

func TestWriteFileWorkflowPlan(t *testing.T) {
	plan, err := (WriteFileWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": "a.txt", "content": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	step := plan.Steps[0]
	if step.Capability != "filesystem.write" || step.Operation != "write_file" || step.Input["content"] != "hi" {
		t.Fatalf("unexpected step: %#v", step)
	}
	if _, err = (WriteFileWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{}}); err == nil {
		t.Fatal("missing path accepted")
	}
}

func TestPatchFileWorkflowPlan(t *testing.T) {
	plan, err := (PatchFileWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": "a.txt", "find": "x", "replace": "y", "all": true}})
	if err != nil {
		t.Fatal(err)
	}
	step := plan.Steps[0]
	if step.Operation != "patch_file" || step.Input["all"] != true {
		t.Fatalf("unexpected step: %#v", step)
	}
	if _, err = (PatchFileWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": "a.txt", "replace": "y"}}); err == nil {
		t.Fatal("missing find accepted")
	}
}

func TestMoveFileWorkflowPlan(t *testing.T) {
	if _, err := (MoveFileWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": "a.txt"}}); err == nil {
		t.Fatal("missing target accepted")
	}
	plan, err := (MoveFileWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": "a.txt", "target": "b/c.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[0].Input["target"] != "b/c.txt" {
		t.Fatalf("unexpected step: %#v", plan.Steps[0])
	}
}

func TestRunCommandWorkflowPlan(t *testing.T) {
	plan, err := (RunCommandWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"command": "go", "args": []any{"test", "./..."}}})
	if err != nil {
		t.Fatal(err)
	}
	step := plan.Steps[0]
	if step.Capability != "process.execute" || step.Operation != "run" {
		t.Fatalf("unexpected step: %#v", step)
	}
	args, _ := step.Input["args"].([]any)
	if len(args) != 2 || args[0] != "test" {
		t.Fatalf("args not planned: %#v", step.Input)
	}
	if _, ok := step.Input["cwd"]; ok {
		t.Fatal("absent cwd should not be planned")
	}
	if _, err = (RunCommandWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{}}); err == nil {
		t.Fatal("missing command accepted")
	}
}

func TestGitWorkflowPlans(t *testing.T) {
	plan, err := (GitCreateBranchWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": ".", "name": "experiment"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[0].Capability != "git.write" || plan.Steps[0].Input["name"] != "experiment" {
		t.Fatalf("unexpected step: %#v", plan.Steps[0])
	}
	if _, err = (GitCreateBranchWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": "."}}); err == nil {
		t.Fatal("missing branch name accepted")
	}
	plan, err = (GitCommitWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": ".", "message": "snapshot"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[0].Operation != "commit" || plan.Steps[0].Input["message"] != "snapshot" {
		t.Fatalf("unexpected step: %#v", plan.Steps[0])
	}
	if _, err = (GitCommitWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": "."}}); err == nil {
		t.Fatal("missing message accepted")
	}
}

func TestHTTPPostWorkflowPlan(t *testing.T) {
	plan, err := (HTTPPostWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"url": "https://api.example/items", "body": "{}"}})
	if err != nil {
		t.Fatal(err)
	}
	step := plan.Steps[0]
	if step.Capability != "http.write" || step.Operation != "post" || step.Input["body"] != "{}" {
		t.Fatalf("unexpected step: %#v", step)
	}
	if _, ok := step.Input["content_type"]; ok {
		t.Fatal("absent content_type should not be planned")
	}
	if _, err = (HTTPPostWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{}}); err == nil {
		t.Fatal("missing url accepted")
	}
}
