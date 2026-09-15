package world

import (
	"testing"

	"github.com/temporality-project/temporality/frp/affordance"
)

func TestStandardSemanticRegistryCoversWorkflows(t *testing.T) {
	definitions := StandardSemanticDefinitions()
	workflows := StandardWorkflows()
	if len(definitions) != 4 {
		t.Fatalf("unexpected semantic definition count: %d", len(definitions))
	}
	definitionIDs := map[string]struct{}{}
	for _, definition := range definitions {
		definitionIDs[definition.ID] = struct{}{}
	}
	for id := range workflows {
		if _, ok := definitionIDs[id]; ok {
			continue
		}
		if _, ok := readDefinitionIDs()[id]; ok {
			continue
		}
		if _, ok := writeDefinitionIDs()[id]; !ok {
			t.Fatalf("workflow %q has no definition", id)
		}
	}
	for id := range definitionIDs {
		if _, ok := workflows[id]; !ok {
			t.Fatalf("definition %q has no workflow", id)
		}
	}
}

func semanticDefinitionIDs() map[string]struct{} {
	ids := map[string]struct{}{}
	for _, definition := range StandardSemanticDefinitions() {
		ids[definition.ID] = struct{}{}
	}
	return ids
}

func writeDefinitionIDs() map[string]struct{} {
	ids := map[string]struct{}{}
	for _, definition := range StandardWriteDefinitions() {
		ids[definition.ID] = struct{}{}
	}
	return ids
}

func TestInspectRepositoryWorkflowPlan(t *testing.T) {
	if _, err := (InspectRepositoryWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{}}); err == nil {
		t.Fatal("missing path accepted")
	}
	plan, err := (InspectRepositoryWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": "."}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 4 {
		t.Fatalf("expected 4 steps, got %d", len(plan.Steps))
	}
	want := []struct {
		capability string
		operation  string
	}{
		{"filesystem.read", "stat"},
		{"filesystem.read", "list_dir"},
		{"git.read", "status"},
		{"git.read", "log"},
	}
	for i, expected := range want {
		step := plan.Steps[i]
		if step.Capability != expected.capability || step.Operation != expected.operation {
			t.Fatalf("step %d: got %s/%s, want %s/%s", i, step.Capability, step.Operation, expected.capability, expected.operation)
		}
	}
	if plan.Steps[3].Input["limit"] != 10.0 {
		t.Fatalf("default log limit not planned: %#v", plan.Steps[3].Input)
	}
	plan, err = (InspectRepositoryWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": ".", "log_limit": 3.0}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[3].Input["limit"] != 3.0 {
		t.Fatalf("log_limit override not planned: %#v", plan.Steps[3].Input)
	}
}

func TestRunTestsWorkflowPlan(t *testing.T) {
	plan, err := (RunTestsWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	step := plan.Steps[0]
	if step.Capability != "process.execute" || step.Operation != "run" {
		t.Fatalf("unexpected step: %#v", step)
	}
	if step.Input["command"] != "go" {
		t.Fatalf("default command not planned: %#v", step.Input)
	}
	args, _ := step.Input["args"].([]any)
	if len(args) != 2 || args[0] != "test" || args[1] != "./..." {
		t.Fatalf("default args not planned: %#v", step.Input)
	}
	if _, ok := step.Input["cwd"]; ok {
		t.Fatal("absent path should not plan cwd")
	}
	plan, err = (RunTestsWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{
		"command": "pytest",
		"args":    []any{"-q"},
		"path":    "server",
		"env":     map[string]any{"CI": "true"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	step = plan.Steps[0]
	if step.Input["command"] != "pytest" || step.Input["cwd"] != "server" {
		t.Fatalf("overrides not planned: %#v", step.Input)
	}
	args, _ = step.Input["args"].([]any)
	if len(args) != 1 || args[0] != "-q" {
		t.Fatalf("args override not planned: %#v", step.Input)
	}
	if step.Input["env"].(map[string]any)["CI"] != "true" {
		t.Fatalf("env not planned: %#v", step.Input)
	}
}

func TestReproduceIssueWorkflowPlan(t *testing.T) {
	if _, err := (ReproduceIssueWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{}}); err == nil {
		t.Fatal("missing command accepted")
	}
	plan, err := (ReproduceIssueWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{
		"command": "go",
		"args":    []any{"test", "-run", "TestFailing"},
		"path":    ".",
	}})
	if err != nil {
		t.Fatal(err)
	}
	step := plan.Steps[0]
	if step.Capability != "process.execute" || step.Operation != "run" || step.Input["command"] != "go" {
		t.Fatalf("unexpected step: %#v", step)
	}
	args, _ := step.Input["args"].([]any)
	if len(args) != 3 || args[2] != "TestFailing" {
		t.Fatalf("args not planned: %#v", step.Input)
	}
	if step.Input["cwd"] != "." {
		t.Fatalf("cwd not planned: %#v", step.Input)
	}
}

func TestUpdateConfigurationWorkflowPlan(t *testing.T) {
	if _, err := (UpdateConfigurationWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": "config.yaml", "replace": "y"}}); err == nil {
		t.Fatal("missing find accepted")
	}
	if _, err := (UpdateConfigurationWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{"path": "config.yaml", "find": "x"}}); err == nil {
		t.Fatal("missing replace accepted")
	}
	plan, err := (UpdateConfigurationWorkflow{}.Plan)(affordance.Request{Arguments: map[string]any{
		"path":    "config.yaml",
		"find":    "debug: true",
		"replace": "debug: false",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}
	read, patch := plan.Steps[0], plan.Steps[1]
	if read.Capability != "filesystem.read" || read.Operation != "read_file" {
		t.Fatalf("unexpected read step: %#v", read)
	}
	if patch.Capability != "filesystem.write" || patch.Operation != "patch_file" {
		t.Fatalf("unexpected patch step: %#v", patch)
	}
	if patch.Input["find"] != "debug: true" || patch.Input["replace"] != "debug: false" {
		t.Fatalf("patch arguments not planned: %#v", patch.Input)
	}
	if patch.Input["all"] != false {
		t.Fatalf("patch all must default to first occurrence: %#v", patch.Input)
	}
}
