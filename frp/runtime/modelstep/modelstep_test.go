package modelstep_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/model"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/runtime/modelstep"
	"github.com/temporality-project/temporality/frp/runtime/step"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func TestModelStepPersistsRenderAndSuggestedAttention(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	current := frame.Frame{FrameID: id(1), AgentID: id(2), EpisodeID: id(3), BranchID: id(4), ObjectiveID: id(5), AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "inspect work"}, Attention: frame.Attention{Ambient: true}}
	current.ApplyDefaults()
	goal := objective.Objective{ObjectiveID: current.ObjectiveID, EpisodeID: current.EpisodeID, Text: "inspect work"}
	goal.ApplyDefaults()
	started := event(id(6), "episode.started", now, current, map[string]any{"objective_id": goal.ObjectiveID})
	if err := store.CreateObjective(ctx, goal, started); err != nil {
		t.Fatal(err)
	}
	created := event(id(7), "frame.created", now, current, map[string]any{"frame_id": current.FrameID})
	if err := store.CreateFrame(ctx, current, created); err != nil {
		t.Fatal(err)
	}
	observed := event(id(8), "observation.recorded", now, current, map[string]any{"text": "work ready"})
	if err := store.Append(ctx, observed); err != nil {
		t.Fatal(err)
	}

	request := render.Request{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 8000}
	first, err := render.New(store).Render(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := render.New(store).Render(ctx, request)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("same parent render differs: %v", err)
	}
	emission := cognition.CognitiveEmission{Schema: cognition.EmissionSchema, EmissionID: "recorded-emission", FrameID: current.FrameID, Observation: []cognition.Observation{{Ref: "event:" + observed.EventID, Interpretation: "ready"}}, Reasoning: []cognition.Reasoning{}, Claims: []cognition.EmittedClaim{}, Attention: []cognition.AttentionOperation{}, Actions: []cognition.ActionRequest{}, FrameOps: []cognition.FrameOperation{}}
	provenance := model.Provenance{Adapter: "recorded", Model: "fixture"}
	service := modelstep.Service{Store: store, Adapter: &model.RecordedAdapter{Emissions: []cognition.CognitiveEmission{emission}}, ModelProvenance: provenance, NewID: sequence(100), Now: func() time.Time { return now.Add(time.Second) }}
	result, err := service.Run(ctx, modelstep.Input{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 8000})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.RenderPacket, first) {
		t.Fatal("orchestrator changed parent render")
	}
	suggested := 0
	for _, value := range result.Step.Events {
		if value.Type == step.EventAttentionSuggested {
			suggested++
		}
	}
	if suggested == 0 {
		t.Fatal("ambient map candidates were not committed as suggestions")
	}
	if _, err = store.GetFrame(ctx, result.Step.Frame.FrameID); err != nil {
		t.Fatal(err)
	}
	durable, err := store.GetCognitiveStep(ctx, emission.EmissionID)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(durable.RenderPacketJSON)
	if durable.RenderPacketHash != hex.EncodeToString(sum[:]) || durable.RendererVersion != render.Version || durable.AttentionVersion == "" {
		t.Fatalf("invalid durable render metadata: %#v", durable)
	}
	var gotProvenance model.Provenance
	if err = json.Unmarshal(durable.ModelProvenance, &gotProvenance); err != nil || !reflect.DeepEqual(gotProvenance, provenance) {
		t.Fatalf("invalid durable model provenance: %#v %v", gotProvenance, err)
	}
}

func id(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func sequence(n int) func() string {
	return func() string {
		n++
		return id(n)
	}
}

func event(eventID, kind string, at time.Time, current frame.Frame, payload map[string]any) protocol.Event {
	value := protocol.Event{EventID: eventID, TransactionTime: at, ValidTime: at, AgentID: current.AgentID, EpisodeID: current.EpisodeID, BranchID: current.BranchID, Type: kind, Payload: payload, Provenance: map[string]any{"source": "modelstep-test"}}
	value.ApplyDefaults(at)
	return value
}
