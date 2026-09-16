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
	if len(result.Claims) != 1 || len(result.Executions) != 1 || len(result.Events) != 5 || result.Events[1].Type != step.EventAttentionSelected || result.Events[4].Type != step.EventFrameTransitioned {
		t.Fatalf("unexpected step result: %#v", result)
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
