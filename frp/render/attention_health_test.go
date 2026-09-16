package render_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

// TestRenderAttentionHealthSurfacesFocusThrashing walks a frame chain that
// replaced its focus three times out of four recent steps: the packet must
// tell the model — as a fact, not a correction — how unstable its deliberate
// attention has been. The trailing stable step must not erase the diagnosis.
func TestRenderAttentionHealthSurfacesFocusThrashing(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, healthEvent("anchor-event", "world.observation", base, map[string]any{"resource": "test", "observation_type": "note"})); err != nil {
		t.Fatal(err)
	}
	root := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-root", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "failing test"}, WorkingSet: []frame.Ref{{Type: frame.RefEvent, ID: "anchor-event"}}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, root, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": root.FrameID})); err != nil {
		t.Fatal(err)
	}
	chain := transitionChain(t, store, root, []frame.Focus{
		{Type: frame.RefQuery, Query: "config"},
		{Type: frame.RefQuery, Query: "dependencies"},
		{Type: frame.RefQuery, Query: "logs"},
		{Type: frame.RefQuery, Query: "logs"},
	}, base, false)
	renderer := render.New(store)
	packet, err := renderer.Render(ctx, render.Request{FrameID: chain.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	health := healthItem(t, packet)
	if health["focus_changed_steps"] != 3 || health["lookback_steps"] != 4 {
		t.Fatalf("focus_changed_steps=%v lookback_steps=%v, want 3 of 4", health["focus_changed_steps"], health["lookback_steps"])
	}
	if _, collapsed := health["collapsed"]; collapsed {
		t.Fatalf("stable working set reported as collapsed: %#v", health)
	}
	hint, warned := health["hint"].(string)
	if !warned || !strings.Contains(hint, "focus changed in 3 of the last 4 steps") {
		t.Fatalf("thrashing hint missing: %#v", health)
	}
}

func TestRenderAttentionHealthReportsCollapseAndStability(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective", EpisodeID: "episode", Text: "find the failing test", SuccessConditions: []string{}}
	if err := store.CreateObjective(ctx, goal, healthEvent("objective-event", "episode.started", base, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, healthEvent("anchor-event", "world.observation", base, map[string]any{"resource": "test", "observation_type": "note"})); err != nil {
		t.Fatal(err)
	}
	anchored := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-anchored", AgentID: "agent", EpisodeID: "episode", BranchID: "branch", ObjectiveID: goal.ObjectiveID, AsOf: base, Focus: frame.Focus{Type: frame.RefQuery, Query: "tests"}, WorkingSet: []frame.Ref{{Type: frame.RefEvent, ID: "anchor-event"}}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	if err := store.CreateFrame(ctx, anchored, healthEvent("frame-event", "frame.created", base, map[string]any{"frame_id": anchored.FrameID})); err != nil {
		t.Fatal(err)
	}
	renderer := render.New(store)
	// Stable frame: no collapse, no flapping, no cap pressure — no hint.
	packet, err := renderer.Render(ctx, render.Request{FrameID: anchored.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	health := healthItem(t, packet)
	if health["focus_changed_steps"] != 0 || health["working_set_size"] != 1 {
		t.Fatalf("stable health malformed: %#v", health)
	}
	if _, warned := health["hint"]; warned {
		t.Fatalf("stable frame produced a hint: %#v", health)
	}
	// Collapse: dropping the only anchor leaves deliberate attention with
	// nothing to hold — the packet says so instead of silently re-anchoring.
	collapsed := transitionChain(t, store, anchored, []frame.Focus{{Type: frame.RefQuery, Query: "tests"}}, base, true)
	packet, err = renderer.Render(ctx, render.Request{FrameID: collapsed.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	health = healthItem(t, packet)
	if flag, isCollapsed := health["collapsed"].(bool); !isCollapsed || !flag {
		t.Fatalf("collapsed flag missing: %#v", health)
	}
	if health["working_set_size"] != 0 {
		t.Fatalf("collapsed working set size = %v, want 0", health["working_set_size"])
	}
	if _, warned := health["hint"]; !warned {
		t.Fatalf("collapse hint missing: %#v", health)
	}
}

func healthItem(t *testing.T, packet render.Packet) map[string]any {
	t.Helper()
	for i := range packet.Sections {
		if packet.Sections[i].Kind != "attention_health" {
			continue
		}
		if len(packet.Sections[i].Items) != 1 {
			t.Fatalf("attention_health must hold one item: %#v", packet.Sections[i])
		}
		health, ok := packet.Sections[i].Items[0].(map[string]any)
		if !ok {
			t.Fatalf("attention_health item type %T", packet.Sections[i].Items[0])
		}
		return health
	}
	t.Fatal("attention_health section missing")
	return nil
}

// transitionChain extends the frame through attend transitions (one per
// focus) and returns the resulting leaf. When unpinOnLast is set, the final
// transition also unpins everything the root had anchored, producing a
// collapsed working set.
func transitionChain(t *testing.T, store *memory.Store, root frame.Frame, foci []frame.Focus, base time.Time, unpinOnLast bool) frame.Frame {
	t.Helper()
	ctx := context.Background()
	current := root
	for i, focus := range foci {
		operations := []frame.Operation{{Kind: frame.OpAttend, Focus: &focus}}
		if unpinOnLast && i == len(foci)-1 {
			for _, ref := range current.WorkingSet {
				unpin := ref
				operations = append(operations, frame.Operation{Kind: frame.OpUnpin, Ref: &unpin})
			}
		}
		at := base.Add(time.Duration(i+1) * time.Minute)
		event := healthEvent(fmt.Sprintf("transition-event-%d", i), "frame.transitioned", at, map[string]any{"parent_frame_id": current.FrameID})
		result, err := store.TransitionFrame(ctx, current.FrameID, frame.Transition{Operations: operations, AsOf: at.UTC().Format(time.RFC3339Nano)}, event)
		if err != nil {
			t.Fatal(err)
		}
		current = result.Frame
	}
	return current
}

func healthEvent(id, kind string, at time.Time, payload map[string]any) protocol.Event {
	value := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: "episode", BranchID: "branch", Type: kind, Payload: payload, Provenance: map[string]any{"source": "attention-health-test"}}
	value.ApplyDefaults(at)
	return value
}
