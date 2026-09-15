package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/world"
)

type Store interface {
	execution.Store
	affordance.Store
}

type Adapter interface {
	Execute(context.Context, execution.Step) (map[string]any, error)
}

// ObservingAdapter is implemented by world adapters that surface what was
// observed so the worker can persist world.observation events.
type ObservingAdapter interface {
	Observe(context.Context, execution.Step) (world.Result, error)
}

// EffectingAdapter is implemented by world adapters that can physically
// change the external world. Steps whose capability category is write or
// execute route here so the worker can persist world.effect events (M12).
type EffectingAdapter interface {
	Effect(context.Context, execution.Step) (world.Result, error)
}

// WorldStore provides the current world state for effect-boundary checks.
type WorldStore interface {
	GetWorld(context.Context, string) (world.World, error)
}

// ObservationTransitionStore commits a terminal transition and its
// observations atomically.
type ObservationTransitionStore interface {
	TransitionExecutionWithObservations(context.Context, string, execution.Status, time.Time, *execution.ExecutionError, protocol.Event, []protocol.Event) (execution.Execution, error)
}

type IDProvider func() string

type Worker struct {
	Store         Store
	Workflows     map[string]execution.DeterministicWorkflow
	Adapter       Adapter
	NewID         IDProvider
	Now           func() time.Time
	PlannerRunner PlannerRunner
}

func (w *Worker) RunOnce(ctx context.Context, episodeID string) (int, error) {
	if w.Store == nil || w.Adapter == nil || w.NewID == nil || w.Now == nil {
		return 0, errors.New("worker dependencies are required")
	}
	active, err := w.Store.ListActiveExecutions(ctx, episodeID)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, value := range active {
		if err = w.process(ctx, value); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func (w *Worker) process(ctx context.Context, value execution.Execution) error {
	definition, err := w.Store.GetDefinition(ctx, value.AffordanceID)
	if err != nil {
		return err
	}
	request, err := w.Store.GetRequest(ctx, value.RequestID)
	if err != nil {
		return err
	}
	var plan execution.Plan
	if definition.ExecutionMode == affordance.ModeDeterministic {
		if value.Status != execution.StatusCreated {
			return nil
		}
		workflow, ok := w.Workflows[value.AffordanceID]
		if !ok {
			return fmt.Errorf("no deterministic workflow for %s", value.AffordanceID)
		}
		plan, err = workflow.Plan(request)
		if err != nil {
			return err
		}
		if err = plan.Validate(definition); err != nil {
			return err
		}
	} else if w.PlannerRunner == nil {
		return errors.New("adaptive execution requires planner runner")
	}
	var running execution.Execution
	if value.Status == execution.StatusCreated {
		now := w.Now().UTC()
		runningEvent, err := w.event(value, execution.StatusRunning, now, nil)
		if err != nil {
			return err
		}
		running, err = w.Store.TransitionExecution(ctx, value.ExecutionID, execution.StatusRunning, now, nil, runningEvent)
		if err != nil {
			return err
		}
	} else if value.Status == execution.StatusRunning {
		running = value
	} else {
		return nil
	}
	if err = execution.ValidateEffectBoundary(running); err != nil {
		return err
	}
	// M11 effect boundary: authorize the planned capabilities against the
	// current world state before any physical step runs. Denials fail the
	// execution after its intent was durably persisted.
	currentWorld, err := w.bindWorld(ctx, running, plan)
	if err != nil {
		return w.failExecution(ctx, running, err)
	}
	if definition.ExecutionMode == affordance.ModeAdaptive {
		if err = w.PlannerRunner.Run(ctx, running, definition, request); err != nil {
			return w.failExecution(ctx, running, err)
		}
		return w.complete(ctx, running, currentWorld, nil)
	}
	observations := make([]protocol.Event, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		result, stepErr := w.performStep(ctx, step)
		if stepErr != nil {
			return w.failExecution(ctx, running, stepErr)
		}
		if result.EffectType != "" {
			effectEvent, eventErr := world.EffectEvent(currentWorld, running.ExecutionID, running.AffordanceID, world.Effect{Resource: result.Resource, EffectType: result.EffectType, Payload: result.Output, Truncated: result.Truncated}, w.NewID(), w.Now().UTC(), running.EpisodeID, running.BranchID, running.WorldVersion)
			if eventErr != nil {
				return eventErr
			}
			observations = append(observations, effectEvent)
			continue
		}
		if result.ObservationType == "" {
			continue
		}
		observationEvent, eventErr := world.ObservationEvent(currentWorld, running.ExecutionID, running.AffordanceID, world.Observation{Resource: result.Resource, ObservationType: result.ObservationType, Payload: result.Output, Truncated: result.Truncated}, w.NewID(), w.Now().UTC(), running.EpisodeID, running.BranchID, running.WorldVersion)
		if eventErr != nil {
			return eventErr
		}
		observations = append(observations, observationEvent)
	}
	return w.complete(ctx, running, currentWorld, observations)
}

// performStep routes one planned step to the adapter by capability category:
// write and execute categories are physical effects, everything else is an
// observation.
func (w *Worker) performStep(ctx context.Context, step execution.Step) (world.Result, error) {
	if category, ok := world.CapabilityCategory(step.Capability); ok && (category == world.CategoryWrite || category == world.CategoryExecute) {
		if effecting, implemented := w.Adapter.(EffectingAdapter); implemented {
			return effecting.Effect(ctx, step)
		}
		return world.Result{}, &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: fmt.Sprintf("adapter cannot perform capability %q", step.Capability), Retryable: false}
	}
	if observing, implemented := w.Adapter.(ObservingAdapter); implemented {
		return observing.Observe(ctx, step)
	}
	output, err := w.Adapter.Execute(ctx, step)
	if err != nil {
		return world.Result{}, err
	}
	return world.Result{Output: output}, nil
}

// complete commits the terminal completed transition, attaching observations
// atomically when the store supports it.
func (w *Worker) complete(ctx context.Context, value execution.Execution, currentWorld world.World, observations []protocol.Event) error {
	at := w.Now().UTC()
	event, err := w.event(value, execution.StatusCompleted, at, nil)
	if err != nil {
		return err
	}
	if len(observations) > 0 {
		if store, ok := w.Store.(ObservationTransitionStore); ok {
			_, err = store.TransitionExecutionWithObservations(ctx, value.ExecutionID, execution.StatusCompleted, at, nil, event, observations)
			return err
		}
		for _, observation := range observations {
			if appendErr := w.append(observation); appendErr != nil {
				return appendErr
			}
		}
	}
	_, err = w.Store.TransitionExecution(ctx, value.ExecutionID, execution.StatusCompleted, at, nil, event)
	return err
}

// bindWorld enforces the M11 effect boundary at effect time: when an
// execution is bound to a world, every planned capability must still be
// authorized by the current world state.
func (w *Worker) bindWorld(ctx context.Context, value execution.Execution, plan execution.Plan) (world.World, error) {
	if value.WorldID == "" {
		return world.World{}, nil
	}
	worldStore, ok := w.Store.(WorldStore)
	if !ok {
		return world.World{}, &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: "execution requires a world but the store cannot resolve worlds", Retryable: false}
	}
	current, err := worldStore.GetWorld(ctx, value.WorldID)
	if err != nil {
		return world.World{}, &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: fmt.Sprintf("world %q is not available: %v", value.WorldID, err), Retryable: false}
	}
	capabilities := make(map[string]struct{}, len(plan.Steps))
	for _, step := range plan.Steps {
		capabilities[step.Capability] = struct{}{}
	}
	names := make([]string, 0, len(capabilities))
	for capability := range capabilities {
		names = append(names, capability)
	}
	if err = current.AuthorizeAll(names); err != nil {
		return world.World{}, &execution.ExecutionError{Class: execution.ErrorPermissionDenied, Message: err.Error(), Retryable: false}
	}
	if bound, ok := w.Adapter.(WorldBoundAdapter); ok {
		bound.SetWorld(current)
	}
	return current, nil
}

// WorldBoundAdapter is implemented by adapters that re-bind the current
// world snapshot before effects run.
type WorldBoundAdapter interface {
	SetWorld(world.World)
}

func (w *Worker) append(event protocol.Event) error {
	appender, ok := w.Store.(eventAppender)
	if !ok {
		return errors.New("store cannot append observation events")
	}
	return appender.Append(context.Background(), event)
}

type eventAppender interface {
	Append(context.Context, protocol.Event) error
}

func (w *Worker) failExecution(ctx context.Context, value execution.Execution, cause error) error {
	failure := &execution.ExecutionError{Class: execution.ErrorProcessFailed, Message: cause.Error(), Retryable: false}
	if normalized, ok := cause.(*execution.ExecutionError); ok {
		failure = normalized
	}
	at := w.Now().UTC()
	event, err := w.event(value, execution.StatusFailed, at, failure)
	if err != nil {
		return err
	}
	_, err = w.Store.TransitionExecution(ctx, value.ExecutionID, execution.StatusFailed, at, failure, event)
	return err
}

func (w *Worker) event(value execution.Execution, status execution.Status, at time.Time, failure *execution.ExecutionError) (protocol.Event, error) {
	kind, ok := execution.EventTypeForStatus(status)
	if !ok {
		return protocol.Event{}, errors.New("status has no canonical event")
	}
	event := protocol.Event{EventID: w.NewID(), TransactionTime: at, ValidTime: at, EpisodeID: value.EpisodeID, Type: kind, Payload: map[string]any{"execution_id": value.ExecutionID}, Provenance: map[string]any{"source": "temporality-executor"}}
	if failure != nil {
		event.Payload["error"] = failure
	}
	event.ApplyDefaults(at)
	return event, event.Validate()
}
