package render_test

import (
	"context"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/procedure"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

// TestRenderWorldMemoryAcrossEpisodes locks the longitudinal-memory contract
// (M13/M15): a frame bound to a world sees the durable claims prior episodes
// built in that world — through the normal render path, not a side channel.
// Evidence-backed claims (ingestion), episode-authored claims (model
// emissions), and their exclusions (refuted, foreign episodes, the frame's own
// episode) must all behave as documented.
func TestRenderWorldMemoryAcrossEpisodes(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	worldEvent := func(id, episode, branch, eventType string, at time.Time, payload map[string]any) protocol.Event {
		event := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: episode, BranchID: branch, Type: eventType, Payload: payload, Provenance: map[string]any{"source": "test"}}
		event.ApplyDefaults(at)
		return event
	}

	// Episode 1 acted in world-1: a world observation plus three claims —
	// one evidence-backed (ingestion style), one model-authored (no evidence),
	// one that gets refuted before episode 2 starts.
	observation := worldEvent("obs-1", "episode-1", "branch-1", "world.observation", base, map[string]any{"world_id": "world-1", "observation_type": "directory_listing", "payload": map[string]any{"entries": 4}})
	if err := store.Append(ctx, observation); err != nil {
		t.Fatal(err)
	}
	commit := func(id, createdEvent, proposition string, evidence []string) cognition.Commit {
		at := base.Add(time.Minute)
		event := worldEvent(createdEvent, "episode-1", "branch-1", "claim.candidate", at, map[string]any{"claim_id": id})
		claim := cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: id, Proposition: proposition, Confidence: 0.8, Status: cognition.ClaimCandidate, CreatedEvent: createdEvent, ValidFrom: at}
		return cognition.Commit{Event: event, Claim: claim, Evidence: evidence}
	}
	if err := store.CommitClaim(ctx, commit("claim-ev", "event-ev", "repository declares Go module example.com/ledger", []string{"obs-1"})); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitClaim(ctx, commit("claim-model", "event-model", "failing test TestTotalHandlesLargeValues is caused by the int32 accumulator", nil)); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitClaim(ctx, commit("claim-refuted", "event-refuted", "temporary hypothesis that will be refuted", []string{"obs-1"})); err != nil {
		t.Fatal(err)
	}
	refuteAt := base.Add(2 * time.Minute)
	if _, err := store.TransitionClaim(ctx, cognition.Transition{Event: worldEvent("event-refutation", "episode-1", "branch-1", "claim.refuted", refuteAt, map[string]any{"claim_id": "claim-refuted"}), ClaimID: "claim-refuted", ToStatus: cognition.ClaimRefuted, ValidAt: refuteAt}); err != nil {
		t.Fatal(err)
	}
	// A foreign episode that never saw world-1 must not leak into its memory.
	foreign := worldEvent("obs-foreign", "episode-3", "branch-3", "world.observation", base.Add(3*time.Minute), map[string]any{"world_id": "world-9", "observation_type": "directory_listing"})
	if err := store.Append(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	foreignAt := base.Add(3 * time.Minute)
	if err := store.CommitClaim(ctx, cognition.Commit{Event: worldEvent("event-foreign", "episode-3", "branch-3", "claim.candidate", foreignAt, map[string]any{"claim_id": "claim-foreign"}), Claim: cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: "claim-foreign", Proposition: "claim about a different world", Confidence: 0.8, Status: cognition.ClaimCandidate, CreatedEvent: "event-foreign", ValidFrom: foreignAt}, Evidence: []string{"obs-foreign"}}); err != nil {
		t.Fatal(err)
	}

	// Episode 2: a new episode in the same world, created the way a warm agent
	// arrives — objective plus frame, no bootstrap ingestion of its own.
	startedAt := base.Add(10 * time.Minute)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective-2", EpisodeID: "episode-2", Text: "find the failing test in the ledger", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-2", AgentID: "agent-2", EpisodeID: "episode-2", BranchID: "branch-2", ObjectiveID: goal.ObjectiveID, AsOf: startedAt, Focus: frame.Focus{Type: frame.RefQuery, Query: "failing test"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 16000}}
	if err := store.CreateObjective(ctx, goal, worldEvent("episode-2-start", "episode-2", "", "episode.started", startedAt, map[string]any{"objective_id": goal.ObjectiveID, "world_id": "world-1"})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, worldEvent("frame-2-created", "episode-2", "branch-2", "frame.created", startedAt, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}

	// Prior-episode procedures must be reusable through the same world scope.
	if err := store.ReplaceProcedures(ctx, "episode-1", []procedure.Procedure{{Protocol: protocol.Name, Version: protocol.Version, ProcedureID: "procedure-world-1", EpisodeID: "episode-1", SemanticTrigger: "find failing test", Preconditions: procedure.Preconditions{RequiredArguments: []string{}}, AffordanceSequence: []procedure.AffordanceStep{{Position: 0, AffordanceID: "read_file"}}, ExpectedOutcomes: []procedure.ExpectedOutcome{}, EvidenceExecutionIDs: []string{"execution-1"}, Successes: 1, SuccessRate: 1, ProjectionVersion: procedure.ProjectorVersion}}); err != nil {
		t.Fatal(err)
	}

	renderer := render.New(store)
	packet, err := renderer.Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000, WorldID: "world-1"})
	if err != nil {
		t.Fatal(err)
	}
	section := sectionByKind(packet, "world_memory")
	if section == nil {
		t.Fatalf("world_memory section missing for a world-bound frame: %#v", packet.Sections)
	}
	refs := map[string]bool{}
	states := map[string]map[string]any{}
	for _, item := range section.Items {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("world_memory item has type %T", item)
		}
		ref, _ := entry["ref"].(string)
		refs[ref] = true
		states[ref] = entry
		if entry["scope"] != "world" {
			t.Fatalf("world_memory item %q lost its scope: %#v", ref, entry)
		}
	}
	if !refs["claim:claim-ev"] || !refs["claim:claim-model"] {
		t.Fatalf("world_memory must carry the evidence-backed and the model-authored claim: %v", refs)
	}
	// Pivot Этап 4: knowledge is delivered by state. The evidence-backed claim
	// stays a hypothesis here (it was committed as candidate), and the refuted
	// one enters as explicit history — never as a fact.
	if states["claim:claim-ev"]["state"] != "hypothesis" || states["claim:claim-model"]["state"] != "hypothesis" {
		t.Fatalf("live claims must be labeled hypothesis: %v", states)
	}
	if states["claim:claim-refuted"]["state"] != "refuted" {
		t.Fatalf("refuted claim must be delivered as refuted history: %v", states["claim:claim-refuted"])
	}
	if refs["claim:claim-foreign"] {
		t.Fatal("foreign-world claim leaked into world_memory")
	}

	// Procedures: episode-2 has none of its own; the match must come from the
	// prior episode through the world scope.
	procedures := sectionByKind(packet, "procedures")
	if procedures == nil || len(procedures.Items) != 1 {
		t.Fatalf("expected one cross-episode procedure match: %#v", procedures)
	}
	match, ok := procedures.Items[0].(render.ProcedureMatch)
	if !ok || match.Procedure.ProcedureID != "procedure-world-1" || match.Procedure.EpisodeID != "episode-1" {
		t.Fatalf("unexpected procedure match: %#v", procedures.Items[0])
	}

	// Without a world binding the packet keeps its historical shape: no
	// world_memory section, and procedures stay scoped to the own episode.
	bare, err := renderer.Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if found := sectionByKind(bare, "world_memory"); found != nil {
		t.Fatalf("world_memory section present without world binding: %#v", found)
	}
	if found := sectionByKind(bare, "procedures"); found != nil && len(found.Items) > 0 {
		t.Fatalf("procedures leaked across episodes without world binding: %#v", found.Items)
	}

	// The store-level read path agrees with the render filter: the world claim
	// base contains exactly the reusable knowledge plus the refuted claim
	// (status filtering is the renderer's contract).
	worldClaims, err := store.ListClaimsByWorld(ctx, "world-1")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, claim := range worldClaims {
		ids[claim.ClaimID] = true
	}
	if !ids["claim-ev"] || !ids["claim-model"] || !ids["claim-refuted"] || ids["claim-foreign"] {
		t.Fatalf("ListClaimsByWorld returned unexpected scope: %v", ids)
	}
	episodes, err := store.ListWorldEpisodes(ctx, "world-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 2 || episodes[0] != "episode-1" || episodes[1] != "episode-2" {
		t.Fatalf("ListWorldEpisodes returned %v", episodes)
	}
}

// TestRenderWorldMemoryDeliversKnowledgeState pins the pivot-Этап-4 contract:
// warm memory arrives as CONFIRMED (with evidence refs) → REFUTED (as
// do-not-repeat history) → INVESTIGATED (prior focus targets) → HYPOTHESES,
// in that order, so trimming eats speculation first and a refuted claim can
// never pose as a fact.
func TestRenderWorldMemoryDeliversKnowledgeState(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	worldEvent := func(id, episode, branch, eventType string, at time.Time, payload map[string]any) protocol.Event {
		event := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: episode, BranchID: branch, Type: eventType, Payload: payload, Provenance: map[string]any{"source": "test"}}
		event.ApplyDefaults(at)
		return event
	}
	// Episode 1 in world-1: an observation, a confirmed fact citing it, a live
	// hypothesis, a refuted claim, and a focus trajectory.
	if err := store.Append(ctx, worldEvent("obs-1", "episode-1", "branch-1", "world.observation", base, map[string]any{"world_id": "world-1"})); err != nil {
		t.Fatal(err)
	}
	commit := func(id, proposition string, status cognition.ClaimStatus, confidence float32, evidence []string) {
		t.Helper()
		at := base.Add(time.Minute)
		event := worldEvent("event-"+id, "episode-1", "branch-1", "claim.candidate", at, map[string]any{"claim_id": id})
		claim := cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: id, Proposition: proposition, Confidence: confidence, Status: status, CreatedEvent: event.EventID, ValidFrom: at}
		if err := store.CommitClaim(ctx, cognition.Commit{Event: event, Claim: claim, Evidence: evidence}); err != nil {
			t.Fatal(err)
		}
	}
	commit("claim-fact", "series.Accumulate uses int32", cognition.ClaimSupported, 0.95, []string{"obs-1"})
	commit("claim-guess", "store.Remove may leave a stale count", cognition.ClaimCandidate, 0.6, nil)
	commit("claim-wrong", "internal/reports is the source of the failing tests", cognition.ClaimCandidate, 0.85, []string{"obs-1"})
	refuteAt := base.Add(3 * time.Minute)
	if _, err := store.TransitionClaim(ctx, cognition.Transition{Event: worldEvent("event-refute", "episode-1", "branch-1", "claim.refuted", refuteAt, map[string]any{"claim_id": "claim-wrong"}), ClaimID: "claim-wrong", ToStatus: cognition.ClaimRefuted, ValidAt: refuteAt}); err != nil {
		t.Fatal(err)
	}
	// The prior episode's investigation trajectory: it studied internal/reports
	// and then moved on.
	focusAt := base.Add(4 * time.Minute)
	if err := store.Append(ctx, worldEvent("focus-1", "episode-1", "branch-1", "focus.changed", focusAt, map[string]any{"from": "query:failing test", "to": "query:internal/reports", "trigger": "deliberate"})); err != nil {
		t.Fatal(err)
	}

	// Episode 2 renders against the same world.
	startedAt := base.Add(10 * time.Minute)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective-2", EpisodeID: "episode-2", Text: "find the failing test", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-2", AgentID: "agent-2", EpisodeID: "episode-2", BranchID: "branch-2", ObjectiveID: goal.ObjectiveID, AsOf: startedAt, Focus: frame.Focus{Type: frame.RefQuery, Query: "failing test"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 16000}}
	if err := store.CreateObjective(ctx, goal, worldEvent("episode-2-start", "episode-2", "branch-2", "episode.started", startedAt, map[string]any{"objective_id": goal.ObjectiveID, "world_id": "world-1"})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, worldEvent("frame-2-created", "episode-2", "branch-2", "frame.created", startedAt, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}

	packet, err := render.New(store).Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000, WorldID: "world-1"})
	if err != nil {
		t.Fatal(err)
	}
	section := sectionByKind(packet, "world_memory")
	if section == nil {
		t.Fatal("world_memory section missing")
	}
	wantOrder := []string{"confirmed", "refuted", "investigated", "hypothesis"}
	seenOrder := make([]string, 0, 4)
	byState := map[string]map[string]any{}
	for _, item := range section.Items {
		entry := item.(map[string]any)
		state, _ := entry["state"].(string)
		if _, exists := byState[state]; !exists {
			seenOrder = append(seenOrder, state)
		}
		byState[state] = entry
	}
	if len(seenOrder) != len(wantOrder) {
		t.Fatalf("group order = %v, want %v", seenOrder, wantOrder)
	}
	for i := range wantOrder {
		if seenOrder[i] != wantOrder[i] {
			t.Fatalf("group order = %v, want %v", seenOrder, wantOrder)
		}
	}
	if ref, _ := byState["confirmed"]["ref"].(string); ref != "claim:claim-fact" {
		t.Fatalf("confirmed group must carry the cited fact: %v", byState["confirmed"])
	}
	if evidence, ok := byState["confirmed"]["evidence"].([]string); !ok || len(evidence) != 1 || evidence[0] != "event:obs-1" {
		t.Fatalf("confirmed fact must cite its evidence: %v", byState["confirmed"])
	}
	if ref, _ := byState["refuted"]["ref"].(string); ref != "claim:claim-wrong" {
		t.Fatalf("refuted group must carry the retired hypothesis: %v", byState["refuted"])
	}
	if _, hasConf := byState["refuted"]["confidence"]; hasConf {
		t.Fatalf("retired history must not carry confidence (it would read as a fact): %v", byState["refuted"])
	}
	if target, _ := byState["investigated"]["target"].(string); target != "query:internal/reports" {
		t.Fatalf("investigated group must carry the prior focus target: %v", byState["investigated"])
	}
	if ref, _ := byState["hypothesis"]["ref"].(string); ref != "claim:claim-guess" {
		t.Fatalf("hypothesis group must carry the live guess: %v", byState["hypothesis"])
	}
}

// TestRenderWorldMemoryConfirmedModeServesFactsOnly pins the warm-facts
// benchmark arm (pivot TZ §16 C): with the confirmed mode the packet carries
// prior FACTS and nothing else — no refuted paths, no investigation history,
// no live hypotheses — so C vs D measures the value of the investigation
// story rather than the mere presence of facts.
func TestRenderWorldMemoryConfirmedModeServesFactsOnly(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	worldEvent := func(id, episode, branch, eventType string, at time.Time, payload map[string]any) protocol.Event {
		event := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: episode, BranchID: branch, Type: eventType, Payload: payload, Provenance: map[string]any{"source": "test"}}
		event.ApplyDefaults(at)
		return event
	}
	if err := store.Append(ctx, worldEvent("obs-1", "episode-1", "branch-1", "world.observation", base, map[string]any{"world_id": "world-1"})); err != nil {
		t.Fatal(err)
	}
	commit := func(id, proposition string, status cognition.ClaimStatus, confidence float32, evidence []string) {
		t.Helper()
		at := base.Add(time.Minute)
		event := worldEvent("event-"+id, "episode-1", "branch-1", "claim.candidate", at, map[string]any{"claim_id": id})
		claim := cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: id, Proposition: proposition, Confidence: confidence, Status: status, CreatedEvent: event.EventID, ValidFrom: at}
		if err := store.CommitClaim(ctx, cognition.Commit{Event: event, Claim: claim, Evidence: evidence}); err != nil {
			t.Fatal(err)
		}
	}
	commit("claim-fact", "series.Accumulate uses int32", cognition.ClaimSupported, 0.95, []string{"obs-1"})
	commit("claim-wrong", "internal/reports is the source of the failing tests", cognition.ClaimCandidate, 0.85, []string{"obs-1"})
	refuteAt := base.Add(3 * time.Minute)
	if _, err := store.TransitionClaim(ctx, cognition.Transition{Event: worldEvent("event-refute", "episode-1", "branch-1", "claim.refuted", refuteAt, map[string]any{"claim_id": "claim-wrong"}), ClaimID: "claim-wrong", ToStatus: cognition.ClaimRefuted, ValidAt: refuteAt}); err != nil {
		t.Fatal(err)
	}
	commit("claim-guess", "store.Remove may leave a stale count", cognition.ClaimCandidate, 0.6, nil)
	if err := store.Append(ctx, worldEvent("focus-1", "episode-1", "branch-1", "focus.changed", base.Add(4*time.Minute), map[string]any{"from": "query:failing test", "to": "query:internal/reports", "trigger": "deliberate"})); err != nil {
		t.Fatal(err)
	}

	startedAt := base.Add(10 * time.Minute)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective-2", EpisodeID: "episode-2", Text: "find the failing test", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-2", AgentID: "agent-2", EpisodeID: "episode-2", BranchID: "branch-2", ObjectiveID: goal.ObjectiveID, AsOf: startedAt, Focus: frame.Focus{Type: frame.RefQuery, Query: "failing test"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 16000}}
	if err := store.CreateObjective(ctx, goal, worldEvent("episode-2-start", "episode-2", "branch-2", "episode.started", startedAt, map[string]any{"objective_id": goal.ObjectiveID, "world_id": "world-1"})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, worldEvent("frame-2-created", "episode-2", "branch-2", "frame.created", startedAt, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}

	packet, err := render.NewWithMode(store, render.WorldMemoryModeConfirmed).Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000, WorldID: "world-1"})
	if err != nil {
		t.Fatal(err)
	}
	section := sectionByKind(packet, "world_memory")
	if section == nil {
		t.Fatal("world_memory section missing")
	}
	if len(section.Items) != 1 {
		t.Fatalf("confirmed mode must serve the single prior fact, got %v", section.Items)
	}
	entry, _ := section.Items[0].(map[string]any)
	if ref, _ := entry["ref"].(string); ref != "claim:claim-fact" {
		t.Fatalf("confirmed mode must carry the fact: %v", entry)
	}
	if state, _ := entry["state"].(string); state != "confirmed" {
		t.Fatalf("confirmed mode item must stay state=confirmed: %v", entry)
	}

	// An unknown mode must fail loudly instead of quietly running the wrong
	// experiment arm.
	if _, err = render.NewWithMode(store, "facts").Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000, WorldID: "world-1"}); err == nil {
		t.Fatal("unknown world-memory mode must fail renders")
	}
}
