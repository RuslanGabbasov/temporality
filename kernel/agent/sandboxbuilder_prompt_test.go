package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// D2.2/AC-7: the draft contract the model answers with never carries a
// recipe field — the operator override cannot travel through the model path.
func TestSandboxDraftContractHasNoRecipeField(t *testing.T) {
	typ := reflect.TypeOf(SandboxDraft{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		shape := strings.ToLower(field.Name + " " + field.Tag.Get("json"))
		if strings.Contains(shape, "dockerfile") || strings.Contains(shape, "override") {
			t.Errorf("SandboxDraft must not carry a recipe field, found %q", field.Name)
		}
	}
}

// D2.2: even if the model answers with a dockerfile_override field, the
// draft contract drops it — the recipe can never travel through the
// model-driven path.
func TestParseSandboxDraftDropsRecipeField(t *testing.T) {
	parse := parseSandboxDraft([]string{"python"})
	draft, err := parse(`{"id":"qa-tools","name":"QA","description":"d","base":"python","dockerfile_override":"FROM mcr.microsoft.com/x:latest\n","image":"repo@sha256:` + strings.Repeat("ab", 32) + `"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if draft.Base != "python" || draft.ID != "qa-tools" {
		t.Fatalf("unexpected draft: %+v", draft)
	}
	raw, marshalErr := json.Marshal(draft)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(raw), "dockerfile") || strings.Contains(string(raw), strings.Repeat("ab", 32)) {
		t.Errorf("recipe fields leaked through the model path: %s", raw)
	}
}

// D5/AC-7: the master is told to propose only packages it is confident exist
// in the base index and to omit the rest — with the pentest-tooling rule
// spelled out — and never to design Dockerfiles.
func TestSandboxDraftPromptForbidsGuessingAndOverride(t *testing.T) {
	prompt := sandboxDraftPrompt("qa agent for a web service", "- node: Node 22")
	for _, want := range []string{"not certain", "OMIT", "sqlmap", "You never design or edit Dockerfiles"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "dockerfile_override") {
		t.Error("prompt must not expose the operator override field")
	}
	// The JSON contract asks only for form fields — no recipe text.
	idx := strings.Index(prompt, "Respond with one JSON object:")
	if idx < 0 {
		t.Fatal("prompt lost its JSON contract")
	}
	if contract := prompt[idx:]; strings.Contains(contract, "dockerfile") {
		t.Errorf("contract must not ask for a dockerfile: %s", contract)
	}
}
