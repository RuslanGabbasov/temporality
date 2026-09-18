package render_test

import (
	"context"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/substrate/memory"
	"github.com/temporality-project/temporality/frp/world"
)

// TestRenderWorldMemoryMarksStaleAcrossWorldVersions locks pivot §21: durable
// world claims carry the world state version they were earned against, and a
// render for a newer world state must deliver them as stale history, not as
// current facts. The warm-facts benchmark was poisoned exactly by a confirmed
// "tests are green" from v1 being read as a fact about v2.
func TestRenderWorldMemoryMarksStaleAcrossWorldVersions(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

	worldID := "bench-world"
	register := func(version int, at time.Time) {
		value := world.World{WorldID: worldID, StateVersion: version, Resources: []world.Resource{{ID: "repo", Type: "git_repository", Path: "/tmp/repo"}}, Capabilities: []string{"filesystem.read"}}
		value.ApplyDefaults()
		event, err := world.StateEvent(value, "world-state-"+string(rune('a'+version)), at)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.SaveWorld(ctx, value, event); err != nil {
			t.Fatal(err)
		}
	}
	register(1, base)
	register(2, base.Add(time.Hour)) // the world changed: fixes applied + new bugs

	// Episode 1 acted in the world (an observation ties the episode to it).
	if err := store.Append(ctx, protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: "obs-1", TransactionTime: base.Add(30 * time.Second), ValidTime: base.Add(30 * time.Second), EpisodeID: "episode-1", BranchID: "branch-1", Type: "world.observation", Payload: map[string]any{"world_id": worldID, "world_version": 1, "observation_type": "directory_listing"}, Provenance: map[string]any{"source": "test"}}); err != nil {
		t.Fatal(err)
	}

	claimEvent := func(id, eventType, claimID string, at time.Time, payload map[string]any) protocol.Event {
		payload["claim_id"] = claimID
		event := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: "episode-1", BranchID: "branch-1", Type: eventType, Payload: payload, Provenance: map[string]any{"source": "test"}}
		event.ApplyDefaults(at)
		return event
	}

	// Episode 1 earned two claims against world v1: a confirmed outcome claim
	// (the benchmark poison) and a structural fact.
	confirmedClaim := cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: "claim-green", Proposition: "final go test run exited 0", Confidence: 1, Status: cognition.ClaimSupported, CreatedEvent: "event-green", ValidFrom: base.Add(time.Minute)}
	if err := store.CommitClaim(ctx, cognition.Commit{Event: claimEvent("event-green", "claim.supported", "claim-green", base.Add(time.Minute), map[string]any{"world_version": 1}), Claim: confirmedClaim}); err != nil {
		t.Fatal(err)
	}
	structuralClaim := cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: "claim-structure", Proposition: "repository declares Go module example.com/ledger", Confidence: 0.9, Status: cognition.ClaimCandidate, CreatedEvent: "event-structure", ValidFrom: base.Add(time.Minute)}
	if err := store.CommitClaim(ctx, cognition.Commit{Event: claimEvent("event-structure", "claim.candidate", "claim-structure", base.Add(time.Minute), map[string]any{"world_version": 1}), Claim: structuralClaim}); err != nil {
		t.Fatal(err)
	}
	// Episode 2 (world already at v2) re-verifies the structural claim: the
	// confirm transition carries world_version 2, refreshing it to current.
	confirmedAt := base.Add(2 * time.Hour)
	if _, err := store.TransitionClaim(ctx, cognition.Transition{Event: claimEvent("event-reconfirm", "claim.supported", "claim-structure", confirmedAt, map[string]any{"world_version": 2}), ClaimID: "claim-structure", ToStatus: cognition.ClaimSupported, ValidAt: confirmedAt}); err != nil {
		t.Fatal(err)
	}

	// Episode 2 frame in the same world, rendered after the world moved to v2.
	startedAt := base.Add(3 * time.Hour)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective-2", EpisodeID: "episode-2", Text: "fix the failing tests", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-2", AgentID: "agent-2", EpisodeID: "episode-2", BranchID: "branch-2", ObjectiveID: goal.ObjectiveID, AsOf: startedAt, Focus: frame.Focus{Type: frame.RefQuery, Query: "failing tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 16000}}
	objectiveEvent := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: "episode-2-start", TransactionTime: startedAt, ValidTime: startedAt, EpisodeID: "episode-2", Type: "episode.started", Payload: map[string]any{"objective_id": goal.ObjectiveID, "world_id": worldID}, Provenance: map[string]any{"source": "test"}}
	if err := store.CreateObjective(ctx, goal, objectiveEvent); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: "episode-2-obs", TransactionTime: startedAt.Add(time.Second), ValidTime: startedAt.Add(time.Second), EpisodeID: "episode-2", Type: "world.observation", Payload: map[string]any{"world_id": worldID, "world_version": 2, "observation_type": "directory_listing"}, Provenance: map[string]any{"source": "test"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: "frame-2-created", TransactionTime: startedAt, ValidTime: startedAt, EpisodeID: "episode-2", BranchID: "branch-2", Type: "frame.created", Payload: map[string]any{"frame_id": current.FrameID}, Provenance: map[string]any{"source": "test"}}); err != nil {
		t.Fatal(err)
	}

	// Store read path: versions derived from the lifecycle events.
	claims, err := store.ListClaimsByWorld(ctx, worldID)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]int{}
	for _, claim := range claims {
		versions[claim.ClaimID] = claim.WorldVersion
	}
	if versions["claim-green"] != 1 {
		t.Fatalf("claim-green must derive world_version 1 from its lifecycle, got %v", versions)
	}

	renderer := render.New(store)
	packet, err := renderer.Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000, WorldID: worldID})
	if err != nil {
		t.Fatal(err)
	}
	section := sectionByKind(packet, "world_memory")
	if section == nil {
		t.Fatalf("world_memory section missing: %#v", packet.Sections)
	}
	states := map[string]map[string]any{}
	for _, item := range section.Items {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("world_memory item has type %T", item)
		}
		if ref, _ := entry["ref"].(string); ref != "" {
			states[ref] = entry
		}
	}
	green := states["claim:claim-green"]
	if green == nil {
		t.Fatalf("claim-green missing from world_memory: %v", states)
	}
	if green["stale"] != true {
		t.Fatalf("a v1-earned claim delivered to a v2 world must be marked stale: %#v", green)
	}
	if green["world_version"] != 1 {
		t.Fatalf("stale marker must carry the earned-against version: %#v", green)
	}
	if note, _ := green["note"].(string); note == "" {
		t.Fatalf("stale marker must explain itself: %#v", green)
	}
	structure := states["claim:claim-structure"]
	if structure == nil {
		t.Fatalf("claim-structure missing from world_memory: %v", states)
	}
	if structure["stale"] == true {
		t.Fatalf("a claim whose lifecycle was touched at v2 must not be stale: %#v", structure)
	}
}
