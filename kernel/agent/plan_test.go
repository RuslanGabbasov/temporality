package agent

import (
	"strings"
	"testing"
)

func TestValidatePlanSpecReviewOfAndMaxRework(t *testing.T) {
	tasks := []PlanTask{
		{ID: "build", AgentID: "coder", Prompt: "build it"},
		{ID: "gate", AgentID: "reviewer", Prompt: "review the build", ReviewOf: []string{"build"}},
	}
	dag, rejection := validatePlanSpec(PlanSpec{Tasks: tasks}, nil)
	if rejection != "" {
		t.Fatalf("review gate must validate: %s", rejection)
	}
	// review_of ids become dependencies by definition, and the write-back keeps
	// the task struct in sync for the prompt builder.
	if strings.Join(dag.DependsOn["gate"], ",") != "build" {
		t.Fatalf("review_of must auto-add depends_on: %#v", dag.DependsOn)
	}
	if strings.Join(tasks[1].DependsOn, ",") != "build" {
		t.Fatalf("depends_on write-back missing: %#v", tasks[1])
	}
	selfReview := []PlanTask{{ID: "a", AgentID: "x", Prompt: "p", ReviewOf: []string{"a"}}}
	if _, rejection := validatePlanSpec(PlanSpec{Tasks: selfReview}, nil); rejection == "" || !strings.Contains(rejection, "cannot review itself") {
		t.Fatalf("self review must reject, got: %s", rejection)
	}
	unknownReview := []PlanTask{{ID: "a", AgentID: "x", Prompt: "p", ReviewOf: []string{"ghost"}}}
	if _, rejection := validatePlanSpec(PlanSpec{Tasks: unknownReview}, nil); rejection == "" || !strings.Contains(rejection, "reviews unknown task") {
		t.Fatalf("unknown review target must reject, got: %s", rejection)
	}
	if _, rejection := validatePlanSpec(PlanSpec{Tasks: tasks, MaxRework: MaxPlanRework + 1}, nil); rejection == "" || !strings.Contains(rejection, "max_rework") {
		t.Fatalf("max_rework above the cap must reject, got: %s", rejection)
	}
	if _, rejection := validatePlanSpec(PlanSpec{Tasks: tasks, MaxRework: -1}, nil); rejection == "" || !strings.Contains(rejection, "max_rework") {
		t.Fatalf("negative max_rework must reject, got: %s", rejection)
	}
}

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
	reviewArgs := map[string]any{
		"max_rework": float64(1),
		"tasks": []any{
			map[string]any{"id": "build", "agent_id": "coder", "prompt": "build it"},
			map[string]any{"id": "gate", "agent_id": "reviewer", "prompt": "review", "review_of": []any{"build"}},
		},
	}
	reviewSpec, rejection := planSpecFromArgs(reviewArgs)
	if rejection != "" {
		t.Fatalf("unexpected rejection: %s", rejection)
	}
	if reviewSpec.MaxRework != 1 || strings.Join(reviewSpec.Tasks[1].ReviewOf, ",") != "build" {
		t.Fatalf("review_of/max_rework must parse: %#v", reviewSpec)
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
		{TaskID: "ok", AgentID: "coder", Status: planStatusCompleted, Turns: 3, Round: 2, Verdict: "accept", Answer: "shipped"},
		{TaskID: "boom", AgentID: "coder", Status: planStatusFailed, Error: "model exploded"},
		{TaskID: "later", AgentID: "qa", Status: planStatusSkipped, BlockedBy: "boom"},
	}, []planRejection{{TaskID: "ok", By: "rev", Round: 1, Feedback: "fix the flaky assertion"}})
	for _, expected := range []string{
		"1 completed, 1 failed, 1 skipped, 0 invalidated",
		"## ok (coder) — completed, round 2, verdict accept",
		"shipped",
		"## boom (coder) — failed", "model exploded",
		"## later — skipped (upstream_failed: boom)",
		"Rework history", "- ok rejected by rev (round 1): fix the flaky assertion",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("summary missing %q:\n%s", expected, summary)
		}
	}
	invalidated := planSummary(spec, []planTaskOutcome{{TaskID: "later", AgentID: "qa", Status: planStatusInvalidated, SkipReason: "upstream_rework"}}, nil)
	if !strings.Contains(invalidated, "## later (qa) — invalidated (result discarded: upstream_rework)") {
		t.Fatalf("invalidated outcome missing from summary:\n%s", invalidated)
	}
}
