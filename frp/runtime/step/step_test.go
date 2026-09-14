package step_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/runtime/step"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func TestMemoryStepSuccessAndReferenceRollback(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	current := testFrame(now)
	created := testEvent("00000000-0000-4000-8000-000000000006", "frame.created", now, current)
	created.Payload["frame_id"] = current.FrameID
	if err := store.CreateFrame(ctx, current, created); err != nil {
		t.Fatal(err)
	}
	seed := testEvent("00000000-0000-4000-8000-000000000007", "observation.recorded", now, current)
	if err := store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}

	def := testDefinition()
	emission := cognition.CognitiveEmission{
		EmissionID: "emission-success", FrameID: current.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "available"}},
		Claims:      []cognition.EmittedClaim{{Proposition: "work is available", Confidence: 0.9}},
		Attention:   []cognition.AttentionOperation{{Op: "attend", Target: cognition.AttentionTarget{Type: frame.RefQuery, Text: "next work"}}},
		Actions:     []cognition.ActionRequest{{Affordance: def.ID, Args: map[string]any{"task": "test"}}},
	}
	result, err := step.Run(ctx, store, step.Input{Current: current, Emission: emission, Definitions: map[string]affordance.Definition{def.ID: def}, NewID: uuidSequence(100), Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Claims) != 1 || len(result.Executions) != 1 || len(result.Events) != 5 || result.Events[1].Type != step.EventAttentionSelected || result.Events[4].Type != step.EventFrameTransitioned {
		t.Fatalf("unexpected step result: %#v", result)
	}
	if _, err = store.GetFrame(ctx, result.Frame.FrameID); err != nil {
		t.Fatal(err)
	}

	before, err := store.List(ctx, substrate.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	invalid := emission
	invalid.EmissionID = "emission-rollback"
	invalid.FrameID = result.Frame.FrameID
	invalid.Observation = []cognition.Observation{{Ref: "event:00000000-0000-4000-8000-999999999999", Interpretation: "missing"}}
	if _, err = step.Run(ctx, store, step.Input{Current: result.Frame, Emission: invalid, Definitions: map[string]affordance.Definition{def.ID: def}, NewID: uuidSequence(200), Now: func() time.Time { return now.Add(2 * time.Second) }}); err == nil {
		t.Fatal("step with missing reference succeeded")
	}
	after, listErr := store.List(ctx, substrate.EventFilter{})
	if listErr != nil || len(after) != len(before) {
		t.Fatalf("failed step was not rolled back: before=%d after=%d err=%v", len(before), len(after), listErr)
	}
}

func testFrame(now time.Time) frame.Frame {
	value := frame.Frame{FrameID: "00000000-0000-4000-8000-000000000001", AgentID: "00000000-0000-4000-8000-000000000002", EpisodeID: "00000000-0000-4000-8000-000000000003", BranchID: "00000000-0000-4000-8000-000000000004", ObjectiveID: "00000000-0000-4000-8000-000000000005", AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "work"}, Attention: frame.Attention{Deliberate: true}}
	value.ApplyDefaults()
	return value
}

func testEvent(id, kind string, now time.Time, value frame.Frame) protocol.Event {
	event := protocol.Event{EventID: id, TransactionTime: now, ValidTime: now, AgentID: value.AgentID, EpisodeID: value.EpisodeID, BranchID: value.BranchID, Type: kind, Payload: map[string]any{}, Provenance: map[string]any{"source": "test"}}
	event.ApplyDefaults(now)
	return event
}

func testDefinition() affordance.Definition {
	value := affordance.Definition{ID: "work.execute", ExecutionMode: affordance.ModeDeterministic, InputSchema: map[string]any{"type": "object"}, Capabilities: []string{"work.execute"}, Limits: affordance.Limits{TimeoutSec: 10, CPU: 1, MemoryMB: 64, DiskMB: 64}}
	value.ApplyDefaults()
	return value
}

func uuidSequence(start int) func() string {
	n := start
	return func() string {
		n++
		return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	}
}
