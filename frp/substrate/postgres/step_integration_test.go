package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
	stepRuntime "github.com/temporality-project/temporality/frp/runtime/step"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

func TestAtomicStepSuccessAndRollback(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000002_claims.up.sql", "../../../migrations/000004_frames.up.sql", "../../../migrations/000007_execution.up.sql", "../../../migrations/000008_step.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	current := postgresStepFrame(now)
	created := postgresStepEvent(newTestUUID(), "frame.created", now, current)
	created.Payload["frame_id"] = current.FrameID
	if err = store.CreateFrame(ctx, current, created); err != nil {
		t.Fatal(err)
	}
	seed := postgresStepEvent(newTestUUID(), "observation.recorded", now, current)
	if err = store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}
	definition := postgresStepDefinition()
	emission := cognition.CognitiveEmission{EmissionID: newTestUUID(), FrameID: current.FrameID, Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "ready"}}, Claims: []cognition.EmittedClaim{{Proposition: "ready", Confidence: 0.8}}, Actions: []cognition.ActionRequest{{Affordance: definition.ID, Args: map[string]any{"task": "integration"}}}}
	result, err := stepRuntime.Run(ctx, store, stepRuntime.Input{Current: current, Emission: emission, Definitions: map[string]affordance.Definition{definition.ID: definition}, NewID: newTestUUID, Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Claims) != 1 || len(result.Executions) != 1 || result.Events[len(result.Events)-1].Type != stepRuntime.EventFrameTransitioned {
		t.Fatalf("unexpected successful step: %#v", result)
	}
	before, err := store.List(ctx, substrate.EventFilter{EpisodeID: current.EpisodeID})
	if err != nil {
		t.Fatal(err)
	}

	invalid := cognition.CognitiveEmission{EmissionID: newTestUUID(), FrameID: result.Frame.FrameID, Observation: []cognition.Observation{{Ref: "event:" + newTestUUID(), Interpretation: "missing"}}, Claims: []cognition.EmittedClaim{{Proposition: "must roll back", Confidence: 0.5}}, Actions: []cognition.ActionRequest{{Affordance: definition.ID, Args: map[string]any{}}}}
	if _, err = stepRuntime.Run(ctx, store, stepRuntime.Input{Current: result.Frame, Emission: invalid, Definitions: map[string]affordance.Definition{definition.ID: definition}, NewID: newTestUUID, Now: func() time.Time { return now.Add(2 * time.Second) }}); err == nil {
		t.Fatal("step with missing reference succeeded")
	}
	after, listErr := store.List(ctx, substrate.EventFilter{EpisodeID: current.EpisodeID})
	if listErr != nil || len(after) != len(before) {
		t.Fatalf("failed step left events behind: before=%d after=%d err=%v", len(before), len(after), listErr)
	}
}

func postgresStepFrame(now time.Time) frame.Frame {
	value := frame.Frame{FrameID: newTestUUID(), AgentID: newTestUUID(), EpisodeID: newTestUUID(), BranchID: newTestUUID(), ObjectiveID: newTestUUID(), AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "integration"}}
	value.ApplyDefaults()
	return value
}

func postgresStepEvent(id, kind string, now time.Time, value frame.Frame) protocol.Event {
	event := protocol.Event{EventID: id, TransactionTime: now, ValidTime: now, AgentID: value.AgentID, EpisodeID: value.EpisodeID, BranchID: value.BranchID, Type: kind, Payload: map[string]any{}, Provenance: map[string]any{"source": "step-integration-test"}}
	event.ApplyDefaults(now)
	return event
}

func postgresStepDefinition() affordance.Definition {
	value := affordance.Definition{ID: "step.execute", ExecutionMode: affordance.ModeDeterministic, InputSchema: map[string]any{"type": "object"}, Capabilities: []string{"step.execute"}, Limits: affordance.Limits{TimeoutSec: 10, CPU: 1, MemoryMB: 64, DiskMB: 64}}
	value.ApplyDefaults()
	return value
}
