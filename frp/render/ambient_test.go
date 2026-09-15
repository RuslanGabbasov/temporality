package render

import (
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/projection"
	"github.com/temporality-project/temporality/frp/protocol"
)

// TestSelectAmbientGraphProximityFeature is the white-box companion of
// TestGraphProximityChangesAmbientRegionRanking: it asserts the raw
// graph-proximity feature that drives the ranking, which the packet no longer
// carries (features were dropped from render output for token efficiency).
func TestSelectAmbientGraphProximityFeature(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "unrelated objective"}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "unrelated focus"}, WorkingSet: []frame.Ref{{Type: frame.RefRegion, ID: "anchor"}}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	regions := []projection.Region{
		{RegionID: "anchor", EpisodeID: "episode", BranchID: "branch", Label: "anchor", Activation: .5},
		{RegionID: "a-far", EpisodeID: "episode", BranchID: "branch", Label: "distant", Activation: .5},
		{RegionID: "z-near", EpisodeID: "episode", BranchID: "branch", Label: "nearby", Activation: .5},
		// Bookkeeping aggregate: an event_type region over attention's own
		// exhaust must not compete in the ambient pool.
		{RegionID: "noise", EpisodeID: "episode", BranchID: "branch", Kind: "event_type", Label: "attention.suggested", Activation: 1},
	}
	edges := []projection.Edge{{EdgeID: "edge", EpisodeID: "episode", BranchID: "branch", SourceRegionID: "anchor", TargetRegionID: "z-near", Type: projection.EdgeSequenceAdjacent, Weight: 1, EvidenceCount: 1, ProjectionVersion: projection.EdgeProjectorVersion}}
	result, err := selectAmbient(current, goal, nil, regions, edges, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	proximity := 0.0
	present := map[string]bool{}
	for _, candidate := range result.Selected {
		present[candidate.Candidate.Ref.ID] = true
		if candidate.Candidate.Ref.ID == "z-near" {
			proximity = candidate.Candidate.Features.GraphProximity
		}
	}
	if proximity != 1 {
		t.Fatalf("unexpected graph proximity: %v", proximity)
	}
	if present["noise"] {
		t.Fatal("bookkeeping event_type region leaked into ambient selection")
	}
}
