package agent

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/workspace"
)

func TestParseAgentDraftFull(t *testing.T) {
	raw := "```json\n" + `{
  "name": "Coder",
  "description": "Реализует изменения в коде и проверяет результат тестами.",
  "capabilities": { "modify_files": true, "run_commands": true, "network": false },
  "constraints": ["Не изменять инфраструктуру."],
  "completion": ["Запустить соответствующие тесты."],
  "suggested": { "model": "mimo-v2.6-pro", "sandbox": "standard", "network": true },
  "questions": [{ "field": "constraints", "question": "Можно ли запускать миграции БД?" }, { "field": "", "question": "  " }]
}` + "\n```"
	draft, err := parseAgentDraft(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if draft.Name != "Coder" || draft.Description == "" {
		t.Fatalf("identity not parsed: %+v", draft)
	}
	if *draft.Capabilities.Network != false || *draft.Capabilities.RunCommands != true {
		t.Fatalf("capabilities not parsed: %+v", draft.Capabilities)
	}
	if draft.Capabilities.Skills != nil || draft.Capabilities.Knowledge != nil {
		t.Fatalf("unset capabilities must stay nil (allowed): %+v", draft.Capabilities)
	}
	if len(draft.Constraints) != 1 || len(draft.Completion) != 1 {
		t.Fatalf("constraints/completion not parsed: %+v", draft)
	}
	if draft.Suggested.Sandbox != "standard" || draft.Suggested.Model != "mimo-v2.6-pro" {
		t.Fatalf("suggestion not parsed: %+v", draft.Suggested)
	}
	if len(draft.Questions) != 1 {
		t.Fatalf("blank questions must be dropped, got %+v", draft.Questions)
	}
}

func TestParseAgentDraftFallbacks(t *testing.T) {
	draft, err := parseAgentDraft(`{"description": "Backend development on C#. May run tests."}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if draft.Capabilities != (workspace.AgentCapabilities{}) {
		t.Fatalf("empty capabilities must stay unset (allowed), got %+v", draft.Capabilities)
	}
	if draft.Name != "Backend development on" {
		t.Fatalf("name fallback from purpose expected, got %q", draft.Name)
	}
	if draft.Suggested.Sandbox != "" {
		t.Fatalf("unknown/empty sandbox must be dropped, got %q", draft.Suggested.Sandbox)
	}
	if len(draft.Questions) != 0 || len(draft.Constraints) != 0 {
		t.Fatalf("no questions/constraints expected")
	}
}

func TestParseAgentDraftInvalidJSON(t *testing.T) {
	if _, err := parseAgentDraft("not json at all"); err == nil {
		t.Fatal("invalid JSON must fail")
	}
	if _, err := parseAgentDraft(""); err == nil {
		t.Fatal("empty completion must fail")
	}
}

func TestBuildAgentDraftRequiresDescription(t *testing.T) {
	if _, err := BuildAgentDraft(nil, nil, "  ", ""); err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("blank description must be rejected, got %v", err)
	}
}
