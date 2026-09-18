package render_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

// TestRenderBoundsExecutionDiagnostics locks the packet-cost guard on captured
// command output: an execution.failed event may carry tens of kilobytes of
// stdout in error.diagnostics, and two such events once dominated every later
// packet. The packet must carry a bounded prefix; the stored event must keep
// the full output for audit (and must not be mutated by rendering, which the
// in-memory store would otherwise surface immediately).
func TestRenderBoundsExecutionDiagnostics(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, renderEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", 20_000)
	failed := renderEvent("failure-event", "execution.failed", base.Add(time.Second), map[string]any{
		"execution_id": "x-1",
		"error": map[string]any{
			"class": "process_failed", "message": "tests failed", "retryable": false,
			"diagnostics": map[string]any{"stdout": huge, "stderr": strings.Repeat("y", 5_000)},
		},
	})
	if err := store.Append(ctx, failed); err != nil {
		t.Fatal(err)
	}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "failing test"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 100_000}}
	if err := store.CreateFrame(ctx, current, renderEvent("frame-event", "frame.created", base, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}
	packet, err := render.New(store).Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 10_000_000})
	if err != nil {
		t.Fatal(err)
	}
	recent := packet.Section("recent")
	if recent == nil {
		t.Fatalf("recent section missing: %#v", packet.Sections)
	}
	var bounded bool
	for _, item := range recent.Items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		payload, _ := entry["payload"].(map[string]any)
		errMap, _ := payload["error"].(map[string]any)
		diag, _ := errMap["diagnostics"].(map[string]any)
		stdout, _ := diag["stdout"].(string)
		if stdout == "" {
			continue
		}
		bounded = true
		if len(stdout) > 4096+len("…[truncated]") || !strings.HasSuffix(stdout, "…[truncated]") {
			t.Fatalf("stdout not bounded: %d bytes, suffix %q", len(stdout), stdout[len(stdout)-40:])
		}
	}
	if !bounded {
		t.Fatalf("failed execution diagnostics never reached the recent section: %#v", recent.Items)
	}
	// The stored event keeps the full output: rendering is copy-on-write.
	stored, err := store.Get(ctx, "failure-event")
	if err != nil {
		t.Fatal(err)
	}
	errMap, _ := stored.Payload["error"].(map[string]any)
	diag, _ := errMap["diagnostics"].(map[string]any)
	stdout, _ := diag["stdout"].(string)
	if len(stdout) != 20_000 {
		t.Fatalf("render mutated the stored event payload: %d bytes", len(stdout))
	}
}

// TestRenderWorldMemoryRanksRelevantClaimsFirst locks the relevance ranking:
// within the confirmed and hypothesis groups, lexical relevance to the current
// objective outranks raw confidence — a cited fact about the task beats a
// louder fact about something unrelated.
func TestRenderWorldMemoryRanksRelevantClaimsFirst(t *testing.T) {
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
	commit := func(id, proposition string, confidence float32) {
		at := base.Add(time.Minute)
		if err := store.CommitClaim(ctx, cognition.Commit{Event: worldEvent("event-"+id, "episode-1", "branch-1", "claim.candidate", at, map[string]any{"claim_id": id}), Claim: cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: id, Proposition: proposition, Confidence: confidence, Status: cognition.ClaimSupported, CreatedEvent: "event-" + id, ValidFrom: at}, Evidence: []string{"obs-1"}}); err != nil {
			t.Fatal(err)
		}
	}
	// The irrelevant fact is louder (0.99) but shares no terms with the goal;
	// the relevant one is quieter but names the failing ledger test.
	commit("claim-irrelevant", "documentation recommends coffee machine descaling guidelines", 0.99)
	commit("claim-relevant", "the failing ledger test covers series addition", 0.7)

	startedAt := base.Add(10 * time.Minute)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective-2", EpisodeID: "episode-2", Text: "find the failing ledger test", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-2", AgentID: "agent-2", EpisodeID: "episode-2", BranchID: "branch-2", ObjectiveID: goal.ObjectiveID, AsOf: startedAt, Focus: frame.Focus{Type: frame.RefQuery, Query: "failing ledger test"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 16000}}
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
	section := packet.Section("world_memory")
	if section == nil {
		t.Fatal("world_memory section missing")
	}
	first, _ := section.Items[0].(map[string]any)
	if ref, _ := first["ref"].(string); ref != "claim:claim-relevant" {
		t.Fatalf("relevant claim must outrank the louder irrelevant one: %v", section.Items)
	}
}

// TestRenderWorldMemoryExcludesEffectBackedClaims locks the experience/knowledge
// split: a claim whose evidence includes a world.effect event describes what an
// episode DID (and observed right after), not what the world IS. Such claims
// poisoned the warm benchmark arms ("tests are green after the patch" read as
// a fact about a recycled world) and must never enter world memory — neither
// via the store read path nor via render.
func TestRenderWorldMemoryExcludesEffectBackedClaims(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	worldEvent := func(id, episode, branch, eventType string, at time.Time, payload map[string]any) protocol.Event {
		event := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: episode, BranchID: branch, Type: eventType, Payload: payload, Provenance: map[string]any{"source": "test"}}
		event.ApplyDefaults(at)
		return event
	}
	// Episode 1 observed the world and changed it: one observation, one effect,
	// and two otherwise-identical claims — one citing the observation (durable
	// knowledge), one citing the effect (episode experience).
	if err := store.Append(ctx, worldEvent("obs-1", "episode-1", "branch-1", "world.observation", base, map[string]any{"world_id": "world-1"})); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, worldEvent("effect-1", "episode-1", "branch-1", "world.effect", base.Add(time.Minute), map[string]any{"world_id": "world-1", "effect_type": "file.patched"})); err != nil {
		t.Fatal(err)
	}
	commit := func(id, createdEvent, proposition string, evidence []string) {
		at := base.Add(2 * time.Minute)
		if err := store.CommitClaim(ctx, cognition.Commit{Event: worldEvent(createdEvent, "episode-1", "branch-1", "claim.candidate", at, map[string]any{"claim_id": id}), Claim: cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: id, Proposition: proposition, Confidence: 0.9, Status: cognition.ClaimSupported, CreatedEvent: createdEvent, ValidFrom: at}, Evidence: evidence}); err != nil {
			t.Fatal(err)
		}
	}
	commit("claim-fact", "event-fact", "series.Accumulate uses int32", []string{"obs-1"})
	commit("claim-outcome", "event-outcome", "после правок go test проходит зелёно", []string{"effect-1"})

	worldClaims, err := store.ListClaimsByWorld(ctx, "world-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range worldClaims {
		if claim.ClaimID == "claim-outcome" {
			t.Fatal("effect-backed claim leaked into the world claim base")
		}
	}
	found := false
	for _, claim := range worldClaims {
		if claim.ClaimID == "claim-fact" {
			found = true
		}
	}
	if !found {
		t.Fatal("observation-backed fact missing from the world claim base")
	}

	// The render path agrees: world_memory carries the fact, never the outcome.
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
	section := packet.Section("world_memory")
	if section == nil {
		t.Fatal("world_memory section missing")
	}
	for _, item := range section.Items {
		entry, _ := item.(map[string]any)
		if ref, _ := entry["ref"].(string); ref == "claim:claim-outcome" {
			t.Fatalf("effect-backed claim rendered into world_memory: %v", entry)
		}
	}
}

// TestRenderSectionOrderIsPrefixStable locks the wire order (render-0.6.0):
// stable head sections (objective, affordances, world_memory, procedures)
// precede everything per-step volatile, so provider-side prompt prefix caching
// can reuse the unchanged head across a step sequence; the volatile tail ends
// with recent. The JSON field order must put sections before the per-render
// ids for the same reason.
func TestRenderSectionOrderIsPrefixStable(t *testing.T) {
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
	at := base.Add(time.Minute)
	if err := store.CommitClaim(ctx, cognition.Commit{Event: worldEvent("event-fact", "episode-1", "branch-1", "claim.candidate", at, map[string]any{"claim_id": "claim-fact"}), Claim: cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: "claim-fact", Proposition: "world fact", Confidence: 0.9, Status: cognition.ClaimSupported, CreatedEvent: "event-fact", ValidFrom: at}, Evidence: []string{"obs-1"}}); err != nil {
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
	definitions := []affordance.Definition{{Protocol: protocol.Name, Version: protocol.Version, ID: "read_file", ExecutionMode: affordance.ModeDeterministic, Capabilities: []string{"filesystem.read"}}}
	packet, err := render.New(store).Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000, WorldID: "world-1", Affordances: definitions})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"objective", "affordances", "world_memory", "procedures", "identity", "map", "focus", "attention_health", "working_set", "periphery", "recent"}
	got := make([]string, 0, len(packet.Sections))
	for _, section := range packet.Sections {
		got = append(got, section.Kind)
	}
	if len(got) != len(want) {
		t.Fatalf("section kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("section kinds = %v, want %v", got, want)
		}
	}
	wire, err := render.MarshalPacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	sectionsAt := strings.Index(string(wire), `"sections"`)
	frameIDAt := strings.Index(string(wire), `"frame_id"`)
	if sectionsAt < 0 || frameIDAt < 0 || sectionsAt > frameIDAt {
		t.Fatalf("wire form must serialize sections before per-render ids: sections@%d frame_id@%d", sectionsAt, frameIDAt)
	}
}
