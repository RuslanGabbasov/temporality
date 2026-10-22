package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
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

// The C4 judge must be able to reject a candidate, not only draft skills:
// most recurring pairs are generic navigation, and the old prompt forced a
// skill draft out of every pair.
func TestSkillCandidateCaseRejection(t *testing.T) {
	var prompt string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		for _, m := range payload.Messages {
			if m.Role == "user" {
				prompt = m.Content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"worth_skill": false, "explanation": "два чтения подряд — навигация, а не процедура"}`,
			}}},
		})
	}))
	t.Cleanup(model.Close)
	verdict, err := SkillCandidateCase(context.Background(), llm.New(llm.Config{BaseURL: model.URL, Model: "m"}), SkillCandidateCaseInput{
		Signature: "artifact_read → saga_list", Runs: 3,
	})
	if err != nil {
		t.Fatalf("SkillCandidateCase: %v", err)
	}
	if verdict.WorthSkill {
		t.Fatal("a rejection must keep worth_skill false")
	}
	if !strings.Contains(prompt, "worth_skill") {
		t.Fatal("the prompt must license the judge to reject")
	}
}

// A rejection without a reason is unauditable — fail loudly like the other
// case prompts do for empty explanations.
func TestSkillCandidateCaseRejectionRequiresExplanation(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"worth_skill": false}`,
			}}},
		})
	}))
	t.Cleanup(model.Close)
	if _, err := SkillCandidateCase(context.Background(), llm.New(llm.Config{BaseURL: model.URL, Model: "m"}), SkillCandidateCaseInput{Signature: "a → b", Runs: 3}); err == nil {
		t.Fatal("an unexplained rejection must fail")
	}
}

// An approval still requires a usable name and description.
func TestSkillCandidateCaseApproval(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"worth_skill": true, "skill_name": "PR Checklist", "description": "Финализация работы через PR", "steps": ["git push"]}`,
			}}},
		})
	}))
	t.Cleanup(model.Close)
	verdict, err := SkillCandidateCase(context.Background(), llm.New(llm.Config{BaseURL: model.URL, Model: "m"}), SkillCandidateCaseInput{Signature: "skill_search → run_command", Runs: 3})
	if err != nil {
		t.Fatalf("SkillCandidateCase: %v", err)
	}
	if !verdict.WorthSkill || verdict.SkillName != "pr-checklist" {
		t.Fatalf("verdict = %#v", verdict)
	}
}
