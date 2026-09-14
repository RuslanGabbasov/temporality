package frame_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/frame"
)

func TestReduceIsDeterministicAndImmutable(t *testing.T) {
	original := testFrame()
	before := original
	before.WorkingSet = append([]frame.Ref(nil), original.WorkingSet...)
	zoom := 3
	transition := frame.Transition{AsOf: "2026-09-14T11:00:00Z", Operations: []frame.Operation{{Kind: frame.OpAttend, Focus: &frame.Focus{Type: frame.RefQuery, Query: "contradicting evidence"}}, {Kind: frame.OpPin, Ref: &frame.Ref{Type: frame.RefClaim, ID: "claim-2"}}, {Kind: frame.OpSetMode, Mode: frame.ModeVerify}, {Kind: frame.OpSetZoom, Zoom: &zoom}}}
	first, err := frame.Reduce(original, transition)
	if err != nil {
		t.Fatal(err)
	}
	second, err := frame.Reduce(original, transition)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same transition produced different frames")
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("reducer mutated source frame")
	}
	if first.ParentFrameID != original.FrameID || first.Revision != original.Revision+1 || first.Mode != frame.ModeVerify || len(first.WorkingSet) != 2 {
		t.Fatalf("unexpected frame: %#v", first)
	}
}

func TestReduceRejectsInvalidOperationWithoutMutation(t *testing.T) {
	original := testFrame()
	_, err := frame.Reduce(original, frame.Transition{Operations: []frame.Operation{{Kind: frame.OpAttend}}})
	if err == nil {
		t.Fatal("invalid operation accepted")
	}
	if original.Revision != 0 || original.ParentFrameID != "" {
		t.Fatal("source mutated on rejection")
	}
}

func testFrame() frame.Frame {
	value := frame.Frame{Protocol: "frp", Version: "0.3", FrameID: "frame-1", AgentID: "agent-1", EpisodeID: "episode-1", BranchID: "branch-1", ObjectiveID: "objective-1", AsOf: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC), Focus: frame.Focus{Type: frame.RefClaim, ID: "claim-1"}, WorkingSet: []frame.Ref{{Type: frame.RefClaim, ID: "claim-1"}}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Zoom: 2, Filters: frame.Filters{TrustMin: 0.5, AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	return value
}
