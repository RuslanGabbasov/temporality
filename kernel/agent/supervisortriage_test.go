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

func TestParseSupervisorTriageReplyPatternOptional(t *testing.T) {
	verdict, err := parseSupervisorTriageReply(`{"cause_class": "harness_bug", "pattern": "cpu range", "summary": "s"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if verdict.Pattern != "cpu range" {
		t.Errorf("pattern = %q", verdict.Pattern)
	}
	verdict, err = parseSupervisorTriageReply(`{"cause_class": "harness_bug", "summary": "s"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if verdict.Pattern != "" {
		t.Errorf("pattern must be optional, got %q", verdict.Pattern)
	}
}

func TestSupervisorClusterKeyNormalizesVaryingNumbers(t *testing.T) {
	a := SupervisorTraceRef{ErrorType: "StartToClose", Error: "range of CPUs is from 0.01 to 1.00"}
	b := SupervisorTraceRef{ErrorType: "StartToClose", Error: "range of CPUs is from 0.05 to 2.00"}
	if SupervisorClusterKey(a) != SupervisorClusterKey(b) {
		t.Fatalf("same-kind failures must share the cluster key:\n%q\n%q", SupervisorClusterKey(a), SupervisorClusterKey(b))
	}
	c := SupervisorTraceRef{ErrorType: "StartToClose", Error: "tmpfs limit exceeded: /tmp"}
	if SupervisorClusterKey(a) == SupervisorClusterKey(c) {
		t.Fatal("different failure kinds must not share a cluster")
	}
	d := SupervisorTraceRef{ErrorType: "ActivityError", Error: "range of CPUs is from 0.01 to 1.00"}
	if SupervisorClusterKey(a) == SupervisorClusterKey(d) {
		t.Fatal("error type belongs in the key")
	}
}
