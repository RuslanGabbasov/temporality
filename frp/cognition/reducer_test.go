package cognition_test

import (
	"reflect"
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
