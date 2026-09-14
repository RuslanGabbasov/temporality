package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

func TestExecutionDurabilityLifecycleAndRollback(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000007_execution.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}

	def, request, value, requested, created := executionFixture()
	if err = store.CreateExecution(ctx, def, request, value, requested, created); err != nil {
		store.Close()
		t.Fatal(err)
	}
	persisted, err := store.GetExecution(ctx, value.ExecutionID)
	if err != nil || persisted.CreatedEventID != created.EventID || persisted.IntentPersistedAt.IsZero() {
		store.Close()
		t.Fatalf("intent was not durable before effect: %#v, %v", persisted, err)
	}
	active, err := store.ListActiveExecutions(ctx, value.EpisodeID)
	if err != nil || len(active) != 1 {
		store.Close()
		t.Fatalf("unexpected active executions: %#v, %v", active, err)
	}

	startedAt := created.TransactionTime.Add(time.Second)
	started := testExecutionEvent(execution.EventExecutionStarted, value, startedAt)
	persisted, err = store.TransitionExecution(ctx, value.ExecutionID, execution.StatusRunning, startedAt, nil, started)
	if err != nil || execution.ValidateEffectBoundary(persisted) != nil {
		store.Close()
		t.Fatalf("running transition failed: %#v, %v", persisted, err)
	}
	invalid := testExecutionEvent(execution.EventExecutionCompleted, value, startedAt.Add(time.Second))
	if _, err = store.TransitionExecution(ctx, value.ExecutionID, execution.StatusCompleted, startedAt.Add(time.Second), &execution.ExecutionError{Class: execution.ErrorInternal, Message: "not allowed"}, invalid); err == nil {
		store.Close()
		t.Fatal("invalid transition succeeded")
	}
	if _, err = store.Get(ctx, invalid.EventID); err != substrate.ErrNotFound {
		store.Close()
		t.Fatalf("invalid transition event was not rolled back: %v", err)
	}
	persisted, err = store.GetExecution(ctx, value.ExecutionID)
	if err != nil || persisted.Status != execution.StatusRunning {
		store.Close()
		t.Fatalf("invalid transition changed execution: %#v, %v", persisted, err)
	}

	finishedAt := startedAt.Add(2 * time.Second)
	completed := testExecutionEvent(execution.EventExecutionCompleted, value, finishedAt)
	if _, err = store.TransitionExecution(ctx, value.ExecutionID, execution.StatusCompleted, finishedAt, nil, completed); err != nil {
		store.Close()
		t.Fatal(err)
	}
	active, err = store.ListActiveExecutions(ctx, value.EpisodeID)
	if err != nil || len(active) != 0 {
		store.Close()
		t.Fatalf("terminal execution remained active: %#v, %v", active, err)
	}
	store.Close()

	reopened, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err = reopened.GetExecution(ctx, value.ExecutionID)
	if err != nil || persisted.Status != execution.StatusCompleted || persisted.FinishedAt == nil {
		t.Fatalf("execution did not survive restart: %#v, %v", persisted, err)
	}
	gotDef, err := reopened.GetDefinition(ctx, def.ID)
	if err != nil || gotDef.ID != def.ID {
		t.Fatalf("definition did not survive restart: %#v, %v", gotDef, err)
	}
}

func executionFixture() (affordance.Definition, affordance.Request, execution.Execution, protocol.Event, protocol.Event) {
	now := time.Now().UTC()
	episodeID, requestID, executionID := newTestUUID(), newTestUUID(), newTestUUID()
	def := affordance.Definition{ID: "test.execute", ExecutionMode: affordance.ModeDeterministic, InputSchema: map[string]any{"type": "object"}, Capabilities: []string{"process.execute"}, Limits: affordance.Limits{TimeoutSec: 30, CPU: 1, MemoryMB: 128, DiskMB: 64}}
	def.ApplyDefaults()
	request := affordance.Request{RequestID: requestID, EpisodeID: episodeID, AffordanceID: def.ID, Arguments: map[string]any{"suite": "integration"}}
	request.ApplyDefaults()
	requested := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: now, ValidTime: now, EpisodeID: episodeID, Type: affordance.EventRequested, Payload: map[string]any{"request_id": requestID}, Provenance: map[string]any{"source": "integration-test"}}
	createdAt := now.Add(time.Millisecond)
	created := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: createdAt, ValidTime: createdAt, EpisodeID: episodeID, Type: execution.EventExecutionCreated, Payload: map[string]any{"execution_id": executionID}, Provenance: map[string]any{"source": "integration-test"}}
	value := execution.Execution{Protocol: protocol.Name, Version: protocol.Version, ExecutionID: executionID, RequestID: requestID, EpisodeID: episodeID, AffordanceID: def.ID, Status: execution.StatusCreated, CreatedEventID: created.EventID, IntentPersistedAt: createdAt}
	return def, request, value, requested, created
}

func testExecutionEvent(eventType string, value execution.Execution, at time.Time) protocol.Event {
	return protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: at, ValidTime: at, EpisodeID: value.EpisodeID, Type: eventType, Payload: map[string]any{"execution_id": value.ExecutionID}, Provenance: map[string]any{"source": "integration-test"}}
}
