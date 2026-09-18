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

// repairAdapter rejects its first emission with a RejectedError (as the
// OpenAI adapter does for delivered-but-invalid output) and succeeds on the
// repair pass, recording both usages so the test can assert the summed cost.
type repairAdapter struct {
	calls       int
	repairs     int
	alwaysFail  bool
	firstUsage  model.Usage
	repairUsage model.Usage
	emission    cognition.CognitiveEmission
}

func (a *repairAdapter) Emit(_ context.Context, packet render.Packet) (cognition.CognitiveEmission, model.Usage, error) {
	a.calls++
	if a.calls == 1 {
		return cognition.CognitiveEmission{}, a.firstUsage, &model.RejectedError{Reason: "validate cognitive emission: invalid ref \"\"", Raw: "{\"schema\":\"broken\"}"}
	}
	return cognition.CognitiveEmission{}, model.Usage{}, &model.RejectedError{Reason: "still invalid", Raw: "{}"}
}

func (a *repairAdapter) EmitRepair(_ context.Context, packet render.Packet, rejection *model.RejectedError) (cognition.CognitiveEmission, model.Usage, error) {
	a.repairs++
	if a.alwaysFail || a.repairs > 1 {
		return cognition.CognitiveEmission{}, model.Usage{}, &model.RejectedError{Reason: "still invalid", Raw: "{}"}
	}
	return a.emission, a.repairUsage, nil
}

// TestModelStepRepairsRejectedEmission locks the retry economics: a rejected
// emission is re-asked with the precise reason (bounded to two repairs)
// instead of failing the step, and the reported usage is the SUM of every
// attempt — the repair pass is not free.
func TestModelStepRepairsRejectedEmission(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	current := frame.Frame{FrameID: id(1), AgentID: id(2), EpisodeID: id(3), BranchID: id(4), ObjectiveID: id(5), AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "inspect work"}, Attention: frame.Attention{Ambient: true}}
	current.ApplyDefaults()
	goal := objective.Objective{ObjectiveID: current.ObjectiveID, EpisodeID: current.EpisodeID, Text: "inspect work"}
	goal.ApplyDefaults()
	if err := store.CreateObjective(ctx, goal, event(id(6), "episode.started", now, current, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, event(id(7), "frame.created", now, current, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}
	completion := "done"
	adapter := &repairAdapter{firstUsage: model.Usage{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100}, repairUsage: model.Usage{PromptTokens: 500, CompletionTokens: 80, TotalTokens: 580}, emission: cognition.CognitiveEmission{Schema: cognition.EmissionSchema, EmissionID: "repaired-emission", FrameID: current.FrameID, Observation: []cognition.Observation{}, Reasoning: []cognition.Reasoning{}, Claims: []cognition.EmittedClaim{}, Attention: []cognition.AttentionOperation{}, Actions: []cognition.ActionRequest{}, FrameOps: []cognition.FrameOperation{}, Completion: &completion}}
	service := modelstep.Service{Store: store, Adapter: adapter, NewID: sequence(100), Now: func() time.Time { return now.Add(time.Second) }}
	result, err := service.Run(ctx, modelstep.Input{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 8000})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.repairs != 1 {
		t.Fatalf("expected exactly one repair, got %d", adapter.repairs)
	}
	if result.Emission.EmissionID != "repaired-emission" {
		t.Fatalf("unexpected emission: %#v", result.Emission)
	}
	want := model.Usage{PromptTokens: 1500, CompletionTokens: 180, TotalTokens: 1680}
	if result.ModelUsage != want {
		t.Fatalf("usage must sum emit + repair: %#v want %#v", result.ModelUsage, want)
	}
}

// TestModelStepRepairIsBounded locks the guard: a model that cannot converge
// burns at most the two repair attempts, then the step fails with the last
// rejection — the driver-level retry loop stays the outer safety net.
func TestModelStepRepairIsBounded(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	current := frame.Frame{FrameID: id(1), AgentID: id(2), EpisodeID: id(3), BranchID: id(4), ObjectiveID: id(5), AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "inspect work"}, Attention: frame.Attention{Ambient: true}}
	current.ApplyDefaults()
	goal := objective.Objective{ObjectiveID: current.ObjectiveID, EpisodeID: current.EpisodeID, Text: "inspect work"}
	goal.ApplyDefaults()
	if err := store.CreateObjective(ctx, goal, event(id(6), "episode.started", now, current, map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, event(id(7), "frame.created", now, current, map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}
	alwaysReject := &repairAdapter{alwaysFail: true, firstUsage: model.Usage{TotalTokens: 10}, repairUsage: model.Usage{TotalTokens: 5}}
	service := modelstep.Service{Store: store, Adapter: alwaysReject, NewID: sequence(100), Now: func() time.Time { return now.Add(time.Second) }}
	_, err := service.Run(ctx, modelstep.Input{FrameID: current.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 8000})
	if err == nil {
		t.Fatal("unconverged model must fail the step")
	}
	if alwaysReject.repairs != 2 {
		t.Fatalf("repair must stop after two attempts, got %d", alwaysReject.repairs)
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
