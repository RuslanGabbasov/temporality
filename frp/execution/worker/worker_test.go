package worker_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/execution/worker"
	"github.com/temporality-project/temporality/frp/planner"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate/memory"
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

type scriptedAdapter struct {
	executed []string
}

func (a *scriptedAdapter) Execute(_ context.Context, step execution.Step) (map[string]any, error) {
	a.executed = append(a.executed, step.ID)
	return map[string]any{"step": step.ID}, nil
}

func TestAdaptiveWorkerEnforcesPlannerBoundsAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		maxSteps  int
		proposals []planner.Proposal
	}{
		{name: "max_steps", maxSteps: 1, proposals: []planner.Proposal{{Step: plannerStep("s1", "filesystem.read")}, {Step: plannerStep("s2", "filesystem.read")}}},
		{name: "policy_denial", maxSteps: 2, proposals: []planner.Proposal{{Step: plannerStep("s1", "process.execute")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, value, _, now := adaptiveFixture(t, tc.maxSteps)
			effects := &scriptedAdapter{}
			clock := func() time.Time { now = now.Add(time.Second); return now }
			runner := &worker.DurablePlannerRunner{Store: store, Planner: &planner.RecordedAdapter{Proposals: tc.proposals}, Adapter: effects, Now: clock}
			n := 0
			w := worker.Worker{Store: store, Adapter: effects, PlannerRunner: runner, NewID: func() string { n++; return fmt.Sprintf("adaptive-event-%s-%d", tc.name, n) }, Now: clock}
			if _, err := w.RunOnce(context.Background(), value.EpisodeID); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetExecution(context.Background(), value.ExecutionID)
			if err != nil || got.Status != execution.StatusFailed {
				t.Fatalf("planner violation did not fail execution: %#v %v", got, err)
			}
			run, err := store.GetPlannerRun(context.Background(), value.ExecutionID)
			if err != nil || run.Status != planner.RunFailed {
				t.Fatalf("planner failure was not durable: %#v %v", run, err)
			}
		})
	}
}

func TestAdaptivePlannerResumesPersistedProposal(t *testing.T) {
	store, value, definition, now := adaptiveFixture(t, 2)
	ctx := context.Background()
	state, err := planner.New(planner.Context{ExecutionID: value.ExecutionID, Objective: definition.ID, Definition: definition, Environment: map[string]any{}, MaxSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EnsurePlannerRun(ctx, planner.Run{ExecutionID: value.ExecutionID, Context: state.Context, State: state, Status: planner.RunRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	state, err = state.Apply(planner.Proposal{Step: plannerStep("persisted", "filesystem.read")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RecordPlannerProposal(ctx, value.ExecutionID, planner.Proposal{Step: plannerStep("persisted", "filesystem.read")}, state, now); err != nil {
		t.Fatal(err)
	}
	started, err := value.Transition(execution.StatusRunning, now.Add(time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := &scriptedAdapter{}
	clock := func() time.Time { now = now.Add(time.Second); return now }
	runner := worker.DurablePlannerRunner{Store: store, Planner: &planner.RecordedAdapter{Proposals: []planner.Proposal{{Complete: true, Summary: "recovered"}}}, Adapter: effects, Now: clock}
	request, _ := store.GetRequest(ctx, value.RequestID)
	if err = runner.Run(ctx, started, definition, request); err != nil {
		t.Fatal(err)
	}
	if len(effects.executed) != 1 || effects.executed[0] != "persisted" {
		t.Fatalf("persisted proposal was not resumed: %#v", effects.executed)
	}
	run, _ := store.GetPlannerRun(ctx, value.ExecutionID)
	if run.Status != planner.RunCompleted {
		t.Fatalf("resumed planner did not complete: %#v", run)
	}
}

func adaptiveFixture(t *testing.T, maxSteps int) (*memory.Store, execution.Execution, affordance.Definition, time.Time) {
	t.Helper()
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	definition := affordance.Definition{Protocol: "frp", Version: "0.3", ID: "adaptive_inspect", ExecutionMode: affordance.ModeAdaptive, InputSchema: map[string]any{}, Capabilities: []string{"filesystem.read"}, Limits: affordance.Limits{TimeoutSec: 30, CPU: 1, MemoryMB: 64, DiskMB: 64}, Planner: affordance.Planner{Enabled: true, Model: "recorded", MaxSteps: maxSteps}}
	request := affordance.Request{Protocol: "frp", Version: "0.3", RequestID: "adaptive-request-" + fmt.Sprint(maxSteps), EpisodeID: "adaptive-episode-" + fmt.Sprint(maxSteps), AffordanceID: definition.ID, Arguments: map[string]any{}}
	value := execution.Execution{Protocol: "frp", Version: "0.3", ExecutionID: "adaptive-execution-" + fmt.Sprint(maxSteps), RequestID: request.RequestID, EpisodeID: request.EpisodeID, AffordanceID: definition.ID, Status: execution.StatusCreated, CreatedEventID: "adaptive-created-" + fmt.Sprint(maxSteps), IntentPersistedAt: now}
	requested := event("adaptive-requested-"+fmt.Sprint(maxSteps), affordance.EventRequested, now, "request_id", request.RequestID)
	requested.EpisodeID = request.EpisodeID
	created := event(value.CreatedEventID, execution.EventExecutionCreated, now, "execution_id", value.ExecutionID)
	created.EpisodeID = request.EpisodeID
	if err := store.CreateExecution(ctx, definition, request, value, requested, created); err != nil {
		t.Fatal(err)
	}
	return store, value, definition, now
}

func plannerStep(id, capability string) *execution.Step {
	return &execution.Step{ID: id, Capability: capability, Operation: "inspect", Input: map[string]any{}}
}

func event(id, kind string, at time.Time, key, value string) protocol.Event {
	return protocol.Event{Protocol: "frp", Version: "0.3", EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: "episode", Type: kind, Payload: map[string]any{key: value}, Provenance: map[string]any{}}
}
