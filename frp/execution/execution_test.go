package execution_test

import (
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
)

func created() execution.Execution {
	return execution.Execution{Protocol: "frp", Version: "0.3", ExecutionID: "x1", RequestID: "r1", EpisodeID: "e1", AffordanceID: "run_test", Status: execution.StatusCreated, CreatedEventID: "event-1", IntentPersistedAt: time.Unix(10, 0).UTC()}
}

func TestStrictTransitions(t *testing.T) {
	e := created()
	running, err := e.Transition(execution.StatusRunning, time.Unix(11, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := running.Transition(execution.StatusCompleted, time.Unix(12, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !completed.Status.Terminal() {
		t.Fatal("completed must be terminal")
	}
	if _, err := e.Transition(execution.StatusCompleted, time.Unix(11, 0), nil); err == nil {
		t.Fatal("created -> completed must fail")
	}
	if _, err := completed.Transition(execution.StatusRunning, time.Unix(13, 0), nil); err == nil {
		t.Fatal("terminal transition must fail")
	}
}

func TestFailureRequiresNormalizedError(t *testing.T) {
	running, _ := created().Transition(execution.StatusRunning, time.Unix(11, 0), nil)
	if _, err := running.Transition(execution.StatusFailed, time.Unix(12, 0), nil); err == nil {
		t.Fatal("missing error must fail")
	}
	normalized := &execution.ExecutionError{Class: execution.ErrorResourceExhausted, Message: "disk full", Retryable: true, Resource: "disk"}
	if _, err := running.Transition(execution.StatusFailed, time.Unix(12, 0), normalized); err != nil {
		t.Fatal(err)
	}
}

func TestPersistBeforeEffect(t *testing.T) {
	e := created()
	e.CreatedEventID = ""
	if err := e.Validate(); err == nil {
		t.Fatal("unpersisted intent must fail")
	}
	running, _ := created().Transition(execution.StatusRunning, time.Unix(9, 0), nil)
	if err := execution.ValidateEffectBoundary(running); err == nil {
		t.Fatal("effect before persistence must fail")
	}
	running, _ = created().Transition(execution.StatusRunning, time.Unix(11, 0), nil)
	if err := execution.ValidateEffectBoundary(running); err != nil {
		t.Fatal(err)
	}
}

func TestPlanCapabilitiesAndCanonicalEvents(t *testing.T) {
	d := affordance.Definition{Capabilities: []string{"process.execute"}}
	p := execution.Plan{Steps: []execution.Step{{ID: "test", Capability: "process.execute", Operation: "configured_test", Input: map[string]any{}}}}
	if err := p.Validate(d); err != nil {
		t.Fatal(err)
	}
	p.Steps[0].Capability = "shell.arbitrary"
	if err := p.Validate(d); err == nil {
		t.Fatal("undeclared capability must fail")
	}
	for _, eventType := range execution.CanonicalEventTypes() {
		if !execution.IsCanonicalEventType(eventType) {
			t.Fatalf("%s not canonical", eventType)
		}
	}
	if eventType, ok := execution.EventTypeForStatus(execution.StatusFailed); !ok || eventType != execution.EventExecutionFailed {
		t.Fatal("wrong status event")
	}
}
