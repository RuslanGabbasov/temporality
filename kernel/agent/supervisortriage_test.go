package agent

import (
	"strings"
	"testing"
)

func TestParseSupervisorTriageReply(t *testing.T) {
	reply := `Ниже мой диагноз.
` + "```json" + `
{"cause_class": "SANDBOX_LIMIT", "summary": "Сборка упала по лимиту памяти контейнера.", "recommendation": "Поднимите память профиля песочницы.", "evidence": ["tool.failed:go build"]}
` + "```" + `
Дополнительные пояснения.`
	verdict, err := parseSupervisorTriageReply(reply)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if verdict.CauseClass != CauseSandboxLimit {
		t.Errorf("class = %v", verdict.CauseClass)
	}
	if !strings.Contains(verdict.Summary, "лимит") {
		t.Errorf("summary = %q", verdict.Summary)
	}
	if len(verdict.Evidence) != 1 || verdict.Evidence[0] != "tool.failed:go build" {
		t.Errorf("evidence = %v", verdict.Evidence)
	}
}

func TestParseSupervisorTriageReplyUnknownClassDegradesToHarnessBug(t *testing.T) {
	verdict, err := parseSupervisorTriageReply(`{"cause_class": "космос", "summary": "неизвестно"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if verdict.CauseClass != CauseHarnessBug {
		t.Errorf("class = %v, want harness_bug fallback", verdict.CauseClass)
	}
}

func TestParseSupervisorTriageReplyRejectsProse(t *testing.T) {
	if _, err := parseSupervisorTriageReply("Похоже, ран упал из-за сети."); err == nil {
		t.Fatal("prose-only reply must fail")
	}
}
