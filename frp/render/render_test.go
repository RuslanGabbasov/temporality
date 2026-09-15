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

	section := first.Sections[6]
	if section.Kind != "procedures" || len(section.Items) != 8 {
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

func renderEvent(id, eventType string, now time.Time, payload map[string]any) protocol.Event {
	return protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: now, ValidTime: now, EpisodeID: "episode", BranchID: "branch", Type: eventType, Payload: payload, Provenance: map[string]any{"source": "test"}}
}
