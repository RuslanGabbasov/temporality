package branch

import (
	"reflect"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/frame"
)

func sourceFrame() frame.Frame {
	return frame.Frame{
		Protocol: "frp", Version: "0.3", FrameID: "source-frame", ParentFrameID: "parent-frame", AgentID: "agent-1",
		EpisodeID: "episode-1", BranchID: "main", ObjectiveID: "objective-1",
		AsOf:       time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC),
		Focus:      frame.Focus{Type: frame.RefQuery, Query: "compare approaches"},
		WorkingSet: []frame.Ref{{Type: frame.RefClaim, ID: "claim-1"}}, Mode: frame.ModeExplore,
		Attention: frame.Attention{Policy: "balanced", MaxCandidates: 32},
		Filters:   frame.Filters{AgentIDs: []string{"agent-1"}, RegionKinds: []string{"topic"}},
		Budget:    frame.Budget{Tokens: 8000}, Revision: 4,
	}
}

func TestForkIsDeterministicAndDoesNotMutateSource(t *testing.T) {
	source := sourceFrame()
	before := cloneFrame(source)
	request := ForkRequest{ForkGroupID: "fork-1", BranchIDs: []string{"branch-a", "branch-b"}}

	group, roots, err := Fork(source, request)
	if err != nil {
		t.Fatal(err)
	}
	secondGroup, secondRoots, err := Fork(source, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(group, secondGroup) || !reflect.DeepEqual(roots, secondRoots) {
		t.Fatal("same fork input produced different output")
	}
	if !reflect.DeepEqual(source, before) {
		t.Fatal("Fork mutated source frame")
	}
	if len(roots) != 2 || roots[0].BranchID != "branch-a" || roots[1].BranchID != "branch-b" {
		t.Fatalf("unexpected roots: %#v", roots)
	}
	for i, root := range roots {
		if root.ParentFrameID != source.FrameID || root.Revision != source.Revision+1 {
			t.Fatalf("root %d has invalid lineage", i)
		}
		if err := ValidateFrameIsolation(group.Branches[i], root); err != nil {
			t.Fatalf("root %d isolation: %v", i, err)
		}
	}

	roots[0].WorkingSet[0].ID = "mutated"
	roots[0].Filters.AgentIDs[0] = "mutated"
	if source.WorkingSet[0].ID != "claim-1" || roots[1].WorkingSet[0].ID != "claim-1" {
		t.Fatal("working set aliases source or sibling branch")
	}
	if source.Filters.AgentIDs[0] != "agent-1" || roots[1].Filters.AgentIDs[0] != "agent-1" {
		t.Fatal("filters alias source or sibling branch")
	}
}

func TestForkRejectsNonIsolatedBranchIDs(t *testing.T) {
	for _, request := range []ForkRequest{
		{ForkGroupID: "fork-1", BranchIDs: []string{"same", "same"}},
		{ForkGroupID: "fork-1", BranchIDs: []string{"main", "other"}},
	} {
		if _, _, err := Fork(sourceFrame(), request); err == nil {
			t.Fatalf("Fork(%#v) succeeded", request)
		}
	}
}

func TestValidateFrameIsolationRejectsForeignFrame(t *testing.T) {
	group, roots, err := Fork(sourceFrame(), ForkRequest{ForkGroupID: "fork-1", BranchIDs: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateFrameIsolation(group.Branches[0], roots[1]); err == nil {
		t.Fatal("foreign branch frame was accepted")
	}
}
