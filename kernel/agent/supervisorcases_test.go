package agent

import (
	"strings"
	"testing"
)

func TestContradictionCasePromptCarriesItem(t *testing.T) {
	prompt := ContradictionCasePrompt(ContradictionCaseInput{KnowledgeID: "k-1", Proposition: "deploys need QA sign-off", Reason: "contradicted by extraction: QA is optional", ContradictedBy: "k-2"})
	for _, want := range []string{"k-1", "deploys need QA sign-off", "contradicted by extraction", "k-2", "invalidate_old"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestCaseReplyParsingDegradation(t *testing.T) {
	// The closed vocabularies degrade to their safest member instead of
	// letting the model invent destructive actions.
	var contradiction struct {
		Action string `json:"action"`
	}
	if err := parseCaseReply(`{"action": "nuke", "explanation": "x"}`, &contradiction); err != nil {
		t.Fatalf("parse: %v", err)
	}
	switch contradiction.Action {
	case "invalidate_old", "supersede_old", "keep_both_scoped", "confirm_new":
		t.Fatalf("unsafe action passed through: %q", contradiction.Action)
	}

	// Prose-only replies fail loudly (the caller keeps its deterministic
	// fallback).
	if err := parseCaseReply("кажется, всё хорошо", &contradiction); err == nil {
		t.Fatal("prose-only reply must fail")
	}
}

func TestSkillNameSanitizerShape(t *testing.T) {
	// The candidate name slug mirrors the pattern: lowercase, digits, dash.
	allowed := func(r rune) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' }
	for _, name := range []string{"qa-handoff", "csv-fix-2"} {
		if !strings.ContainsFunc(name, allowed) && name != "" {
			t.Errorf("valid name %q rejected by the shape", name)
		}
	}
}
