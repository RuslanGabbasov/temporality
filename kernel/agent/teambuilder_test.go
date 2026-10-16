package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

func TestParseTeamDraftFull(t *testing.T) {
	raw := "```json\n" + `{
  "name": "Code Delivery",
  "description": "Разработчик реализует задачу, ревьюер проверяет изменение.",
  "manifest": {
    "protocol": { "kind": "pipeline", "max_rework": 2 },
    "slots": [
      { "id": "developer", "title": "Developer", "responsibility": "Реализация",
        "requirements": { "capabilities": ["Read Files", "modify-files", "run_commands"], "skills": [" code-review ", ""] },
        "contract": "Готовая правка с тестами",
        "binding": { "mode": "role" } },
      { "id": "reviewer", "title": "Reviewer", "responsibility": "Проверка",
        "contract": "Вердикт ревью",
        "binding": { "mode": "fixed", "agent_id": " reviewer-1 " } }
    ],
    "defaults": { "max_turns": -5, "budget_usd": 3, "parallel_limit": 2 }
  },
  "questions": [{ "field": "manifest.slots", "question": "Нужен ли отдельный QA?" }, { "field": "", "question": "  " }]
}` + "\n```"
	draft, err := parseTeamDraft(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if draft.Name != "Code Delivery" {
		t.Fatalf("name = %q", draft.Name)
	}
	if len(draft.Questions) != 1 {
		t.Fatalf("questions = %+v, want 1 trimmed question", draft.Questions)
	}
	var manifest workspace.TeamManifest
	if err := json.Unmarshal(draft.Manifest, &manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if manifest.Protocol.Kind != workspace.TeamProtocolPipeline || manifest.Protocol.MaxRework != 2 {
		t.Fatalf("protocol = %+v", manifest.Protocol)
	}
	if manifest.Defaults.MaxTurns != 0 || manifest.Defaults.BudgetUSD != 3 {
		t.Fatalf("defaults = %+v, want negative clamped to 0", manifest.Defaults)
	}
	if len(manifest.Slots) != 2 {
		t.Fatalf("slots = %d, want 2", len(manifest.Slots))
	}
	dev := manifest.Slots[0]
	if dev.Binding.Mode != workspace.TeamBindingRole {
		t.Fatalf("developer binding = %+v, want role", dev.Binding)
	}
	wantCaps := []string{"read_files", "modify_files", "run_commands"}
	if strings.Join(dev.Requirements.Capabilities, ",") != strings.Join(wantCaps, ",") {
		t.Fatalf("capabilities = %v, want %v (normalized, ordered)", dev.Requirements.Capabilities, wantCaps)
	}
	if strings.Join(dev.Requirements.Skills, ",") != "code-review" {
		t.Fatalf("skills = %v, want trimmed [code-review]", dev.Requirements.Skills)
	}
	rev := manifest.Slots[1]
	if rev.Binding.Mode != workspace.TeamBindingFixed || rev.Binding.AgentID != "reviewer-1" {
		t.Fatalf("reviewer binding = %+v, want fixed reviewer-1", rev.Binding)
	}
}

func TestParseTeamDraftNormalizesEnums(t *testing.T) {
	draft, err := parseTeamDraft(`{
  "name": "Research",
  "description": "Параллельное исследование со сборкой результата.",
  "manifest": {
    "protocol": { "kind": "Fan-Out" },
    "slots": [
      { "id": "researcher-a", "requirements": { "capabilities": ["write_files"] }, "binding": { "mode": "By Role" } },
      { "id": "researcher-b", "binding": { "mode": "role" } },
      { "id": "synthesizer", "binding": { "mode": "fixed", "agent_id": "" } }
    ]
  }
}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var manifest workspace.TeamManifest
	if err := json.Unmarshal(draft.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Protocol.Kind != workspace.TeamProtocolFanOut {
		t.Fatalf("protocol = %q, want fan_out", manifest.Protocol.Kind)
	}
	if manifest.Slots[0].Binding.Mode != workspace.TeamBindingRole {
		t.Fatalf("by role not normalized: %+v", manifest.Slots[0].Binding)
	}
	if got := manifest.Slots[0].Requirements.Capabilities; len(got) != 1 || got[0] != "modify_files" {
		t.Fatalf("write_files alias not applied: %v", got)
	}
	// A fixed binding without an agent is downgraded to a role binding
	// instead of failing validation wholesale.
	if manifest.Slots[2].Binding.Mode != workspace.TeamBindingRole {
		t.Fatalf("agentless fixed binding not downgraded: %+v", manifest.Slots[2].Binding)
	}
}

func TestParseTeamDraftReasoningWrapped(t *testing.T) {
	draft, err := parseTeamDraft("<think>нужен конвейер…</think>\n" + `{"name":"Delivery","description":"Правки и ревью.","manifest":{"protocol":{"kind":"pipeline"},"slots":[{"id":"dev","binding":{"mode":"role"}}]}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if draft.Name != "Delivery" {
		t.Fatalf("name = %q", draft.Name)
	}
}

func TestParseTeamDraftNameFallback(t *testing.T) {
	draft, err := parseTeamDraft(`{"description": "Команда доставки кода в репозиторий. Полный цикл.", "manifest": {"protocol": {"kind": "pipeline"}, "slots": [{"id": "dev", "binding": {"mode": "role"}}]}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if draft.Name != "Команда доставки кода" {
		t.Fatalf("name fallback = %q", draft.Name)
	}
}

func TestParseTeamDraftRejectsInvalidManifests(t *testing.T) {
	cases := map[string]string{
		"unknown protocol": `{"name":"X","manifest":{"protocol":{"kind":"chaos"},"slots":[{"id":"dev","binding":{"mode":"role"}}]}}`,
		"no slots":         `{"name":"X","manifest":{"protocol":{"kind":"pipeline"},"slots":[]}}`,
		"no binding":       `{"name":"X","manifest":{"protocol":{"kind":"pipeline"},"slots":[{"id":"dev"}]}}`,
		"not json":         "команда без json",
	}
	for name, raw := range cases {
		if _, err := parseTeamDraft(raw); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestBuildTeamDraftRequiresModelAndDescription(t *testing.T) {
	if _, err := BuildTeamDraft(nil, nil, "описание команды", "", false); err == nil {
		t.Fatal("nil model must fail")
	}
	if _, err := BuildTeamDraft(nil, &llm.Client{}, "  ", "roster", false); err == nil {
		t.Fatal("empty description must fail")
	}
}

func TestFormatTeamRoster(t *testing.T) {
	yes, no := true, false
	agents := []workspace.Agent{
		{ID: "coder", Name: "Coder", Description: "Пишет код и прогоняет тесты.", Skills: []string{"code-review", "tests"}},
		{ID: "researcher", Name: "Researcher", Definition: &workspace.AgentDefinition{Capabilities: workspace.AgentCapabilities{
			ModifyFiles: &no, RunCommands: &no, Delegation: &yes,
		}}},
	}
	summary := FormatTeamRoster(agents)
	for _, want := range []string{"- coder (Coder)", "code-review", "researcher (Researcher)", "denied: modify_files, run_commands"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("roster summary missing %q:\n%s", want, summary)
		}
	}
}
