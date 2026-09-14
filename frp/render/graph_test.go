package render_test

import (
	"context"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/attention"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/projection"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func TestGraphProximityChangesAmbientRegionRanking(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "unrelated objective", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "unrelated focus"}, WorkingSet: []frame.Ref{{Type: frame.RefRegion, ID: "anchor"}}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Ambient: true, Deliberate: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateObjective(ctx, goal, renderEvent("objective-event", "episode.started", now, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, renderEvent("frame-event", "frame.created", now, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}
	regions := []projection.Region{
		{RegionID: "anchor", EpisodeID: "episode", BranchID: "branch", Label: "anchor", Activation: .5},
		{RegionID: "a-far", EpisodeID: "episode", BranchID: "branch", Label: "distant", Activation: .5},
		{RegionID: "z-near", EpisodeID: "episode", BranchID: "branch", Label: "nearby", Activation: .5},
	}
	if err := store.ReplaceRegions(ctx, "episode", "branch", regions); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceEdges(ctx, "episode", "branch", []projection.Edge{{EdgeID: "edge", EpisodeID: "episode", BranchID: "branch", SourceRegionID: "anchor", TargetRegionID: "z-near", Type: projection.EdgeSequenceAdjacent, Weight: 1, EvidenceCount: 1, ProjectionVersion: projection.EdgeProjectorVersion}}); err != nil {
		t.Fatal(err)
	}
	packet, err := render.New(store).Render(ctx, render.Request{FrameID: "frame", ObjectiveID: "objective", BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	var near attention.ScoredCandidate
	for i, item := range packet.Sections[2].Items {
		scored := item.(attention.ScoredCandidate)
		if scored.Candidate.Ref.Type == frame.RefRegion {
			positions[scored.Candidate.Ref.ID] = i
			if scored.Candidate.Ref.ID == "z-near" {
				near = scored
			}
		}
	}
	if positions["z-near"] >= positions["a-far"] {
		t.Fatalf("graph-near region did not outrank deterministic ID tie-break: %#v", positions)
	}
	if near.Candidate.Features.GraphProximity != 1 {
		t.Fatalf("unexpected graph proximity: %#v", near.Candidate.Features)
	}
}
