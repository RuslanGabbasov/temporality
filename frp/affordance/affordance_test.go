package affordance_test

import (
	"testing"

	"github.com/temporality-project/temporality/frp/affordance"
)

func validDefinition(mode affordance.ExecutionMode) affordance.Definition {
	d := affordance.Definition{ID: "run_test", ExecutionMode: mode, InputSchema: map[string]any{"type": "object"}, Capabilities: []string{"process.execute"}, Limits: affordance.Limits{TimeoutSec: 30, CPU: 1, MemoryMB: 128, DiskMB: 64}, FailurePolicy: affordance.FailurePolicy{RetryTransient: true, MaxRetries: 1}}
	d.ApplyDefaults()
	if mode == affordance.ModeAdaptive {
		d.Planner = affordance.Planner{Enabled: true, Model: "planner-v1", MaxSteps: 5}
		d.FailurePolicy.AllowStrategyChange = true
	}
	return d
}

func TestDefinitionModes(t *testing.T) {
	for _, mode := range []affordance.ExecutionMode{affordance.ModeDeterministic, affordance.ModeAdaptive} {
		if err := validDefinition(mode).Validate(); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	d := validDefinition(affordance.ModeDeterministic)
	d.Planner.Enabled = true
	if err := d.Validate(); err == nil {
		t.Fatal("deterministic planner must be rejected")
	}
}

func TestDefinitionValidation(t *testing.T) {
	tests := []func(*affordance.Definition){
		func(d *affordance.Definition) { d.Capabilities = append(d.Capabilities, d.Capabilities[0]) },
		func(d *affordance.Definition) { d.Limits.TimeoutSec = 0 },
		func(d *affordance.Definition) { d.FailurePolicy.RetryTransient = false },
	}
	for i, mutate := range tests {
		d := validDefinition(affordance.ModeDeterministic)
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Fatalf("case %d should fail", i)
		}
	}
}

func TestSemanticRequest(t *testing.T) {
	d := validDefinition(affordance.ModeDeterministic)
	r := affordance.Request{RequestID: "request-1", EpisodeID: "episode-1", AffordanceID: "run_test", Arguments: map[string]any{"suite": "unit"}}
	r.ApplyDefaults()
	if err := affordance.ValidateRequest(d, r); err != nil {
		t.Fatal(err)
	}
	r.AffordanceID = "shell"
	if err := affordance.ValidateRequest(d, r); err == nil {
		t.Fatal("mismatched semantic request must fail")
	}
}
