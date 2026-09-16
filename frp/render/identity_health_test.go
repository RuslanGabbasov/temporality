package render_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

// identityEvent is healthEvent plus agent authorship: claim provenance
// (created_event author) is what separates the agent's own commitments from
// imported knowledge, so identity tests must control it explicitly.
func identityEvent(id string, at time.Time, agentID string) protocol.Event {
	value := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: at, ValidTime: at, AgentID: agentID, EpisodeID: "episode", BranchID: "branch", Type: "claim.candidate", Payload: map[string]any{"claim_id": id}, Provenance: map[string]any{"source": "identity-test"}}
	value.ApplyDefaults(at)
	return value
}

func commitIdentityClaim(t *testing.T, store *memory.Store, id, proposition, agentID string, at time.Time) {
	t.Helper()
	event := identityEvent("event-"+id, at, agentID)
	claim := cognition.Claim{ClaimID: id, Proposition: proposition, Confidence: 0.9, Status: cognition.ClaimCandidate, CreatedEvent: event.EventID, ValidFrom: at}
	claim.ApplyDefaults(at)
	if err := store.CommitClaim(context.Background(), cognition.Commit{Event: event, Claim: claim}); err != nil {
		t.Fatal(err)
	}
}

func identitySection(t *testing.T, packet render.Packet, kind string) *render.Section {
	t.Helper()
	for i := range packet.Sections {
		if packet.Sections[i].Kind == kind {
			return &packet.Sections[i]
		}
	}
	return nil
}

// TestRenderIdentityAnchorListsOwnCommitments: the identity section must anchor
// the agent with its own live commitments — newest first, provenance-filtered
// (imported knowledge and other agents' claims are not commitments) — plus the
// episode step depth. A retired own commitment leaves the anchor; a single
// reconciliation is health, not drift, so no identity_health renders.
func TestRenderIdentityAnchorListsOwnCommitments(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	root := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-identity", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, root, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": root.FrameID})); err != nil {
		t.Fatal(err)
	}
	commitIdentityClaim(t, store, "claim-self-old", "the failing test lives in main_test.go", "agent", base.Add(1*time.Minute))
	commitIdentityClaim(t, store, "claim-self-new", "Sum returns a minus b in main.go", "agent", base.Add(2*time.Minute))
	// Imported knowledge (no author) and another agent's claim are not this
	// agent's commitments.
	commitIdentityClaim(t, store, "claim-imported", "repository is a Go module", "", base.Add(3*time.Minute))
	commitIdentityClaim(t, store, "claim-other-agent", "another agent asserted this", "other", base.Add(4*time.Minute))
	// A retired own commitment leaves the anchor without raising an alarm.
	commitIdentityClaim(t, store, "claim-self-retired", "a position later retracted", "agent", base.Add(5*time.Minute))
	retireEvent := identityEvent("event-retire", base.Add(6*time.Minute), "agent")
	retireEvent.Type = "claim.superseded"
	if _, err := store.TransitionClaim(ctx, cognition.Transition{Event: retireEvent, ClaimID: "claim-self-retired", ToStatus: cognition.ClaimSuperseded, ValidAt: base.Add(6 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	leaf := transitionChain(t, store, root, []frame.Focus{{Type: frame.RefQuery, Query: "sum"}, {Type: frame.RefQuery, Query: "main.go"}}, base, false)

	packet, err := render.New(store).Render(ctx, render.Request{FrameID: leaf.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	identity := identitySection(t, packet, "identity")
	if identity == nil {
		t.Fatal("identity section missing")
	}
	anchor, ok := identity.Items[0].(map[string]any)
	if !ok {
		t.Fatalf("identity item type %T", identity.Items[0])
	}
	if anchor["objective_id"] != goal.ObjectiveID || anchor["steps"] != 2 {
		t.Fatalf("anchor objective/steps = %v/%v, want %v/2", anchor["objective_id"], anchor["steps"], goal.ObjectiveID)
	}
	encoded := sectionItemsJSON(t, *identity)
	if want := "\"ref\":\"claim:claim-self-new\""; !strings.Contains(encoded, want) {
		t.Fatalf("anchor missing newest commitment: %s", encoded)
	}
	if want := "\"ref\":\"claim:claim-self-old\""; !strings.Contains(encoded, want) {
		t.Fatalf("anchor missing older commitment: %s", encoded)
	}
	for _, forbidden := range []string{"claim-imported", "claim-other-agent", "claim-self-retired"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("anchor must not list %q: %s", forbidden, encoded)
		}
	}
	if strings.Index(encoded, "claim-self-new") > strings.Index(encoded, "claim-self-old") {
		t.Fatalf("commitments must be newest first: %s", encoded)
	}
	if section := identitySection(t, packet, "identity_health"); section != nil {
		t.Fatalf("one retired commitment is reconciliation, not drift: %#v", section)
	}
}

// TestRenderIdentityHealthSurfacesSelfTension: when every claim in a tension
// group is the agent's own, the packet must say the agent disagrees with
// itself — with refs the model can reconcile through claim_ops.
func TestRenderIdentityHealthSurfacesSelfTension(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	root := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-self-tension", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, root, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": root.FrameID})); err != nil {
		t.Fatal(err)
	}
	commitIdentityClaim(t, store, "claim-self-a", "the failing test is TestSum", "agent", base.Add(1*time.Minute))
	commitIdentityClaim(t, store, "claim-self-b", "The failing test is TestSum", "agent", base.Add(2*time.Minute))

	packet, err := render.New(store).Render(ctx, render.Request{FrameID: root.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	section := identitySection(t, packet, "identity_health")
	if section == nil {
		t.Fatal("identity_health section missing for self-authored tension")
	}
	encoded := sectionItemsJSON(t, *section)
	for _, want := range []string{"\"self_tension_groups\":1", "\"kind\":\"duplicate\"", "claim:claim-self-a", "claim:claim-self-b", "reconcile"} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("identity_health payload missing %q: %s", want, encoded)
		}
	}
	if identitySection(t, packet, "memory_health") == nil {
		t.Fatal("memory_health must still carry the full tension detail")
	}
}

// TestRenderIdentityHealthIgnoresImportedTension: tension among imported
// claims is a memory problem (memory_health), not an identity problem — the
// agent is not disagreeing with itself.
func TestRenderIdentityHealthIgnoresImportedTension(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	root := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-imported-tension", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, root, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": root.FrameID})); err != nil {
		t.Fatal(err)
	}
	commitIdentityClaim(t, store, "claim-ingest-a", "documentation says router is react-router", "", base.Add(1*time.Minute))
	commitIdentityClaim(t, store, "claim-ingest-b", "documentation says Router is React-Router", "", base.Add(2*time.Minute))
	commitIdentityClaim(t, store, "claim-self-sole", "one consistent own position", "agent", base.Add(3*time.Minute))

	packet, err := render.New(store).Render(ctx, render.Request{FrameID: root.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if identitySection(t, packet, "memory_health") == nil {
		t.Fatal("imported tension must surface in memory_health")
	}
	if section := identitySection(t, packet, "identity_health"); section != nil {
		t.Fatalf("imported tension is not identity drift: %#v", section)
	}
}

// TestRenderMemoryHealthIgnoresForeignEpisodes: the claim table is global (a
// shared database holds every episode and every integration-test fixture),
// but a frame's memory is only what its own branch observed. Duplicate claims
// created inside a foreign episode must not surface as this frame's tensions
// — they once flooded memory_health with hundreds of foreign members and
// even goaded the model into refuting claims it never saw.
func TestRenderMemoryHealthIgnoresForeignEpisodes(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	root := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-foreign", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, root, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": root.FrameID})); err != nil {
		t.Fatal(err)
	}
	commitIdentityClaim(t, store, "claim-local-a", "the failing test is TestSum", "agent", base.Add(1*time.Minute))
	commitIdentityClaim(t, store, "claim-local-b", "The failing test is TestSum", "agent", base.Add(2*time.Minute))
	// Duplicates committed inside a foreign episode (as an integration-test
	// fixture would be in a shared database): they must stay invisible here.
	// Their valid times deliberately PRECEDE this episode's events — a claim
	// from the past of another episode passes the cutoff projection, so only
	// episode scoping can keep it out (the time filter once masked this bug).
	foreign1 := identityEvent("event-foreign-1", base.Add(10*time.Second), "")
	foreign1.EpisodeID, foreign1.BranchID = "foreign-episode", "foreign-branch"
	foreignClaim1 := cognition.Claim{ClaimID: "claim-foreign-1", Proposition: "integration claim", Confidence: 0.82, Status: cognition.ClaimCandidate, CreatedEvent: foreign1.EventID, ValidFrom: foreign1.ValidTime}
	foreignClaim1.ApplyDefaults(foreign1.ValidTime)
	if err := store.CommitClaim(ctx, cognition.Commit{Event: foreign1, Claim: foreignClaim1}); err != nil {
		t.Fatal(err)
	}
	foreign2 := identityEvent("event-foreign-2", base.Add(20*time.Second), "")
	foreign2.EpisodeID, foreign2.BranchID = "foreign-episode", "foreign-branch"
	foreignClaim2 := cognition.Claim{ClaimID: "claim-foreign-2", Proposition: "integration claim", Confidence: 0.82, Status: cognition.ClaimCandidate, CreatedEvent: foreign2.EventID, ValidFrom: foreign2.ValidTime}
	foreignClaim2.ApplyDefaults(foreign2.ValidTime)
	if err := store.CommitClaim(ctx, cognition.Commit{Event: foreign2, Claim: foreignClaim2}); err != nil {
		t.Fatal(err)
	}

	packet, err := render.New(store).Render(ctx, render.Request{FrameID: root.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	encoded := sectionItemsJSON(t, *identitySection(t, packet, "identity"))
	for _, forbidden := range []string{"claim-foreign-1", "claim-foreign-2", "integration claim"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("foreign episode leaked into the packet: %s", encoded)
		}
	}
	// The in-episode duplicate pair still reports — scoping removed the noise,
	// not the diagnostic — without the foreign members.
	memory := identitySection(t, packet, "memory_health")
	if memory == nil {
		t.Fatal("in-episode tension must still surface in memory_health")
	}
	memoryEncoded := sectionItemsJSON(t, *memory)
	if strings.Contains(memoryEncoded, "claim-foreign") || strings.Contains(memoryEncoded, "integration claim") {
		t.Fatalf("foreign claims leaked into memory_health: %s", memoryEncoded)
	}
	if !strings.Contains(memoryEncoded, "claim-local-a") {
		t.Fatalf("local tension missing from memory_health: %s", memoryEncoded)
	}
}

// TestRenderMemoryHealthCapsGroupMembers: a pathological duplicate loop (or a
// polluted shared database) can grow one tension group beyond what a frame
// needs to see; the group is capped with an explicit claims_omitted count.
func TestRenderMemoryHealthCapsGroupMembers(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	root := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-capped", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, root, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": root.FrameID})); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		commitIdentityClaim(t, store, fmt.Sprintf("claim-dup-%02d", i), "the structure is unknown", "agent", base.Add(time.Duration(i+1)*time.Minute))
	}

	packet, err := render.New(store).Render(ctx, render.Request{FrameID: root.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	section := identitySection(t, packet, "memory_health")
	if section == nil {
		t.Fatal("memory_health section missing")
	}
	encoded := sectionItemsJSON(t, *section)
	if !strings.Contains(encoded, `"claims_omitted":4`) {
		t.Fatalf("capped group must report 4 omitted members: %s", encoded)
	}
	if strings.Count(encoded, `"ref":"claim:claim-dup-`) > 8 {
		t.Fatalf("group must list at most 8 members: %s", encoded)
	}
}

// TestRenderIdentityCommitmentsHonorAsOf: the identity anchor is projected
// onto the render cutoff — a time-travel render of an earlier frame must show
// the commitments as they stood then, not as they stand now. This is the
// debugger's "what did the agent know at this step" guarantee for identity.
func TestRenderIdentityCommitmentsHonorAsOf(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	root := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-asof", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, root, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": root.FrameID})); err != nil {
		t.Fatal(err)
	}
	commitIdentityClaim(t, store, "claim-era", "later retracted position", "agent", base.Add(1*time.Minute))
	retire := identityEvent("event-retire-era", base.Add(2*time.Minute), "agent")
	retire.Type = "claim.superseded"
	if _, err := store.TransitionClaim(ctx, cognition.Transition{Event: retire, ClaimID: "claim-era", ToStatus: cognition.ClaimSuperseded, ValidAt: base.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	renderer := render.New(store)

	before := base.Add(90 * time.Second)
	historical, err := renderer.Render(ctx, render.Request{FrameID: root.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000, AsOf: &before})
	if err != nil {
		t.Fatal(err)
	}
	encoded := sectionItemsJSON(t, *identitySection(t, historical, "identity"))
	if !strings.Contains(encoded, "claim:claim-era") {
		t.Fatalf("historical render must show the commitment as it stood then: %s", encoded)
	}
	if section := identitySection(t, historical, "identity_health"); section != nil {
		t.Fatalf("no self tension and no churn at that moment: %#v", section)
	}

	present, err := renderer.Render(ctx, render.Request{FrameID: root.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	encoded = sectionItemsJSON(t, *identitySection(t, present, "identity"))
	if strings.Contains(encoded, "claim-era") {
		t.Fatalf("current render must not list the retired commitment: %s", encoded)
	}
}

// TestRenderIdentityHealthReportsRetiredChurn: retracting own positions in a
// pattern (threshold and beyond) is self-model churn — the packet states it so
// the model re-anchors instead of quietly rewriting itself step after step.
func TestRenderIdentityHealthReportsRetiredChurn(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	root := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-churn", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, root, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": root.FrameID})); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"claim-churn-1", "claim-churn-2"} {
		at := base.Add(time.Duration(i+1) * time.Minute)
		commitIdentityClaim(t, store, id, "position "+id, "agent", at)
		retire := identityEvent("event-retire-"+id, at.Add(30*time.Second), "agent")
		retire.Type = "claim.refuted"
		if _, err := store.TransitionClaim(ctx, cognition.Transition{Event: retire, ClaimID: id, ToStatus: cognition.ClaimRefuted, ValidAt: at.Add(30 * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}

	packet, err := render.New(store).Render(ctx, render.Request{FrameID: root.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	section := identitySection(t, packet, "identity_health")
	if section == nil {
		t.Fatal("identity_health section missing for self-model churn")
	}
	encoded := sectionItemsJSON(t, *section)
	for _, want := range []string{"\"retired_commitments\":2", "claim:claim-churn-1", "claim:claim-churn-2", "re-anchor"} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("identity_health payload missing %q: %s", want, encoded)
		}
	}
}
