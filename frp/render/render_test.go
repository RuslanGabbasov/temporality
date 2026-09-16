package render_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/procedure"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func TestRenderAffordancesSection(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "inspect the repository", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "repository"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateObjective(ctx, goal, renderEvent("objective-event", "episode.started", now, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, renderEvent("frame-event", "frame.created", now, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}
	// Deliberately unsorted: the packet must order affordances deterministically.
	definitions := []affordance.Definition{
		{Protocol: protocol.Name, Version: protocol.Version, ID: "read_file", ExecutionMode: affordance.ModeDeterministic, Capabilities: []string{"filesystem.read"}},
		{Protocol: protocol.Name, Version: protocol.Version, ID: "inspect_repository", ExecutionMode: affordance.ModeDeterministic, Capabilities: []string{"filesystem.read", "git.read"}, InputSchema: map[string]any{"path": "string"}},
	}
	renderer := render.New(store)
	packet, err := renderer.Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000, Affordances: definitions})
	if err != nil {
		t.Fatal(err)
	}
	var section *render.Section
	for i := range packet.Sections {
		if packet.Sections[i].Kind == "affordances" {
			section = &packet.Sections[i]
		}
	}
	if section == nil {
		t.Fatalf("affordances section missing: %#v", packet.Sections)
	}
	if len(section.Items) != 2 {
		t.Fatalf("unexpected affordance count: %#v", section.Items)
	}
	first, _ := section.Items[0].(map[string]any)
	second, _ := section.Items[1].(map[string]any)
	if first["id"] != "inspect_repository" || second["id"] != "read_file" {
		t.Fatalf("affordances not sorted: %#v", section.Items)
	}
	if _, ok := first["input_schema"]; !ok {
		t.Fatalf("declared input_schema missing: %#v", first)
	}
	if _, ok := second["input_schema"]; ok {
		t.Fatalf("empty input_schema must be omitted: %#v", second)
	}
	// Without definitions the packet keeps its historical shape.
	bare, err := renderer.Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	for i := range bare.Sections {
		if bare.Sections[i].Kind == "affordances" {
			t.Fatal("affordances section present without definitions")
		}
	}
}

func TestRenderIncludesDeterministicProcedureMatches(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "run release test", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "test suite"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}

	if err := store.CreateObjective(ctx, goal, renderEvent("objective-event", "episode.started", now, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, renderEvent("frame-event", "frame.created", now, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}

	values := make([]procedure.Procedure, 0, 10)
	for i := 9; i >= 0; i-- {
		values = append(values, procedure.Procedure{Protocol: protocol.Name, Version: protocol.Version, ProcedureID: fmt.Sprintf("procedure-%02d", i), EpisodeID: current.EpisodeID, SemanticTrigger: "run release test", Preconditions: procedure.Preconditions{RequiredArguments: []string{}}, AffordanceSequence: []procedure.AffordanceStep{{Position: 0, AffordanceID: "run_test"}}, ExpectedOutcomes: []procedure.ExpectedOutcome{}, EvidenceExecutionIDs: []string{fmt.Sprintf("execution-%02d", i)}, Successes: 1, SuccessRate: 1, ProjectionVersion: procedure.ProjectorVersion})
	}
	if err := store.ReplaceProcedures(ctx, current.EpisodeID, values); err != nil {
		t.Fatal(err)
	}

	renderer := render.New(store)
	first, err := renderer.Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderer.Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if first.RenderID != second.RenderID {
		t.Fatalf("render is not deterministic: %q != %q", first.RenderID, second.RenderID)
	}

	section := sectionByKind(first, "procedures")
	if section == nil || len(section.Items) != 8 {
		t.Fatalf("unexpected procedures section: %#v", section)
	}
	for i, item := range section.Items {
		match, ok := item.(render.ProcedureMatch)
		if !ok {
			t.Fatalf("item %d has type %T", i, item)
		}
		wantID := fmt.Sprintf("procedure-%02d", i)
		if match.Procedure.ProcedureID != wantID || match.Score != 1 || match.Provenance.ProjectionVersion != procedure.ProjectorVersion {
			t.Fatalf("unexpected match %d: %#v", i, match)
		}
	}
}

func sectionByKind(packet render.Packet, kind string) *render.Section {
	for i := range packet.Sections {
		if packet.Sections[i].Kind == kind {
			return &packet.Sections[i]
		}
	}
	return nil
}

func TestRenderAsOfCutoffControlsMemoryVisibility(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	frameTime := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	observationTime := frameTime.Add(5 * time.Minute)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: frameTime, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateObjective(ctx, goal, renderEvent("objective-event", "episode.started", frameTime, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, renderEvent("frame-event", "frame.created", frameTime, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}
	// An observation lands AFTER the frame was created: the agent loop must
	// still see it (nil AsOf = latest memory), while time travel (explicit
	// cutoff at frame time) must not.
	observation := renderEvent("observation-event", "world.observation", observationTime, map[string]any{"resource": "filesystem:.", "observation_type": "directory_listing"})
	if err := store.Append(ctx, observation); err != nil {
		t.Fatal(err)
	}

	renderer := render.New(store)
	latest, err := renderer.Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if !recentContains(latest, "observation-event") {
		t.Fatalf("latest-memory render must include observations committed after frame creation: %s", latest.Provenance.AsOf)
	}
	frontier, err := time.Parse(time.RFC3339Nano, latest.Provenance.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if !frontier.Equal(observationTime) {
		t.Fatalf("latest-memory provenance.as_of %s want event frontier %s", latest.Provenance.AsOf, observationTime)
	}

	historical, err := renderer.Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000, AsOf: &frameTime})
	if err != nil {
		t.Fatal(err)
	}
	if recentContains(historical, "observation-event") {
		t.Fatal("explicit as_of cutoff must exclude later observations (time travel)")
	}
	cutoff, err := time.Parse(time.RFC3339Nano, historical.Provenance.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if !cutoff.Equal(frameTime) {
		t.Fatalf("time-travel provenance.as_of %s want explicit cutoff %s", historical.Provenance.AsOf, frameTime)
	}
}

func recentContains(packet render.Packet, eventID string) bool {
	for _, section := range packet.Sections {
		if section.Kind != "recent" {
			continue
		}
		for _, item := range section.Items {
			if compact, ok := item.(map[string]any); ok && compact["ref"] == "event:"+eventID {
				return true
			}
		}
	}
	return false
}

func TestAmbientHidesAttentionBookkeeping(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "failing test"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Ambient: true, MaxCandidates: 8}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateObjective(ctx, goal, renderEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, renderEvent("frame-event", "frame.created", base, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}
	// Simulate one step's attention exhaust: dozens of bookkeeping events plus
	// the single perception that actually matters.
	for i := 0; i < 12; i++ {
		event := renderEvent(fmt.Sprintf("suggested-%d", i), "attention.suggested", base.Add(time.Duration(i+1)*time.Second), map[string]any{"ref": "event:x"})
		if err := store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	observation := renderEvent("observation-1", "world.observation", base.Add(20*time.Second), map[string]any{"resource": "filesystem:main.go", "observation_type": "file_content", "payload": "func Sum(a, b int) int { return a - b }"})
	if err := store.Append(ctx, observation); err != nil {
		t.Fatal(err)
	}

	packet, err := render.New(store).Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	var mapTypes, recentTypes map[string]int
	recentTypes = map[string]int{}
	for _, section := range packet.Sections {
		switch section.Kind {
		case "map":
			mapTypes = map[string]int{}
			for _, item := range section.Items {
				candidate := item.(render.MapItem)
				mapTypes[candidate.Payload.(map[string]any)["type"].(string)]++
			}
		case "recent":
			for _, item := range section.Items {
				recentTypes[item.(map[string]any)["type"].(string)]++
			}
		}
	}
	if mapTypes["attention.suggested"] != 0 {
		t.Fatalf("bookkeeping events leaked into ambient map: %#v", mapTypes)
	}
	if mapTypes["world.observation"] == 0 {
		t.Fatalf("world observation missing from ambient map: %#v", mapTypes)
	}
	if recentTypes["attention.suggested"] != 0 {
		t.Fatalf("bookkeeping events must not enter the model packet: %#v", recentTypes)
	}
	if recentTypes["world.observation"] == 0 {
		t.Fatalf("world observation missing from recent section: %#v", recentTypes)
	}
}

func renderEvent(id, eventType string, now time.Time, payload map[string]any) protocol.Event {
	return protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: now, ValidTime: now, EpisodeID: "episode", BranchID: "branch", Type: eventType, Payload: payload, Provenance: map[string]any{"source": "test"}}
}

// TestPacketIsCompactAndPeripheryIsNearMiss locks the token-efficiency
// contract of render-0.4: map items carry canonical string refs and compact
// payloads (no protocol envelope, no attention feature vectors), recent events
// shed their envelope, and periphery holds near-miss candidates instead of
// re-rendering what the map already contains.
func TestPacketIsCompactAndPeripheryIsNearMiss(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "failing test"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Ambient: true, MaxCandidates: 2}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateObjective(ctx, goal, renderEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, renderEvent("frame-event", "frame.created", base, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		event := renderEvent(fmt.Sprintf("observation-%d", i), "world.observation", base.Add(time.Duration(i)*time.Second), map[string]any{"resource": fmt.Sprintf("filesystem:file-%d.go", i), "observation_type": "file_content"})
		if err := store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	packet, err := render.New(store).Render(ctx, render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	mapSection := packetSection(packet, "map")
	peripherySection := packetSection(packet, "periphery")
	if mapSection == nil || peripherySection == nil {
		t.Fatalf("map/periphery missing: %#v", packet.Sections)
	}
	if len(mapSection.Items) != 2 {
		t.Fatalf("map must respect max_candidates: %#v", mapSection.Items)
	}
	mapRefs := map[string]struct{}{}
	for _, item := range mapSection.Items {
		candidate := item.(render.MapItem)
		if candidate.Ref == "" {
			t.Fatalf("map item missing canonical ref: %#v", candidate)
		}
		payload := candidate.Payload.(map[string]any)
		if _, ok := payload["protocol"]; ok {
			t.Fatal("map item leaks protocol envelope")
		}
		if _, ok := payload["features"]; ok {
			t.Fatal("map item leaks attention features")
		}
		mapRefs[candidate.Ref] = struct{}{}
	}
	// Five visible events (objective, frame.created, three observations), two
	// selected: the three near-misses form the periphery — and none of them
	// may duplicate a map ref.
	if len(peripherySection.Items) != 3 {
		t.Fatalf("periphery must hold near-misses, got %#v", peripherySection.Items)
	}
	peripheryRefs := map[string]struct{}{}
	for _, item := range peripherySection.Items {
		nearMiss := item.(render.MapItem)
		if _, duplicate := mapRefs[nearMiss.Ref]; duplicate {
			t.Fatalf("periphery duplicates map ref %q", nearMiss.Ref)
		}
		peripheryRefs[nearMiss.Ref] = struct{}{}
		for _, forbidden := range []string{"protocol", "version", "agent_id", "branch_id", "episode_id", "provenance"} {
			if _, ok := nearMiss.Payload.(map[string]any)[forbidden]; ok {
				t.Fatalf("periphery payload leaks %q", forbidden)
			}
		}
	}
	if len(peripheryRefs) != 3 {
		t.Fatalf("periphery refs must be distinct: %#v", peripheryRefs)
	}
	recentSection := packetSection(packet, "recent")
	for _, item := range recentSection.Items {
		compact := item.(map[string]any)
		for _, forbidden := range []string{"protocol", "version", "agent_id", "branch_id", "episode_id", "provenance", "tx_time", "event_id"} {
			if _, ok := compact[forbidden]; ok {
				t.Fatalf("recent item leaks %q: %#v", forbidden, compact)
			}
		}
		if compact["ref"] == "" || compact["type"] == "" {
			t.Fatalf("recent item missing ref/type: %#v", compact)
		}
	}
}
