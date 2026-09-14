package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/model"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/runtime/modelstep"
	stepRuntime "github.com/temporality-project/temporality/frp/runtime/step"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
	"github.com/temporality-project/temporality/frp/timetravel"
)

func TestModelStepFullCrashRecovery(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{
		"../../../migrations/000001_event_store.up.sql",
		"../../../migrations/000002_claims.up.sql",
		"../../../migrations/000004_frames.up.sql",
		"../../../migrations/000005_objectives.up.sql",
		"../../../migrations/000006_regions.up.sql",
		"../../../migrations/000007_execution.up.sql",
		"../../../migrations/000008_step.up.sql",
		"../../../migrations/000010_time_travel.up.sql",
		"../../../migrations/000012_procedures.up.sql",
		"../../../migrations/000013_edges.up.sql",
		"../../../migrations/000014_model_step.up.sql",
	} {
		if err = store.Migrate(ctx, migration); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	parent := frame.Frame{FrameID: newTestUUID(), AgentID: newTestUUID(), EpisodeID: newTestUUID(), BranchID: newTestUUID(), ObjectiveID: newTestUUID(), AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "recover model step"}, Attention: frame.Attention{Ambient: true, Deliberate: true}}
	parent.ApplyDefaults()
	goal := objective.Objective{ObjectiveID: parent.ObjectiveID, EpisodeID: parent.EpisodeID, Text: "prove durable crash recovery"}
	goal.ApplyDefaults()
	objectiveEvent := recoveryEvent(newTestUUID(), "episode.started", now, parent, map[string]any{"objective_id": goal.ObjectiveID})
	if err = store.CreateObjective(ctx, goal, objectiveEvent); err != nil {
		store.Close()
		t.Fatal(err)
	}
	frameEvent := recoveryEvent(newTestUUID(), "frame.created", now, parent, map[string]any{"frame_id": parent.FrameID})
	if err = store.CreateFrame(ctx, parent, frameEvent); err != nil {
		store.Close()
		t.Fatal(err)
	}
	observed := recoveryEvent(newTestUUID(), "observation.recorded", now, parent, map[string]any{"text": "database state is ready"})
	if err = store.Append(ctx, observed); err != nil {
		store.Close()
		t.Fatal(err)
	}

	request := render.Request{FrameID: parent.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: 8000}
	recordedRender, err := render.New(store).Render(ctx, request)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	emission := cognition.CognitiveEmission{
		Schema: cognition.EmissionSchema, EmissionID: newTestUUID(), FrameID: parent.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + observed.EventID, Interpretation: "ready after recovery"}},
		Reasoning:   []cognition.Reasoning{},
		Claims:      []cognition.EmittedClaim{{Proposition: "the model step is durable", Confidence: 0.9}},
		Attention:   []cognition.AttentionOperation{{Op: "attend", Target: cognition.AttentionTarget{Type: frame.RefQuery, Text: "durable evidence"}}},
		Actions:     []cognition.ActionRequest{},
		FrameOps:    []cognition.FrameOperation{},
	}
	provenance := model.Provenance{Adapter: "recorded", Model: "crash-recovery-fixture"}
	service := modelstep.Service{Store: store, Adapter: &model.RecordedAdapter{Emissions: []cognition.CognitiveEmission{emission}}, ModelProvenance: provenance, NewID: newTestUUID, Now: func() time.Time { return now.Add(time.Second) }}
	result, err := service.Run(ctx, modelstep.Input{FrameID: parent.FrameID, ObjectiveID: goal.ObjectiveID, BudgetTokens: request.BudgetTokens})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.RenderPacket, recordedRender) {
		store.Close()
		t.Fatal("model step did not use the recorded parent render")
	}
	if len(result.Step.Executions) != 0 {
		store.Close()
		t.Fatalf("action-free emission created executions: %#v", result.Step.Executions)
	}

	// Closing the only pool immediately after commit simulates process death: all
	// following assertions must reconstruct state from PostgreSQL.
	store.Close()
	reopened, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	child, err := reopened.GetFrame(ctx, result.Step.Frame.FrameID)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentFrameID != parent.FrameID {
		t.Fatalf("recovered child has wrong parent: %#v", child)
	}
	replayed, err := timetravel.ReplayFrame(ctx, reopened, parent.FrameID)
	if err != nil {
		t.Fatal(err)
	}
	childEventIDs := make(map[string]struct{}, len(result.Step.Events))
	for _, event := range result.Step.Events {
		childEventIDs[event.EventID] = struct{}{}
	}
	for _, event := range replayed.Events {
		if _, leaked := childEventIDs[event.Event.EventID]; leaked {
			t.Fatalf("child event leaked into parent replay: %#v", event.Event)
		}
	}
	rerendered, err := render.New(reopened).Render(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rerendered, recordedRender) {
		t.Fatalf("parent render changed across crash recovery\nrecorded: %#v\nreopened: %#v", recordedRender, rerendered)
	}

	durable, err := reopened.GetCognitiveStep(ctx, emission.EmissionID)
	if err != nil {
		t.Fatal(err)
	}
	renderSum := sha256.Sum256(durable.RenderPacketJSON)
	if durable.RenderPacketHash != hex.EncodeToString(renderSum[:]) || durable.RenderPacketHash == "" || durable.RendererVersion != render.Version || durable.AttentionVersion == "" {
		t.Fatalf("recovered cognitive step has invalid render metadata: %#v", durable)
	}
	var gotProvenance model.Provenance
	if err = json.Unmarshal(durable.ModelProvenance, &gotProvenance); err != nil || !reflect.DeepEqual(gotProvenance, provenance) {
		t.Fatalf("recovered provenance differs: %#v, %v", gotProvenance, err)
	}
	if !reflect.DeepEqual(durable.Emission, result.Emission) {
		t.Fatalf("recovered emission differs: %#v", durable.Emission)
	}

	events, err := reopened.List(ctx, substrate.EventFilter{EpisodeID: parent.EpisodeID, BranchID: parent.BranchID})
	if err != nil {
		t.Fatal(err)
	}
	hasClaim, hasAttention := false, false
	for _, event := range events {
		if event.Type == execution.EventExecutionStarted || event.Type == execution.EventExecutionCompleted {
			t.Fatalf("executor event exists for action-free model step: %#v", event)
		}
		if event.Type == stepRuntime.EventClaimCandidate {
			hasClaim = true
		}
		if event.Type == stepRuntime.EventAttentionSelected || event.Type == stepRuntime.EventAttentionSuggested {
			hasAttention = true
		}
		if event.EventID == emission.EmissionID {
			t.Fatalf("cognitive emission was persisted as an event: %#v", event)
		}
	}
	if !hasClaim || !hasAttention {
		t.Fatalf("expected durable claim and attention events, claim=%v attention=%v", hasClaim, hasAttention)
	}
}

func recoveryEvent(id, kind string, at time.Time, current frame.Frame, payload map[string]any) protocol.Event {
	value := protocol.Event{EventID: id, TransactionTime: at, ValidTime: at, AgentID: current.AgentID, EpisodeID: current.EpisodeID, BranchID: current.BranchID, Type: kind, Payload: payload, Provenance: map[string]any{"source": "modelstep-crash-recovery-test"}}
	value.ApplyDefaults(at)
	return value
}
