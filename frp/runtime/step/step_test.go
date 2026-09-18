package step_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/runtime/step"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func TestMemoryStepSuccessAndReferenceRollback(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	current := testFrame(now)
	created := testEvent("00000000-0000-4000-8000-000000000006", "frame.created", now, current)
	created.Payload["frame_id"] = current.FrameID
	if err := store.CreateFrame(ctx, current, created); err != nil {
		t.Fatal(err)
	}
	seed := testEvent("00000000-0000-4000-8000-000000000007", "observation.recorded", now, current)
	if err := store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}

	def := testDefinition()
	emission := cognition.CognitiveEmission{
		EmissionID: "emission-success", FrameID: current.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "available"}, {Ref: "query:WORK", Interpretation: "visible focus query"}},
		Claims:      []cognition.EmittedClaim{{Proposition: "work is available", Confidence: 0.9}},
		Attention:   []cognition.AttentionOperation{{Op: "attend", Target: cognition.AttentionTarget{Type: frame.RefQuery, Text: "next work"}}},
		Actions:     []cognition.ActionRequest{{Affordance: def.ID, Args: map[string]any{"task": "test"}}},
	}
	result, err := step.Run(ctx, store, step.Input{Current: current, Emission: emission, Definitions: map[string]affordance.Definition{def.ID: def}, NewID: uuidSequence(100), Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Claims) != 1 || len(result.Executions) != 1 || len(result.Events) != 6 || result.Events[1].Type != step.EventFocusChanged || result.Events[2].Type != step.EventAttentionSelected || result.Events[5].Type != step.EventFrameTransitioned {
		t.Fatalf("unexpected step result: %#v", result)
	}
	// The attendance moved focus "work" → "next work": the investigation log
	// must carry from/to with the deliberate trigger.
	if from, _ := result.Events[1].Payload["from"], result.Events[1].Payload["to"]; from != "query:work" {
		t.Fatalf("focus.changed from = %v, want query:work: %#v", from, result.Events[1].Payload)
	}
	if _, err = store.GetFrame(ctx, result.Frame.FrameID); err != nil {
		t.Fatal(err)
	}

	before, err := store.List(ctx, substrate.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	invalid := emission
	invalid.EmissionID = "emission-rollback"
	invalid.FrameID = result.Frame.FrameID
	invalid.Observation = []cognition.Observation{{Ref: "event:00000000-0000-4000-8000-999999999999", Interpretation: "missing"}}
	if _, err = step.Run(ctx, store, step.Input{Current: result.Frame, Emission: invalid, Definitions: map[string]affordance.Definition{def.ID: def}, NewID: uuidSequence(200), Now: func() time.Time { return now.Add(2 * time.Second) }}); err == nil {
		t.Fatal("step with missing reference succeeded")
	}
	after, listErr := store.List(ctx, substrate.EventFilter{})
	if listErr != nil || len(after) != len(before) {
		t.Fatalf("failed step was not rolled back: before=%d after=%d err=%v", len(before), len(after), listErr)
	}
}

// TestStepPersistsAttentionGuardDiagnostics checks the M16 wiring end to
// end: a no-op unpin must be rejected (and recorded) instead of silently
// applied, and dropping the last anchor must carry a collapse warning — both
// travel with the persisted frame.transitioned event, so replay and the
// debugger see what the runtime refused and why.
func TestStepPersistsAttentionGuardDiagnostics(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	current := testFrame(now)
	current.WorkingSet = []frame.Ref{{Type: frame.RefEvent, ID: "00000000-0000-4000-8000-000000000007"}}
	created := testEvent("00000000-0000-4000-8000-000000000006", "frame.created", now, current)
	created.Payload["frame_id"] = current.FrameID
	if err := store.CreateFrame(ctx, current, created); err != nil {
		t.Fatal(err)
	}
	seed := testEvent("00000000-0000-4000-8000-000000000007", "observation.recorded", now, current)
	if err := store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}
	emission := cognition.CognitiveEmission{
		EmissionID: "emission-guards", FrameID: current.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "available"}},
		FrameOps: []cognition.FrameOperation{
			{Op: "unpin", Ref: "event:" + seed.EventID},                          // applied: collapses the working set
			{Op: "unpin", Ref: "claim:00000000-0000-4000-8000-000000000099"}, // never pinned: must be rejected
		},
	}
	result, err := step.Run(ctx, store, step.Input{Current: current, Emission: emission, NewID: uuidSequence(300), Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Frame.WorkingSet) != 0 {
		t.Fatalf("applied unpin did not collapse the working set: %#v", result.Frame.WorkingSet)
	}
	var transition *protocol.Event
	for i := range result.Events {
		if result.Events[i].Type == step.EventFrameTransitioned {
			transition = &result.Events[i]
		}
	}
	if transition == nil {
		t.Fatalf("frame.transitioned missing: %#v", result.Events)
	}
	persisted, err := store.Get(ctx, transition.EventID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(persisted.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "unpin target is not in the working set") {
		t.Fatalf("rejection missing from persisted transition payload: %s", encoded)
	}
	if !strings.Contains(string(encoded), "collapsed to empty") {
		t.Fatalf("collapse warning missing from persisted transition payload: %s", encoded)
	}
}

// TestStepSurvivesHallucinatedFrameRefs pins the follow-up-loop contract: a
// model emission that pins or attends to a ref the substrate has never seen
// (a hallucinated UUID) must commit as a step with a visible rejection —
// not die at the store guard with 422 "reference not found", which used to
// kill interactive debugger continuations.
func TestStepSurvivesHallucinatedFrameRefs(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	current := testFrame(now)
	created := testEvent("00000000-0000-4000-8000-000000000006", "frame.created", now, current)
	created.Payload["frame_id"] = current.FrameID
	if err := store.CreateFrame(ctx, current, created); err != nil {
		t.Fatal(err)
	}
	seed := testEvent("00000000-0000-4000-8000-000000000007", "observation.recorded", now, current)
	if err := store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}
	ghost := "claim:8f2a7c31-0d84-4f6b-9c25-a1e7b4d90263"
	emission := cognition.CognitiveEmission{
		EmissionID: "emission-ghost", FrameID: current.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "available"}},
		Attention:   []cognition.AttentionOperation{{Op: "attend", Target: cognition.AttentionTarget{Type: frame.RefEntity, ID: "ghost-entity"}}},
		FrameOps:    []cognition.FrameOperation{{Op: "pin", Ref: ghost}, {Op: "pin", Ref: "event:" + seed.EventID}},
	}
	result, err := step.Run(ctx, store, step.Input{Current: current, Emission: emission, NewID: uuidSequence(400), Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatalf("step with hallucinated frame refs failed: %v", err)
	}
	// The real pin applied; the ghost must not leak into the working set, and
	// the focus must stay untouched (the attend targeted a ghost entity).
	if len(result.Frame.WorkingSet) != 1 || result.Frame.WorkingSet[0].ID != seed.EventID {
		t.Fatalf("working set must hold only the resolved pin: %#v", result.Frame.WorkingSet)
	}
	if result.Frame.Focus != current.Focus {
		t.Fatalf("ghost attend must not move focus: %#v", result.Frame.Focus)
	}
	var transition *protocol.Event
	for i := range result.Events {
		if result.Events[i].Type == step.EventFrameTransitioned {
			transition = &result.Events[i]
		}
	}
	if transition == nil {
		t.Fatalf("frame.transitioned missing: %#v", result.Events)
	}
	encoded, err := json.Marshal(transition.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), ghost) || !strings.Contains(string(encoded), "not found in substrate") {
		t.Fatalf("hallucination rejections missing from transition payload: %s", encoded)
	}
	if !strings.Contains(string(encoded), "attend target") {
		t.Fatalf("ghost attend rejection missing from transition payload: %s", encoded)
	}
}

// TestStepSurfacesMemoryTensionOnDuplicateClaims checks the M16 memory
// reliability wiring: a step that re-emits a proposition memory already
// holds must not merge or drop anything silently — the duplicate travels
// with the persisted frame.transitioned event, so replay and the debugger
// see what the emission did to memory.
func TestStepSurfacesMemoryTensionOnDuplicateClaims(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	current := testFrame(now)
	created := testEvent("00000000-0000-4000-8000-000000000006", "frame.created", now, current)
	created.Payload["frame_id"] = current.FrameID
	if err := store.CreateFrame(ctx, current, created); err != nil {
		t.Fatal(err)
	}
	seed := testEvent("00000000-0000-4000-8000-000000000007", "observation.recorded", now, current)
	if err := store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}
	base := cognition.CognitiveEmission{
		EmissionID: "emission-first", FrameID: current.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "available"}},
		Claims:      []cognition.EmittedClaim{{Proposition: "Repository contains a Go module", Confidence: 0.9}},
	}
	first, err := step.Run(ctx, store, step.Input{Current: current, Emission: base, NewID: uuidSequence(400), Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Claims) != 1 {
		t.Fatalf("first step must create one claim: %#v", first.Claims)
	}
	// The model re-emits the same proposition next step — the loop noise the
	// first-contact runs showed. The claim is still persisted (audit), but the
	// duplicate becomes visible instead of silently doubling the substrate.
	repeat := base
	repeat.EmissionID = "emission-repeat"
	repeat.FrameID = first.Frame.FrameID
	second, err := step.Run(ctx, store, step.Input{Current: first.Frame, Emission: repeat, NewID: uuidSequence(500), Now: func() time.Time { return now.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	var transition *protocol.Event
	for i := range second.Events {
		if second.Events[i].Type == step.EventFrameTransitioned {
			transition = &second.Events[i]
		}
	}
	if transition == nil {
		t.Fatalf("frame.transitioned missing: %#v", second.Events)
	}
	encoded, err := json.Marshal(transition.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "\"duplicate\"") || !strings.Contains(string(encoded), first.Claims[0].ClaimID) {
		t.Fatalf("memory tension missing from transition payload: %s", encoded)
	}
	// The first step had a clean memory to enter: no tension to report.
	firstEncoded, err := json.Marshal(first.Events[len(first.Events)-1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(firstEncoded), "memory_tensions") {
		t.Fatalf("first step unexpectedly reported tensions: %s", firstEncoded)
	}
}

// TestStepAppliesClaimReconciliation walks the M16 reconciliation loop end
// to end: the model refutes a wrong claim, supersedes an outdated one with a
// corrected claim emitted in the same step, and aims one refute at a claim
// that does not exist. The first two retire claims atomically with the step;
// the third becomes a visible rejection instead of an error.
func TestStepAppliesClaimReconciliation(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 16, 13, 0, 0, 0, time.UTC)
	current := testFrame(now)
	created := testEvent("00000000-0000-4000-8000-000000000006", "frame.created", now, current)
	created.Payload["frame_id"] = current.FrameID
	if err := store.CreateFrame(ctx, current, created); err != nil {
		t.Fatal(err)
	}
	seed := testEvent("00000000-0000-4000-8000-000000000007", "observation.recorded", now, current)
	if err := store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}
	first, err := step.Run(ctx, store, step.Input{Current: current, Emission: cognition.CognitiveEmission{
		EmissionID: "emission-seed", FrameID: current.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "available"}},
		Claims: []cognition.EmittedClaim{
			{Proposition: "Sum is implemented as a - b", Confidence: 0.9},
			{Proposition: "the test lives in main_test.go", Confidence: 0.8},
		},
	}, NewID: uuidSequence(600), Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Claims) != 2 {
		t.Fatalf("seed step must create two claims: %#v", first.Claims)
	}
	wrong, outdated := first.Claims[0], first.Claims[1]

	second, err := step.Run(ctx, store, step.Input{Current: first.Frame, Emission: cognition.CognitiveEmission{
		EmissionID: "emission-reconcile", FrameID: first.Frame.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "available"}},
		Claims: []cognition.EmittedClaim{
			{Proposition: "the test lives in cmd/app/main_test.go", Confidence: 0.95, Supersedes: "claim:" + outdated.ClaimID},
		},
		ClaimOps: []cognition.ClaimOperation{
			{Op: "refute", Claim: "claim:" + wrong.ClaimID},
			{Op: "refute", Claim: "claim:00000000-0000-4000-8000-000000000999"},
		},
	}, NewID: uuidSequence(700), Now: func() time.Time { return now.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}

	refuted, err := store.GetClaim(ctx, wrong.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	if refuted.Status != cognition.ClaimRefuted || refuted.ValidTo == nil {
		t.Fatalf("claim not refuted: %#v", refuted)
	}
	superseded, err := store.GetClaim(ctx, outdated.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	if superseded.Status != cognition.ClaimSuperseded || superseded.ValidTo == nil {
		t.Fatalf("claim not superseded: %#v", superseded)
	}
	var replacement cognition.Claim
	for _, claim := range second.Claims {
		if claim.Proposition == "the test lives in cmd/app/main_test.go" {
			replacement = claim
		}
	}
	if replacement.ClaimID == "" {
		t.Fatalf("replacement claim missing: %#v", second.Claims)
	}

	var transition *protocol.Event
	for i := range second.Events {
		if second.Events[i].Type == step.EventFrameTransitioned {
			transition = &second.Events[i]
		}
	}
	if transition == nil {
		t.Fatalf("frame.transitioned missing: %#v", second.Events)
	}
	encoded, err := json.Marshal(transition.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "claim_ops[1]") || !strings.Contains(string(encoded), "claim not found") {
		t.Fatalf("missing-op rejection missing from payload: %s", encoded)
	}
	kinds := map[string]int{}
	for _, event := range second.Events {
		kinds[event.Type]++
	}
	if kinds["claim.refuted"] != 1 || kinds["claim.superseded"] != 1 {
		t.Fatalf("reconciliation events missing: %v", kinds)
	}

	// Refuting a retired claim is well-formed but impossible — a rejection,
	// not an error, exactly like the working-set guards.
	third, err := step.Run(ctx, store, step.Input{Current: second.Frame, Emission: cognition.CognitiveEmission{
		EmissionID: "emission-retry", FrameID: second.Frame.FrameID,
		ClaimOps: []cognition.ClaimOperation{{Op: "refute", Claim: "claim:" + wrong.ClaimID}},
	}, NewID: uuidSequence(800), Now: func() time.Time { return now.Add(3 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	var retryTransition *protocol.Event
	for i := range third.Events {
		if third.Events[i].Type == step.EventFrameTransitioned {
			retryTransition = &third.Events[i]
		}
	}
	retryEncoded, err := json.Marshal(retryTransition.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(retryEncoded), "is not active") {
		t.Fatalf("inactive-claim rejection missing: %s", retryEncoded)
	}
}

// TestStepAppliesClaimLifecycle walks the pivot knowledge lifecycle end to
// end: a fact extracted from a cited observation is born supported with
// persisted evidence, a hypothesis stays candidate until a later step
// confirms it after verification, confirming an already-supported claim is a
// visible rejection, and citing an event that never existed fails the step
// atomically.
func TestStepAppliesClaimLifecycle(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	current := testFrame(now)
	created := testEvent("00000000-0000-4000-8000-000000000006", "frame.created", now, current)
	created.Payload["frame_id"] = current.FrameID
	if err := store.CreateFrame(ctx, current, created); err != nil {
		t.Fatal(err)
	}
	seed := testEvent("00000000-0000-4000-8000-000000000007", "world.observation", now, current)
	if err := store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}

	first, err := step.Run(ctx, store, step.Input{Current: current, Emission: cognition.CognitiveEmission{
		EmissionID: "emission-lifecycle", FrameID: current.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "file content observed"}},
		Claims: []cognition.EmittedClaim{
			{Proposition: "main.go implements Sum as a - b", Confidence: 0.95, Status: cognition.ClaimSupported, Evidence: []string{"event:" + seed.EventID}},
			{Proposition: "the Sum bug breaks TestSum", Confidence: 0.7, Status: cognition.ClaimCandidate, Evidence: []string{"event:" + seed.EventID}},
		},
	}, NewID: uuidSequence(900), Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Claims) != 2 {
		t.Fatalf("lifecycle step must create two claims: %#v", first.Claims)
	}
	fact, hypothesis := first.Claims[0], first.Claims[1]
	if fact.Status != cognition.ClaimSupported || hypothesis.Status != cognition.ClaimCandidate {
		t.Fatalf("birth statuses wrong: %q / %q", fact.Status, hypothesis.Status)
	}
	if fact.ValidTo != nil {
		t.Fatalf("born-supported claim must stay open: %#v", fact)
	}
	for _, claim := range first.Claims {
		evidence, listErr := store.ListClaimEvidence(ctx, claim.ClaimID)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(evidence) != 1 || evidence[0] != seed.EventID {
			t.Fatalf("claim %s evidence = %v, want [%s]", claim.ClaimID, evidence, seed.EventID)
		}
	}
	kinds := map[string]int{}
	for _, event := range first.Events {
		kinds[event.Type]++
	}
	if kinds["claim.supported"] != 1 || kinds[step.EventClaimCandidate] != 1 {
		t.Fatalf("lifecycle creation events wrong: %v", kinds)
	}

	// Verification happened (the seed event is the stand-in for an execution
	// result); the next step confirms the hypothesis.
	second, err := step.Run(ctx, store, step.Input{Current: first.Frame, Emission: cognition.CognitiveEmission{
		EmissionID: "emission-confirm", FrameID: first.Frame.FrameID,
		ClaimOps: []cognition.ClaimOperation{
			{Op: "confirm", Claim: "claim:" + hypothesis.ClaimID},
			{Op: "confirm", Claim: "claim:" + fact.ClaimID}, // already supported: rejection, not error
		},
	}, NewID: uuidSequence(1000), Now: func() time.Time { return now.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := store.GetClaim(ctx, hypothesis.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != cognition.ClaimSupported || confirmed.ValidTo != nil {
		t.Fatalf("hypothesis not confirmed openly: %#v", confirmed)
	}
	var transition *protocol.Event
	for i := range second.Events {
		if second.Events[i].Type == step.EventFrameTransitioned {
			transition = &second.Events[i]
		}
	}
	if transition == nil {
		t.Fatalf("frame.transitioned missing: %#v", second.Events)
	}
	encoded, err := json.Marshal(transition.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "already supported") || !strings.Contains(string(encoded), "claim_ops[1]") {
		t.Fatalf("already-supported rejection missing: %s", encoded)
	}

	// Evidence citing an event that never existed fails the whole step — a
	// claim cannot be backed by an unobserved fact, and nothing leaks.
	before, err := store.List(ctx, substrate.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = step.Run(ctx, store, step.Input{Current: second.Frame, Emission: cognition.CognitiveEmission{
		EmissionID: "emission-hallucinated", FrameID: second.Frame.FrameID,
		Claims: []cognition.EmittedClaim{{Proposition: "backed by nothing", Confidence: 0.9, Status: cognition.ClaimSupported, Evidence: []string{"event:00000000-0000-4000-8000-000000000999"}}},
	}, NewID: uuidSequence(1100), Now: func() time.Time { return now.Add(3 * time.Second) }})
	if err == nil {
		t.Fatal("step with hallucinated evidence succeeded")
	}
	after, listErr := store.List(ctx, substrate.EventFilter{})
	if listErr != nil || len(after) != len(before) {
		t.Fatalf("failed step was not rolled back: before=%d after=%d err=%v", len(before), len(after), listErr)
	}
}

// TestStepRecordsFocusChangeWithTrigger pins the pivot investigation-history
// contract: attending a new focus after refuting a hypothesis logs
// focus.changed with trigger hypothesis_retired and the claim/event refs that
// justify the move; re-attending the same focus logs nothing.
func TestStepRecordsFocusChangeWithTrigger(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 18, 13, 0, 0, 0, time.UTC)
	current := testFrame(now)
	created := testEvent("00000000-0000-4000-8000-000000000006", "frame.created", now, current)
	created.Payload["frame_id"] = current.FrameID
	if err := store.CreateFrame(ctx, current, created); err != nil {
		t.Fatal(err)
	}
	seed := testEvent("00000000-0000-4000-8000-000000000007", "execution.completed", now, current)
	if err := store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}
	first, err := step.Run(ctx, store, step.Input{Current: current, Emission: cognition.CognitiveEmission{
		EmissionID: "emission-focus-seed", FrameID: current.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "test run failed"}},
		Claims:      []cognition.EmittedClaim{{Proposition: "bug lives in internal/reports", Confidence: 0.8}},
	}, NewID: uuidSequence(1200), Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}

	second, err := step.Run(ctx, store, step.Input{Current: first.Frame, Emission: cognition.CognitiveEmission{
		EmissionID: "emission-focus-move", FrameID: first.Frame.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "failure points elsewhere"}},
		ClaimOps:    []cognition.ClaimOperation{{Op: "refute", Claim: "claim:" + first.Claims[0].ClaimID}},
		Attention:   []cognition.AttentionOperation{{Op: "attend", Target: cognition.AttentionTarget{Type: frame.RefQuery, Text: "internal/util/format.go"}}},
	}, NewID: uuidSequence(1300), Now: func() time.Time { return now.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	var focusEvent *protocol.Event
	for i := range second.Events {
		if second.Events[i].Type == step.EventFocusChanged {
			focusEvent = &second.Events[i]
		}
	}
	if focusEvent == nil {
		t.Fatalf("focus.changed missing: %#v", second.Events)
	}
	payload := focusEvent.Payload
	if payload["from"] != "query:work" || payload["to"] != "query:internal/util/format.go" || payload["trigger"] != "hypothesis_retired" {
		t.Fatalf("focus.changed wrong: %#v", payload)
	}
	if payload["frame_before"] != first.Frame.FrameID || payload["frame_after"] != second.Frame.FrameID {
		t.Fatalf("focus.changed frames wrong: %#v", payload)
	}
	encoded, err := json.Marshal(payload["evidence"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "claim:"+first.Claims[0].ClaimID) || !strings.Contains(string(encoded), "event:"+seed.EventID) {
		t.Fatalf("focus.changed evidence missing refs: %s", encoded)
	}
	// Persisted in the log, not just the result: the debugger reads events.
	persisted, err := store.Get(ctx, focusEvent.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Type != step.EventFocusChanged {
		t.Fatalf("focus.changed not persisted: %#v", persisted)
	}

	// Attending the focus the frame already holds is a no-op — no noise event.
	third, err := step.Run(ctx, store, step.Input{Current: second.Frame, Emission: cognition.CognitiveEmission{
		EmissionID: "emission-focus-same", FrameID: second.Frame.FrameID,
		Attention: []cognition.AttentionOperation{{Op: "attend", Target: cognition.AttentionTarget{Type: frame.RefQuery, Text: "internal/util/format.go"}}},
	}, NewID: uuidSequence(1400), Now: func() time.Time { return now.Add(3 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range third.Events {
		if event.Type == step.EventFocusChanged {
			t.Fatalf("no-op attend must not log focus.changed: %#v", event)
		}
	}
}

func testFrame(now time.Time) frame.Frame {
	value := frame.Frame{FrameID: "00000000-0000-4000-8000-000000000001", AgentID: "00000000-0000-4000-8000-000000000002", EpisodeID: "00000000-0000-4000-8000-000000000003", BranchID: "00000000-0000-4000-8000-000000000004", ObjectiveID: "00000000-0000-4000-8000-000000000005", AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "work"}, Attention: frame.Attention{Deliberate: true}}
	value.ApplyDefaults()
	return value
}

func testEvent(id, kind string, now time.Time, value frame.Frame) protocol.Event {
	event := protocol.Event{EventID: id, TransactionTime: now, ValidTime: now, AgentID: value.AgentID, EpisodeID: value.EpisodeID, BranchID: value.BranchID, Type: kind, Payload: map[string]any{}, Provenance: map[string]any{"source": "test"}}
	event.ApplyDefaults(now)
	return event
}

func testDefinition() affordance.Definition {
	value := affordance.Definition{ID: "work.execute", ExecutionMode: affordance.ModeDeterministic, InputSchema: map[string]any{"type": "object"}, Capabilities: []string{"work.execute"}, Limits: affordance.Limits{TimeoutSec: 10, CPU: 1, MemoryMB: 64, DiskMB: 64}}
	value.ApplyDefaults()
	return value
}

func uuidSequence(start int) func() string {
	n := start
	return func() string {
		n++
		return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	}
}
