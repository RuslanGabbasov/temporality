package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
	"github.com/temporality-project/temporality/frp/world"
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

// TestExecutionLeaseClaimFencingAndCrashLoop covers the M16 recovery
// lifecycle in postgres: exactly one executor wins a claim, a crashed
// executor's expired lease is reclaimed, stale owners are fenced away from
// both the outcome and the observations, and a crash loop is drained.
func TestExecutionLeaseClaimFencingAndCrashLoop(t *testing.T) {
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

	// Initial claim: the first executor wins created → running.
	claimAt := created.TransactionTime.Add(time.Second)
	first, err := store.ClaimExecution(ctx, value.ExecutionID, "executor-a", claimAt, 30*time.Second, testExecutionEvent(execution.EventExecutionStarted, value, claimAt))
	if err != nil || first.Attempts != 1 || first.ExecutorID != "executor-a" {
		store.Close()
		t.Fatalf("initial claim failed: %#v, %v", first, err)
	}
	// A live lease fences every other executor.
	if _, err = store.ClaimExecution(ctx, value.ExecutionID, "executor-b", claimAt.Add(time.Second), 30*time.Second, testExecutionEvent(execution.EventExecutionStarted, value, claimAt.Add(time.Second))); !errors.Is(err, execution.ErrLeaseHeld) {
		store.Close()
		t.Fatalf("live lease must fence peers: %v", err)
	}
	// The lease expired (the executor crashed): a peer reclaims, attempt grows.
	reclaimAt := claimAt.Add(31 * time.Second)
	reclaimed, err := store.ClaimExecution(ctx, value.ExecutionID, "executor-b", reclaimAt, 30*time.Second, testExecutionEvent(execution.EventExecutionStarted, value, reclaimAt))
	if err != nil || reclaimed.Attempts != 2 || reclaimed.ExecutorID != "executor-b" {
		store.Close()
		t.Fatalf("reclaim after expiry failed: %#v, %v", reclaimed, err)
	}
	// The stale executor's terminal transition — and its observations — are
	// fenced: no outcome overwrite, no duplicate world.observation events.
	staleAt := reclaimAt.Add(time.Second)
	observation := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: staleAt, ValidTime: staleAt, EpisodeID: value.EpisodeID, Type: world.EventWorldObservation, Payload: map[string]any{"execution_id": value.ExecutionID, "resource": "filesystem:.", "observation_type": "stat", "payload": map[string]any{}}, Provenance: map[string]any{"source": "integration-test"}}
	observation.ApplyDefaults(staleAt)
	if _, err = store.TransitionExecutionOwnedWithObservations(ctx, value.ExecutionID, "executor-a", execution.StatusCompleted, staleAt, nil, testExecutionEvent(execution.EventExecutionCompleted, value, staleAt), []protocol.Event{observation}); !errors.Is(err, execution.ErrFenced) {
		store.Close()
		t.Fatalf("stale owner must be fenced: %v", err)
	}
	if _, err = store.Get(ctx, observation.EventID); err != substrate.ErrNotFound {
		store.Close()
		t.Fatalf("fenced transition leaked its observation: %v", err)
	}
	current, err := store.GetExecution(ctx, value.ExecutionID)
	if err != nil || current.Status != execution.StatusRunning {
		store.Close()
		t.Fatalf("fenced transition changed execution: %#v, %v", current, err)
	}
	// The lease owner completes; the fenced observation slot stays clean.
	finishAt := reclaimAt.Add(2 * time.Second)
	ownerObservation := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: finishAt, ValidTime: finishAt, EpisodeID: value.EpisodeID, Type: world.EventWorldObservation, Payload: map[string]any{"execution_id": value.ExecutionID, "resource": "filesystem:.", "observation_type": "stat", "payload": map[string]any{}}, Provenance: map[string]any{"source": "integration-test"}}
	ownerObservation.ApplyDefaults(finishAt)
	completed, err := store.TransitionExecutionOwnedWithObservations(ctx, value.ExecutionID, "executor-b", execution.StatusCompleted, finishAt, nil, testExecutionEvent(execution.EventExecutionCompleted, value, finishAt), []protocol.Event{ownerObservation})
	if err != nil || completed.Status != execution.StatusCompleted {
		store.Close()
		t.Fatalf("owner transition failed: %#v, %v", completed, err)
	}
	if _, err = store.Get(ctx, ownerObservation.EventID); err != nil {
		store.Close()
		t.Fatalf("owner observation missing: %v", err)
	}
	// Terminal executions cannot be reclaimed — the crash loop cannot resurrect
	// finished work.
	if _, err = store.ClaimExecution(ctx, value.ExecutionID, "executor-c", finishAt.Add(time.Second), 30*time.Second, testExecutionEvent(execution.EventExecutionStarted, value, finishAt.Add(time.Second))); err == nil {
		store.Close()
		t.Fatal("terminal execution must not be claimable")
	}
	store.Close()
}
