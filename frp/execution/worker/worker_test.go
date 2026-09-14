package worker_test

import (
	"context"
	"fmt"
	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/execution/worker"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate/memory"
	"testing"
	"time"
)

func TestWorkerRunsOnlyAfterDurableCreate(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	def := affordance.Definition{Protocol: "frp", Version: "0.3", ID: "inspect_environment", ExecutionMode: affordance.ModeDeterministic, InputSchema: map[string]any{}, Capabilities: []string{"filesystem.read"}, Limits: affordance.Limits{TimeoutSec: 30, CPU: 1, MemoryMB: 64, DiskMB: 64}, Planner: affordance.Planner{}, FailurePolicy: affordance.FailurePolicy{}}
	req := affordance.Request{Protocol: "frp", Version: "0.3", RequestID: "r", EpisodeID: "episode", AffordanceID: def.ID, Arguments: map[string]any{"path": "."}}
	requested := event("er", affordance.EventRequested, now, "request_id", req.RequestID)
	created := event("ec", execution.EventExecutionCreated, now, "execution_id", "x")
	value := execution.Execution{Protocol: "frp", Version: "0.3", ExecutionID: "x", RequestID: "r", EpisodeID: "episode", AffordanceID: def.ID, Status: execution.StatusCreated, CreatedEventID: "ec", IntentPersistedAt: now}
	if err := store.CreateExecution(ctx, def, req, value, requested, created); err != nil {
		t.Fatal(err)
	}
	n := 0
	w := worker.Worker{Store: store, Workflows: map[string]execution.DeterministicWorkflow{def.ID: worker.InspectEnvironmentWorkflow{}}, Adapter: worker.SafeAdapter{}, NewID: func() string { n++; return fmt.Sprintf("event-%d", n) }, Now: func() time.Time { now = now.Add(time.Second); return now }}
	processed, err := w.RunOnce(ctx, "episode")
	if err != nil {
		t.Fatal(err)
	}
	final, err := store.GetExecution(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 || final.Status != execution.StatusCompleted {
		t.Fatalf("unexpected worker result: %d %#v", processed, final)
	}
}
func event(id, kind string, at time.Time, key, value string) protocol.Event {
	return protocol.Event{Protocol: "frp", Version: "0.3", EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: "episode", Type: kind, Payload: map[string]any{key: value}, Provenance: map[string]any{}}
}
