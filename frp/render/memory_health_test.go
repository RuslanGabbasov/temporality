package render_test

import (
	"context"
	"encoding/json"
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

// TestRenderMemoryHealthSurfacesClaimTensions: the packet must state, as a
// fact, which parts of the claim base cannot be trusted blindly — duplicated
// propositions and functional-triple contradictions — with claim ids and
// confidences, so reconciliation becomes the model's move instead of silent
// memory rot.
func TestRenderMemoryHealthSurfacesClaimTensions(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	root := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-memory", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, root, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": root.FrameID})); err != nil {
		t.Fatal(err)
	}
	// A duplicate pair and a functional contradiction, committed the way the
	// ingestion pipeline commits triple claims.
	commitTensionClaim(t, store, "claim-dup-1", "No test results are present", base)
	commitTensionClaim(t, store, "claim-dup-2", "no test results are present", base)
	commitTensionClaim(t, store, "claim-contra-1", "module declared in main.go", base, "module:demo", "declared_in", "file:main.go")
	commitTensionClaim(t, store, "claim-contra-2", "module declared in other.go", base, "module:demo", "declared_in", "file:other.go")

	renderer := render.New(store)
	packet, err := renderer.Render(ctx, render.Request{FrameID: root.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	var section *render.Section
	for i := range packet.Sections {
		if packet.Sections[i].Kind == "memory_health" {
			section = &packet.Sections[i]
		}
	}
	if section == nil {
		t.Fatal("memory_health section missing")
	}
	if len(section.Items) != 2 {
		t.Fatalf("memory_health items = %#v, want duplicate + contradiction", section.Items)
	}
	encoded := strings.TrimSpace(sectionItemsJSON(t, *section))
	for _, want := range []string{"duplicate", "contradiction", "claim-dup-1", "claim-contra-2", "declared_in"} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("memory_health payload missing %q: %s", want, encoded)
		}
	}
}

// TestRenderMemoryHealthOmittedOnCleanMemory: a clean claim base renders no
// memory_health section at all — tension diagnostics are rare by design, and
// an always-present empty section would tax every render's budget.
func TestRenderMemoryHealthOmittedOnCleanMemory(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	root := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-clean", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, root, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": root.FrameID})); err != nil {
		t.Fatal(err)
	}
	commitTensionClaim(t, store, "claim-lone", "a single healthy claim", base)
	renderer := render.New(store)
	packet, err := renderer.Render(ctx, render.Request{FrameID: root.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	for i := range packet.Sections {
		if packet.Sections[i].Kind == "memory_health" {
			t.Fatalf("clean memory produced a memory_health section: %#v", packet.Sections[i])
		}
	}
}

func commitTensionClaim(t *testing.T, store *memory.Store, id, proposition string, base time.Time, triple ...string) {
	t.Helper()
	at := base.Add(time.Duration(len(id)) * time.Minute)
	event := healthEvent("event-"+id, "claim.candidate", at, map[string]any{"claim_id": id})
	claim := cognition.Claim{ClaimID: id, Proposition: proposition, Confidence: 0.9, Status: cognition.ClaimCandidate, CreatedEvent: event.EventID, ValidFrom: at}
	claim.ApplyDefaults(at)
	if len(triple) == 3 {
		claim.Subject, claim.Predicate, claim.Object = triple[0], triple[1], triple[2]
	}
	if err := store.CommitClaim(context.Background(), cognition.Commit{Event: event, Claim: claim}); err != nil {
		t.Fatal(err)
	}
}

func sectionItemsJSON(t *testing.T, section render.Section) string {
	t.Helper()
	encoded, err := json.Marshal(section.Items)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
