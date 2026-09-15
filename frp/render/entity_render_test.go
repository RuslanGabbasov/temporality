package render_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/entity"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

// TestRenderAttendsOverEntities verifies M14.3: relevant global entities enter
// the ambient map section, while entities with no relation to the frame's
// focus, objective, or pinned entities stay out of the packet.
func TestRenderAttendsOverEntities(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "understand the billing module dependencies", SuccessConditions: []string{}}
	module := entity.Ref{Type: entity.TypeModule, Name: "example.com/billing"}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "billing module dependencies"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateObjective(ctx, goal, renderEvent("objective-event", "episode.started", now, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, renderEvent("frame-event", "frame.created", now, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}
	relevant := entity.Entity{EntityID: module.ID(), Type: module.Type, Name: module.Name, MentionCount: 4, Confidence: 0.9, ProjectionVersion: entity.ProjectorVersion}
	unrelated := entity.Entity{EntityID: entity.Ref{Type: entity.TypeBranch, Name: "feature-999"}.ID(), Type: entity.TypeBranch, Name: "feature-999", MentionCount: 1, Confidence: 0.9, ProjectionVersion: entity.ProjectorVersion}
	if err := store.ReplaceAllEntities(ctx, []entity.Entity{relevant, unrelated}, nil); err != nil {
		t.Fatal(err)
	}
	packet, err := render.New(store).Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	mapSection := packetSection(packet, "map")
	if mapSection == nil {
		t.Fatal("map section missing")
	}
	found, unexpected := false, false
	for _, item := range mapSection.Items {
		candidate, ok := item.(render.MapItem)
		if !ok {
			t.Fatalf("unexpected map item type %T", item)
		}
		if !strings.HasPrefix(candidate.Ref, "entity:") {
			continue
		}
		if candidate.Ref == "entity:"+relevant.EntityID {
			found = true
		}
		if candidate.Ref == "entity:"+unrelated.EntityID {
			unexpected = true
		}
	}
	if !found {
		t.Fatal("relevant module entity missing from render map")
	}
	if unexpected {
		t.Fatal("unrelated entity leaked into render map")
	}
}

func packetSection(packet render.Packet, kind string) *render.Section {
	for i := range packet.Sections {
		if packet.Sections[i].Kind == kind {
			return &packet.Sections[i]
		}
	}
	return nil
}
