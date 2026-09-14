package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/execution/worker"
	"github.com/temporality-project/temporality/frp/planner"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

type recordedEffects struct{ steps []string }

func (a *recordedEffects) Execute(_ context.Context, step execution.Step) (map[string]any, error) {
	a.steps = append(a.steps, step.ID)
	return map[string]any{"step": step.ID}, nil
}

func TestDurablePlannerPolicyBoundsAndRestart(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000007_execution.up.sql", "../../../migrations/000009_planner.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name      string
		maxSteps  int
		proposals []planner.Proposal
	}{
		{name: "max_steps", maxSteps: 1, proposals: []planner.Proposal{{Step: integrationStep("one", "filesystem.read")}, {Step: integrationStep("two", "filesystem.read")}}},
		{name: "policy_denial", maxSteps: 2, proposals: []planner.Proposal{{Step: integrationStep("denied", "process.execute")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition, request, value, requested, created := adaptiveExecutionFixture(tc.maxSteps)
			if err := store.CreateExecution(ctx, definition, request, value, requested, created); err != nil {
				t.Fatal(err)
			}
			now := created.TransactionTime
			clock := func() time.Time { now = now.Add(time.Millisecond); return now }
			effects := &recordedEffects{}
			runner := &worker.DurablePlannerRunner{Store: store, Planner: &planner.RecordedAdapter{Proposals: tc.proposals}, Adapter: effects, Now: clock}
			w := worker.Worker{Store: store, Adapter: effects, PlannerRunner: runner, NewID: newTestUUID, Now: clock}
			if _, err := w.RunOnce(ctx, value.EpisodeID); err != nil {
				t.Fatal(err)
			}
			persisted, err := store.GetExecution(ctx, value.ExecutionID)
			if err != nil || persisted.Status != execution.StatusFailed {
				t.Fatalf("violation did not fail execution: %#v %v", persisted, err)
			}
			run, err := store.GetPlannerRun(ctx, value.ExecutionID)
			if err != nil || run.Status != planner.RunFailed {
				t.Fatalf("planner failure not persisted: %#v %v", run, err)
			}
		})
	}

	definition, request, value, requested, created := adaptiveExecutionFixture(2)
	if err = store.CreateExecution(ctx, definition, request, value, requested, created); err != nil {
		store.Close()
		t.Fatal(err)
	}
	startedAt := created.TransactionTime.Add(time.Millisecond)
	started, err := store.TransitionExecution(ctx, value.ExecutionID, execution.StatusRunning, startedAt, nil, testExecutionEvent(execution.EventExecutionStarted, value, startedAt))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	state, err := planner.New(planner.Context{ExecutionID: value.ExecutionID, Objective: definition.ID, Definition: definition, Environment: request.Arguments, MaxSteps: 2})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err = store.EnsurePlannerRun(ctx, planner.Run{ExecutionID: value.ExecutionID, Context: state.Context, State: state, Status: planner.RunRunning, CreatedAt: startedAt, UpdatedAt: startedAt}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	proposal := planner.Proposal{Step: integrationStep("persisted", "filesystem.read")}
	state, err = state.Apply(proposal)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err = store.RecordPlannerProposal(ctx, value.ExecutionID, proposal, state, startedAt); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()

	reopened, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	now := startedAt
	clock := func() time.Time { now = now.Add(time.Millisecond); return now }
	effects := &recordedEffects{}
	runner := worker.DurablePlannerRunner{Store: reopened, Planner: &planner.RecordedAdapter{Proposals: []planner.Proposal{{Complete: true, Summary: "restarted"}}}, Adapter: effects, Now: clock}
	if err = runner.Run(ctx, started, definition, request); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(effects.steps) != "[persisted]" {
		t.Fatalf("restart did not resume persisted proposal: %#v", effects.steps)
	}
	run, err := reopened.GetPlannerRun(ctx, value.ExecutionID)
	if err != nil || run.Status != planner.RunCompleted {
		t.Fatalf("restarted planner did not complete: %#v %v", run, err)
	}
}

func adaptiveExecutionFixture(maxSteps int) (affordance.Definition, affordance.Request, execution.Execution, protocol.Event, protocol.Event) {
	now := time.Now().UTC()
	episodeID, requestID, executionID := newTestUUID(), newTestUUID(), newTestUUID()
	definition := affordance.Definition{ID: fmt.Sprintf("adaptive_integration_%d", maxSteps), ExecutionMode: affordance.ModeAdaptive, InputSchema: map[string]any{}, Capabilities: []string{"filesystem.read"}, Limits: affordance.Limits{TimeoutSec: 30, CPU: 1, MemoryMB: 128, DiskMB: 64}, Planner: affordance.Planner{Enabled: true, Model: "recorded", MaxSteps: maxSteps}}
	definition.ApplyDefaults()
	request := affordance.Request{RequestID: requestID, EpisodeID: episodeID, AffordanceID: definition.ID, Arguments: map[string]any{}}
	request.ApplyDefaults()
	requested := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: now, ValidTime: now, EpisodeID: episodeID, Type: affordance.EventRequested, Payload: map[string]any{"request_id": requestID}, Provenance: map[string]any{"source": "planner-integration-test"}}
	createdAt := now.Add(time.Microsecond)
	created := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: createdAt, ValidTime: createdAt, EpisodeID: episodeID, Type: execution.EventExecutionCreated, Payload: map[string]any{"execution_id": executionID}, Provenance: map[string]any{"source": "planner-integration-test"}}
	value := execution.Execution{Protocol: protocol.Name, Version: protocol.Version, ExecutionID: executionID, RequestID: requestID, EpisodeID: episodeID, AffordanceID: definition.ID, Status: execution.StatusCreated, CreatedEventID: created.EventID, IntentPersistedAt: createdAt}
	return definition, request, value, requested, created
}

func integrationStep(id, capability string) *execution.Step {
	return &execution.Step{ID: id, Capability: capability, Operation: "inspect", Input: map[string]any{}}
}
