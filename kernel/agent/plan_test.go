package agent

import (
	"strings"
	"testing"
)

func TestPlanSpecFromArgs(t *testing.T) {
	args := map[string]any{
		"goal": "ship it",
		"tasks": []any{
			map[string]any{"id": "dev-api", "agent_id": "coder", "prompt": "build the api", "max_turns": float64(4)},
			map[string]any{"id": "qa", "agent_id": "qa", "prompt": "test everything", "depends_on": []any{"dev-api", "dev-api"}},
		},
	}
	spec, rejection := planSpecFromArgs(args)
	if rejection != "" {
		t.Fatalf("unexpected rejection: %s", rejection)
	}
	if spec.Goal != "ship it" || len(spec.Tasks) != 2 {
		t.Fatalf("unexpected spec: %#v", spec)
	}
	if spec.Tasks[0].MaxTurns != 4 {
		t.Fatalf("max_turns must parse: %#v", spec.Tasks[0])
	}
	if strings.Join(spec.Tasks[1].DependsOn, ",") != "dev-api,dev-api" {
		t.Fatalf("depends_on must parse: %#v", spec.Tasks[1])
	}
	if _, rejection := planSpecFromArgs(map[string]any{"tasks": []any{}}); rejection == "" {
		t.Fatal("empty tasks must reject")
	}
	if _, rejection := planSpecFromArgs(map[string]any{}); rejection == "" {
		t.Fatal("missing tasks must reject")
	}
}

func TestValidatePlanSpec(t *testing.T) {
	valid := func(tasks ...PlanTask) []PlanTask { return tasks }
	cases := []struct {
		name      string
		tasks     []PlanTask
		rejection string
	}{
		{"valid chain", valid(
			PlanTask{ID: "a", AgentID: "x", Prompt: "p"},
			PlanTask{ID: "b", AgentID: "x", Prompt: "p", DependsOn: []string{"a"}},
			PlanTask{ID: "c", AgentID: "x", Prompt: "p", DependsOn: []string{"a", "b"}},
		), ""},
		{"duplicate id", valid(
			PlanTask{ID: "a", AgentID: "x", Prompt: "p"},
			PlanTask{ID: "a", AgentID: "x", Prompt: "p"},
		), "duplicate"},
		{"unknown dep", valid(
			PlanTask{ID: "a", AgentID: "x", Prompt: "p", DependsOn: []string{"ghost"}},
		), "unknown"},
		{"self dep", valid(
			PlanTask{ID: "a", AgentID: "x", Prompt: "p", DependsOn: []string{"a"}},
		), "itself"},
		{"cycle", valid(
			PlanTask{ID: "a", AgentID: "x", Prompt: "p", DependsOn: []string{"b"}},
			PlanTask{ID: "b", AgentID: "x", Prompt: "p", DependsOn: []string{"a"}},
		), "cycle"},
		{"bad id charset", valid(PlanTask{ID: "bad id!", AgentID: "x", Prompt: "p"}), "invalid"},
		{"missing agent", valid(PlanTask{ID: "a", Prompt: "p"}), "agent_id"},
		{"missing prompt", valid(PlanTask{ID: "a", AgentID: "x"}), "prompt"},
		{"bad max turns", valid(PlanTask{ID: "a", AgentID: "x", Prompt: "p", MaxTurns: 99}), "max_turns"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, rejection := validatePlanSpec(PlanSpec{Tasks: testCase.tasks}, nil)
			if testCase.rejection == "" && rejection != "" {
				t.Fatalf("expected valid, got: %s", rejection)
			}
			if testCase.rejection != "" && !strings.Contains(rejection, testCase.rejection) {
				t.Fatalf("expected rejection containing %q, got: %s", testCase.rejection, rejection)
			}
		})
	}
}

func TestValidatePlanSpecTemplateRefs(t *testing.T) {
	tasks := []PlanTask{
		{ID: "a", AgentID: "x", Prompt: "p"},
		{ID: "b", AgentID: "x", Prompt: "summarize {{a.answer}}", DependsOn: []string{"a"}},
	}
	if _, rejection := validatePlanSpec(PlanSpec{Tasks: tasks}, nil); rejection != "" {
		t.Fatalf("dependency reference must validate: %s", rejection)
	}
	tasks[1].DependsOn = nil
	if _, rejection := validatePlanSpec(PlanSpec{Tasks: tasks}, nil); rejection == "" || !strings.Contains(rejection, "not a dependency") {
		t.Fatalf("same-plan non-dependency reference must reject, got: %s", rejection)
	}
	unknown := []PlanTask{{ID: "a", AgentID: "x", Prompt: "use {{ghost.answer}}"}}
	if _, rejection := validatePlanSpec(PlanSpec{Tasks: unknown}, nil); rejection == "" || !strings.Contains(rejection, "neither a dependency") {
		t.Fatalf("unknown reference must reject, got: %s", rejection)
	}
	prior := []PlanTask{{ID: "tail", AgentID: "x", Prompt: "continue {{head.answer}}"}}
	if _, rejection := validatePlanSpec(PlanSpec{Tasks: prior}, map[string]string{"head": "HEAD-RESULT"}); rejection != "" {
		t.Fatalf("prior-plan reference must validate: %s", rejection)
	}
}

func TestPlanTaskPrompt(t *testing.T) {
	task := PlanTask{ID: "rev", AgentID: "reviewer", Prompt: "review the diff", DependsOn: []string{"a", "b"}}
	upstream := map[string]string{"a": "A-DONE", "b": "B-DONE"}
	appended := planTaskPrompt(task, upstream, nil)
	if !strings.Contains(appended, "review the diff") || !strings.Contains(appended, "## a\nA-DONE") || !strings.Contains(appended, "## b\nB-DONE") {
		t.Fatalf("auto-append composition broken: %q", appended)
	}
	templated := PlanTask{ID: "rev", AgentID: "reviewer", Prompt: "check {{a.answer}} then {{b.answer}}", DependsOn: []string{"a", "b"}}
	inlined := planTaskPrompt(templated, upstream, nil)
	if inlined != "check A-DONE then B-DONE" {
		t.Fatalf("template interpolation broken: %q", inlined)
	}
	missing := planTaskPrompt(templated, map[string]string{"a": "A-DONE"}, nil)
	if !strings.Contains(missing, "[unavailable: b]") {
		t.Fatalf("missing dependency must inline a marker: %q", missing)
	}
	prior := planTaskPrompt(PlanTask{ID: "t", AgentID: "x", Prompt: "was: {{head.answer}}"}, nil, map[string]string{"head": "HEAD-RESULT"})
	if prior != "was: HEAD-RESULT" {
		t.Fatalf("prior-plan interpolation broken: %q", prior)
	}
}

func TestPlanSummary(t *testing.T) {
	spec := PlanSpec{Goal: "release", Tasks: []PlanTask{
		{ID: "ok", AgentID: "coder", Prompt: "p"},
		{ID: "boom", AgentID: "coder", Prompt: "p"},
		{ID: "later", AgentID: "qa", Prompt: "p"},
	}}
	summary := planSummary(spec, []planTaskOutcome{
		{TaskID: "ok", AgentID: "coder", Status: planStatusCompleted, Turns: 3, Answer: "shipped"},
		{TaskID: "boom", AgentID: "coder", Status: planStatusFailed, Error: "model exploded"},
		{TaskID: "later", AgentID: "qa", Status: planStatusSkipped, BlockedBy: "boom"},
	})
	for _, expected := range []string{"1 completed, 1 failed, 1 skipped", "## ok (coder) — completed", "shipped", "## boom (coder) — failed", "model exploded", "## later — skipped (upstream_failed: boom)"} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("summary missing %q:\n%s", expected, summary)
		}
	}
}
