package execution_test

import (
	"errors"
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

func TestClaimLifecycleAndFencing(t *testing.T) {
	at := time.Unix(20, 0).UTC()
	// Initial claim: created → running under a lease, attempt 1.
	first, err := created().Claim("executor-a", at, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != execution.StatusRunning || first.ExecutorID != "executor-a" || first.Attempts != 1 || first.StartedAt == nil || !first.StartedAt.Equal(at) {
		t.Fatalf("initial claim malformed: %#v", first)
	}
	if !first.LeaseHeld(at.Add(time.Second)) {
		t.Fatal("valid lease must hold")
	}
	if first.LeaseHeld(at.Add(31 * time.Second)) {
		t.Fatal("expired lease must not hold")
	}
	// A live lease fences every other executor.
	if _, err = first.Claim("executor-b", at.Add(time.Second), 30*time.Second); !errors.Is(err, execution.ErrLeaseHeld) {
		t.Fatalf("live lease must fence peers: %v", err)
	}
	// The owner re-claiming extends the lease (heartbeat).
	heartbeat, err := first.Claim("executor-a", at.Add(5*time.Second), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !heartbeat.LeaseHeld(at.Add(20 * time.Second)) {
		t.Fatal("heartbeat must extend the lease")
	}
	// After expiry any executor may reclaim; the attempt counter grows and the
	// original start time survives.
	reclaimed, err := heartbeat.Claim("executor-b", at.Add(40*time.Second), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.ExecutorID != "executor-b" || reclaimed.Attempts != 3 || !reclaimed.StartedAt.Equal(*first.StartedAt) {
		t.Fatalf("reclaim malformed: %#v", reclaimed)
	}
	// Ownership fences terminal transitions: the stale owner loses, the
	// reclaiming owner wins.
	if err = heartbeat.ValidateOwnedBy("executor-a"); err != nil {
		t.Fatal("current owner must not be fenced")
	}
	if err = reclaimed.ValidateOwnedBy("executor-a"); !errors.Is(err, execution.ErrFenced) {
		t.Fatalf("stale owner must be fenced: %v", err)
	}
	if err = reclaimed.ValidateOwnedBy("executor-b"); err != nil {
		t.Fatal("reclaiming owner must not be fenced")
	}
	// Terminal executions cannot be claimed again.
	completed, err := reclaimed.Transition(execution.StatusCompleted, at.Add(50*time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = completed.Claim("executor-c", at.Add(60*time.Second), 30*time.Second); err == nil {
		t.Fatal("terminal execution must not be claimable")
	}
	// Lease bookkeeping requires an owner: lease without executor_id is invalid.
	until := at.Add(30 * time.Second)
	invalid := first
	invalid.LeaseUntil = &until
	invalid.ExecutorID = ""
	if err = invalid.Validate(); err == nil {
		t.Fatal("lease without executor_id must fail validation")
	}
}

func TestClaimRequiresIdentityAndPositiveLease(t *testing.T) {
	at := time.Unix(20, 0).UTC()
	if _, err := created().Claim("", at, 30*time.Second); err == nil {
		t.Fatal("empty executor_id must fail")
	}
	if _, err := created().Claim("executor-a", at, 0); err == nil {
		t.Fatal("zero lease must fail")
	}
}
