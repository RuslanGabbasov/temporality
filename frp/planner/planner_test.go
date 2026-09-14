package planner_test

import (
	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/planner"
	"testing"
)

func definition() affordance.Definition {
	return affordance.Definition{Protocol: "frp", Version: "0.3", ID: "investigate", ExecutionMode: affordance.ModeAdaptive, InputSchema: map[string]any{}, Capabilities: []string{"filesystem.read"}, Limits: affordance.Limits{TimeoutSec: 60, CPU: 1, MemoryMB: 256, DiskMB: 128}, Planner: affordance.Planner{Enabled: true, Model: "recorded", MaxSteps: 2}, FailurePolicy: affordance.FailurePolicy{RetryTransient: true, MaxRetries: 1, AllowStrategyChange: true}}
}
func TestPlannerIsBoundedAndPolicyChecked(t *testing.T) {
	state, err := planner.New(planner.Context{ExecutionID: "x", Objective: "inspect", Definition: definition(), MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	allowed := execution.Step{ID: "s1", Capability: "filesystem.read", Operation: "inspect", Input: map[string]any{}}
	state, err = state.Apply(planner.Proposal{Step: &allowed})
	if err != nil {
		t.Fatal(err)
	}
	another := allowed
	another.ID = "s2"
	if _, err = state.Apply(planner.Proposal{Step: &another}); err == nil {
		t.Fatal("step limit bypassed")
	}
	denied := allowed
	denied.Capability = "process.execute"
	fresh, _ := planner.New(planner.Context{ExecutionID: "y", Objective: "inspect", Definition: definition(), MaxSteps: 1})
	if _, err = fresh.Apply(planner.Proposal{Step: &denied}); err == nil {
		t.Fatal("undeclared capability accepted")
	}
}
func TestRecordedPlannerCompletesDeterministically(t *testing.T) {
	adapter := &planner.RecordedAdapter{Proposals: []planner.Proposal{{Complete: true, Summary: "done"}}}
	proposal, err := adapter.Next(planner.Context{})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := planner.New(planner.Context{ExecutionID: "x", Objective: "inspect", Definition: definition(), MaxSteps: 2})
	state, err = state.Apply(proposal)
	if err != nil || !state.Complete || state.Summary != "done" {
		t.Fatalf("unexpected completion: %#v %v", state, err)
	}
}
