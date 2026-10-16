package workspace

import (
	"testing"
	"time"
)

// TestBuildQualitySummaryGuards: pass_rate and override_rate stay 0 when their
// denominators are 0 — an empty suite must not report a fake 100 %.
func TestBuildQualitySummaryGuards(t *testing.T) {
	agents := []Agent{{ID: "analyst", Name: "Analyst", ProjectID: "omni"}}
	lastEval := map[string]AgentEvaluationRun{
		"analyst": {Verdict: "error", Passed: 0, Failed: 0, CreatedAt: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)},
	}
	summary := BuildQualitySummary(agents, lastEval, nil, nil, nil, map[string]int{}, 3, 1, 7)
	if len(summary.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(summary.Agents))
	}
	eval := summary.Agents[0].LastEvaluation
	if eval == nil {
		t.Fatal("expected last evaluation to be present")
	}
	if eval.PassRate != 0 {
		t.Fatalf("expected guarded pass_rate 0, got %f", eval.PassRate)
	}
	if eval.JudgeAgreement != nil {
		t.Fatalf("expected nil judge agreement without calibration, got %f", *eval.JudgeAgreement)
	}
	if summary.Loop.OverrideRate != 0 {
		t.Fatalf("expected guarded override_rate 0, got %f", summary.Loop.OverrideRate)
	}
	if summary.Loop.WindowDays != 7 || summary.Loop.EvaluationRuns != 3 || summary.Loop.JudgeDegradedRuns != 1 {
		t.Fatalf("unexpected loop counters: %+v", summary.Loop)
	}
}

// TestBuildQualitySummaryRates: pass_rate and override_rate are computed and
// rounded like the quality report; judge agreement attaches by judge model.
func TestBuildQualitySummaryRates(t *testing.T) {
	agents := []Agent{{ID: "analyst", Name: "Analyst", ProjectID: "omni", DefinitionVersion: 4}}
	lastEval := map[string]AgentEvaluationRun{
		"analyst": {Verdict: "pass", Passed: 8, Failed: 2, JudgeModel: "gpt-judge", CreatedAt: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)},
	}
	agreement := 0.91234
	summary := BuildQualitySummary(agents, lastEval, map[string]float64{"gpt-judge": agreement},
		map[string]int{"analyst": 1}, map[string]int{"analyst": 2},
		map[string]int{gateEventPassed: 3, gateEventBlocked: 1, gateEventOverridden: 1}, 4, 0, 7)
	row := summary.Agents[0]
	if row.PendingProposals != 1 || row.StaleGoldens != 2 || row.DefinitionVersion != 4 || row.Project != "omni" {
		t.Fatalf("unexpected agent row: %+v", row)
	}
	eval := row.LastEvaluation
	if eval.PassRate != 0.8 {
		t.Fatalf("expected pass_rate 0.8, got %f", eval.PassRate)
	}
	if eval.JudgeAgreement == nil || *eval.JudgeAgreement != agreement {
		t.Fatalf("expected judge agreement %f, got %+v", agreement, eval.JudgeAgreement)
	}
	// 1 override of 3+1+1 decisions = 0.2
	if summary.Loop.GatePassed != 3 || summary.Loop.GateBlocked != 1 || summary.Loop.GateOverridden != 1 {
		t.Fatalf("unexpected gate counts: %+v", summary.Loop)
	}
	if summary.Loop.OverrideRate != 0.2 {
		t.Fatalf("expected override_rate 0.2, got %f", summary.Loop.OverrideRate)
	}
}

// TestBuildQualitySummarySortingAndNilMaps: agents sort by id regardless of
// input order, and nil maps produce zero counts without panics; agents without
// an evaluation report no last_evaluation at all.
func TestBuildQualitySummarySortingAndNilMaps(t *testing.T) {
	agents := []Agent{
		{ID: "zeta", Name: "Zeta"},
		{ID: "alpha", Name: "Alpha"},
		{ID: "mid", Name: "Mid", DefinitionVersion: 2},
	}
	summary := BuildQualitySummary(agents, nil, nil, nil, nil, nil, 10, 2, 7)
	want := []string{"alpha", "mid", "zeta"}
	for i, id := range want {
		if summary.Agents[i].AgentID != id {
			t.Fatalf("expected %s at %d, got %s", id, i, summary.Agents[i].AgentID)
		}
	}
	for _, row := range summary.Agents {
		if row.LastEvaluation != nil {
			t.Fatalf("expected no last evaluation for %s", row.AgentID)
		}
		if row.PendingProposals != 0 || row.StaleGoldens != 0 {
			t.Fatalf("expected zero counts for %s, got %+v", row.AgentID, row)
		}
	}
	if summary.Loop.EvaluationRuns != 10 || summary.Loop.JudgeDegradedRuns != 2 {
		t.Fatalf("unexpected window counts: %+v", summary.Loop)
	}
}
