package agent

import (
	"strings"
	"testing"
)

func TestKnowledgeAreaPromptListsValuesDeterministically(t *testing.T) {
	prompt := knowledgeAreaPrompt(map[string]int{"playwright": 5, "e2e-sandbox-runbook.md": 1, "база данных": 3})
	for _, want := range []string{"playwright (5)", "e2e-sandbox-runbook.md (1)", "база данных (3)"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// Bigger areas first so the judge sees the load-bearing values early.
	above := strings.Index(prompt, "playwright (5)")
	below := strings.Index(prompt, "e2e-sandbox-runbook.md (1)")
	if above > below {
		t.Errorf("values must be ordered by descending size:\n%s", prompt)
	}
}

func TestValidateKnowledgeAreaReply(t *testing.T) {
	values := map[string]int{"playwright": 5, "playwright e2e": 2, "e2e-sandbox-runbook.md": 1, "база данных": 3}
	reply := parseAreaReplyForTest(t, `{"areas": {
		"playwright": {"canonical": "playwright"},
		"playwright e2e": {"canonical": "playwright"},
		"e2e-sandbox-runbook.md": {"hidden": true},
		"база данных": {"canonical": "Databases"},
		"ангстрем-несуществует": {"canonical": "x"}
	}}`)
	result := validateKnowledgeAreaReply(reply, values)
	if len(result.Aliases) != 3 {
		t.Fatalf("aliases = %v, want 3 entries (merge + hidden + rename)", result.Aliases)
	}
	if result.Aliases["playwright e2e"].Canonical != "playwright" {
		t.Errorf("merge decision lost: %v", result.Aliases)
	}
	if !result.Aliases["e2e-sandbox-runbook.md"].Hidden {
		t.Errorf("hidden decision lost: %v", result.Aliases)
	}
	// Canonical is normalized to lowercase.
	if result.Aliases["база данных"].Canonical != "databases" {
		t.Errorf("canonical must be lowercase: %v", result.Aliases)
	}
	// Keep-as-is ("playwright" → "playwright") produced no entry; the
	// hallucinated value was dropped.
	if _, ok := result.Aliases["playwright"]; ok {
		t.Errorf("keep-as-is must not store an entry: %v", result.Aliases)
	}
	if _, ok := result.Aliases["ангстрем-несуществует"]; ok {
		t.Errorf("hallucinated value must be dropped: %v", result.Aliases)
	}
	if result.Merged != 2 || result.Hidden != 1 {
		t.Errorf("merged=%d hidden=%d, want 2/1", result.Merged, result.Hidden)
	}
}

func TestParseKnowledgeAreaReplyRejectsProse(t *testing.T) {
	if _, err := parseKnowledgeAreaReply("playwright — это инструмент тестирования, вот мой анализ областей"); err == nil {
		t.Fatal("prose-only reply must fail")
	}
}

func parseAreaReplyForTest(t *testing.T, raw string) knowledgeAreaReply {
	t.Helper()
	reply, err := parseKnowledgeAreaReply(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return reply
}
