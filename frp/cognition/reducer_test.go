package cognition_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
)

func TestReduceEmissionCompilesIntentDeterministically(t *testing.T) {
	current := emissionFrame()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	emission := cognition.CognitiveEmission{Schema: cognition.EmissionSchema, EmissionID: "emission-1", FrameID: current.FrameID, Observation: []cognition.Observation{{Ref: "event:event-1", Interpretation: "test failed"}}, Reasoning: []cognition.Reasoning{{Kind: "hypothesis", Text: "failure is deterministic"}}, Claims: []cognition.EmittedClaim{{Proposition: "build fails", Confidence: 0.8}}, Attention: []cognition.AttentionOperation{{Op: "attend", Target: cognition.AttentionTarget{Type: frame.RefQuery, Text: "contradicting evidence"}}}, Actions: []cognition.ActionRequest{{Affordance: "run_test", Args: map[string]any{"suite": "unit"}}}, FrameOps: []cognition.FrameOperation{{Op: "pin", Ref: "event:event-1"}}}
	first, err := cognition.ReduceEmission(current, emission, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cognition.ReduceEmission(current, emission, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same emission produced different decision")
	}
	if first.Frame.Focus.Query != "contradicting evidence" || len(first.Frame.WorkingSet) != 1 {
		t.Fatalf("frame intents not applied: %#v", first.Frame)
	}
	// The child frame advances to the commit time so later observations stay
	// visible to time-travel renders of it.
	if !first.Frame.AsOf.Equal(now) {
		t.Fatalf("child frame as_of %s want commit time %s", first.Frame.AsOf, now)
	}
	if len(first.Claims) != 1 || first.Claims[0].Status != cognition.ClaimCandidate || len(first.Actions) != 1 {
		t.Fatalf("durable intents lost: %#v", first)
	}
	if len(first.Rejections) != 0 || len(first.Warnings) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", first)
	}
}

func TestReduceEmissionRejectsWrongFrameAndMalformedRef(t *testing.T) {
	current := emissionFrame()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	wrong := cognition.CognitiveEmission{EmissionID: "e", FrameID: "other", FrameOps: []cognition.FrameOperation{}}
	if _, err := cognition.ReduceEmission(current, wrong, now); err == nil {
		t.Fatal("wrong frame accepted")
	}
	malformed := cognition.CognitiveEmission{EmissionID: "e", FrameID: current.FrameID, FrameOps: []cognition.FrameOperation{{Op: "pin", Ref: "secret:value"}}}
	if _, err := cognition.ReduceEmission(current, malformed, now); err == nil {
		t.Fatal("malformed ref accepted")
	}
	if _, err := cognition.ReduceEmission(current, cognition.CognitiveEmission{EmissionID: "e", FrameID: current.FrameID}, time.Time{}); err == nil {
		t.Fatal("zero commit time accepted")
	}
}

func emissionFrame() frame.Frame {
	return frame.Frame{Protocol: "frp", Version: "0.3", FrameID: "frame-1", AgentID: "agent-1", EpisodeID: "episode-1", BranchID: "branch-1", ObjectiveID: "objective-1", AsOf: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC), Focus: frame.Focus{Type: frame.RefQuery, Query: "initial"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Zoom: 2, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
}

func TestReduceEmissionGuardsWorkingSetCap(t *testing.T) {
	current := emissionFrame()
	current.WorkingSet = make([]frame.Ref, 0, frame.MaxWorkingSet)
	for i := 0; i < frame.MaxWorkingSet; i++ {
		current.WorkingSet = append(current.WorkingSet, frame.Ref{Type: frame.RefEvent, ID: fmt.Sprintf("existing-%d", i)})
	}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	extra := cognition.FrameOperation{Op: "pin", Ref: "event:extra"}
	// A pin past the cap is rejected, not applied — but re-pinning a ref that
	// is already anchored stays legal (idempotent, grows nothing).
	idempotent := cognition.FrameOperation{Op: "pin", Ref: "event:existing-0"}
	decision, err := cognition.ReduceEmission(current, cognition.CognitiveEmission{EmissionID: "e-cap", FrameID: current.FrameID, FrameOps: []cognition.FrameOperation{extra, idempotent}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Frame.WorkingSet) != frame.MaxWorkingSet {
		t.Fatalf("cap did not hold: %d", len(decision.Frame.WorkingSet))
	}
	if len(decision.Rejections) != 1 || decision.Rejections[0].Path != "frame_ops[0]" || !strings.Contains(decision.Rejections[0].Reason, "working set cap") {
		t.Fatalf("missing cap rejection: %#v", decision.Rejections)
	}
	if len(decision.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", decision.Warnings)
	}
}

func TestReduceEmissionRejectsNoOpUnpinAndWarnsOnCollapse(t *testing.T) {
	current := emissionFrame()
	current.WorkingSet = []frame.Ref{{Type: frame.RefEvent, ID: "anchor"}}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	// Unpinning something never pinned is a silent no-op in the frame reducer;
	// the cognitive gate records it so the audit trail shows the model tried.
	decision, err := cognition.ReduceEmission(current, cognition.CognitiveEmission{EmissionID: "e-unpin", FrameID: current.FrameID, FrameOps: []cognition.FrameOperation{{Op: "unpin", Ref: "claim:missing"}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Rejections) != 1 || decision.Rejections[0].Path != "frame_ops[0]" || !strings.Contains(decision.Rejections[0].Reason, "not in the working set") {
		t.Fatalf("missing unpin rejection: %#v", decision.Rejections)
	}
	if len(decision.Frame.WorkingSet) != 1 {
		t.Fatalf("no-op unpin changed the working set: %#v", decision.Frame.WorkingSet)
	}
	// Dropping the last anchor is allowed — the model may re-anchor — but it
	// never happens silently: a collapse warning travels with the decision.
	decision, err = cognition.ReduceEmission(current, cognition.CognitiveEmission{EmissionID: "e-collapse", FrameID: current.FrameID, FrameOps: []cognition.FrameOperation{{Op: "unpin", Ref: "event:anchor"}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Frame.WorkingSet) != 0 {
		t.Fatalf("unpin did not apply: %#v", decision.Frame.WorkingSet)
	}
	if len(decision.Warnings) != 1 || !strings.Contains(decision.Warnings[0], "collapsed") {
		t.Fatalf("missing collapse warning: %#v", decision.Warnings)
	}
	// A frame that was already empty stays empty without a warning: collapse
	// is a transition, not a state.
	empty := emissionFrame()
	decision, err = cognition.ReduceEmission(empty, cognition.CognitiveEmission{EmissionID: "e-stay-empty", FrameID: empty.FrameID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Warnings) != 0 {
		t.Fatalf("stay-empty produced a warning: %#v", decision.Warnings)
	}
}

// TestReduceEmissionRejectsHallucinatedRefs pins the debugger-facing
// contract: a pin or attend naming a ref the substrate has never seen is a
// VISIBLE REJECTION — the step must survive instead of dying at the store
// guard with 422 "reference not found" (a hallucinated UUID used to kill the
// whole follow-up loop).
func TestReduceEmissionRejectsHallucinatedRefs(t *testing.T) {
	current := emissionFrame()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	resolve := func(ref frame.Ref) (bool, error) {
		return ref.ID == "real", nil
	}
	emission := cognition.CognitiveEmission{
		EmissionID: "e-hallucination", FrameID: current.FrameID,
		Attention: []cognition.AttentionOperation{
			{Op: "attend", Target: cognition.AttentionTarget{Type: frame.RefClaim, ID: "real"}},
			{Op: "attend", Target: cognition.AttentionTarget{Type: frame.RefEntity, ID: "ghost"}},
		},
		FrameOps: []cognition.FrameOperation{
			{Op: "pin", Ref: "claim:real"},
			{Op: "pin", Ref: "claim:8f2a7c31-0d84-4f6b-9c25-a1e7b4d90263"},
		},
	}
	decision, err := cognition.ReduceEmission(current, emission, now, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Rejections) != 2 {
		t.Fatalf("want rejections for the hallucinated attend and pin: %#v", decision.Rejections)
	}
	if decision.Rejections[0].Path != "attention[1]" || !strings.Contains(decision.Rejections[0].Reason, "not found in substrate") {
		t.Fatalf("attend rejection wrong: %#v", decision.Rejections[0])
	}
	if decision.Rejections[1].Path != "frame_ops[1]" || !strings.Contains(decision.Rejections[1].Reason, "pin ignored") {
		t.Fatalf("pin rejection wrong: %#v", decision.Rejections[1])
	}
	// The surviving ops applied: focus moved to the real claim, the real pin
	// anchored; the ghost ref must not leak into the working set.
	if decision.Frame.Focus.Type != frame.RefClaim || decision.Frame.Focus.ID != "real" {
		t.Fatalf("attend to the real claim did not apply: %#v", decision.Frame.Focus)
	}
	if len(decision.Frame.WorkingSet) != 1 || decision.Frame.WorkingSet[0].ID != "real" {
		t.Fatalf("working set must hold only the resolved pin: %#v", decision.Frame.WorkingSet)
	}
	// A resolver failure is a store error, not a rejection.
	failing := func(frame.Ref) (bool, error) { return false, errors.New("store down") }
	if _, err = cognition.ReduceEmission(current, emission, now, failing); err == nil || !strings.Contains(err.Error(), "store down") {
		t.Fatalf("resolver error must fail the reduction: %v", err)
	}
}
