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

const (
	// DefaultLeaseTTL is how long a claim survives without completion. It must
	// comfortably exceed the slowest planned step (per-execution leases also
	// account for the affordance's declared timeout).
	DefaultLeaseTTL = 60 * time.Second
	// DefaultMaxAttempts bounds how many times a crashing execution may be
	// reclaimed before it is failed with a timeout (crash-loop guard).
	DefaultMaxAttempts = 3
)

// errLeaseSkip marks an execution skipped because another live executor owns
// it — a normal cycle event, not a worker error.
var errLeaseSkip = errors.New("execution skipped: lease held")

// errDrained marks an execution terminally resolved by the claim itself (the
// crash-loop guard): the cycle continues, nothing is left to execute.
var errDrained = errors.New("execution drained by crash-loop guard")

type Worker struct {
	Store         Store
	Workflows     map[string]execution.DeterministicWorkflow
	Adapter       Adapter
	NewID         IDProvider
	Now           func() time.Time
	PlannerRunner PlannerRunner
	// ExecutorID enables M16 lease fencing: claims, reclaims after crashes,
	// and fenced terminal transitions. Empty keeps the legacy unfenced mode
	// (single-executor tests and stores without lease support).
	ExecutorID string
	// LeaseTTL and MaxAttempts default to DefaultLeaseTTL / DefaultMaxAttempts.
	LeaseTTL    time.Duration
	MaxAttempts int
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
			if errors.Is(err, errLeaseSkip) {
				continue
			}
			if errors.Is(err, errDrained) {
				processed++
				continue
			}
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
	if value.Status != execution.StatusCreated && value.Status != execution.StatusRunning {
		return nil
	}
	// Claim the execution before planning. In fenced mode the claim carries a
	// lease (crash recovery, M16) and skips executions another executor is
	// already running; the running→failed trail still records that the runtime
	// picked the intent up before it proved unexecutable.
	var running execution.Execution
	if w.ExecutorID != "" {
		claimStore, supported := w.Store.(execution.ClaimingStore)
		if !supported {
			return errors.New("executor_id requires a lease-capable store")
		}
		claimed, claimErr := w.claim(ctx, claimStore, value, definition)
		if claimErr != nil {
			return claimErr
		}
		running = claimed
	} else if value.Status == execution.StatusCreated {
		now := w.Now().UTC()
		runningEvent, err := w.event(value, execution.StatusRunning, now, nil)
		if err != nil {
			return err
		}
		running, err = w.Store.TransitionExecution(ctx, value.ExecutionID, execution.StatusRunning, now, nil, runningEvent)
		if err != nil {
			return err
		}
	} else {
		running = value
	}
	var plan execution.Plan
	if definition.ExecutionMode == affordance.ModeDeterministic {
		// Planning failures are permanent: the intent is already durable, so
		// failing the execution drains the queue instead of retrying forever.
		workflow, ok := w.Workflows[value.AffordanceID]
		if !ok {
			return w.failExecution(ctx, running, &execution.ExecutionError{Class: execution.ErrorUnavailable, Message: fmt.Sprintf("no deterministic workflow for affordance %q in this executor", value.AffordanceID), Retryable: false})
		}
		plan, err = workflow.Plan(request)
		if err != nil {
			return w.failExecution(ctx, running, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: "plan: " + err.Error(), Retryable: false})
		}
		if err = plan.Validate(definition); err != nil {
			return w.failExecution(ctx, running, &execution.ExecutionError{Class: execution.ErrorInvalidResult, Message: "plan validation: " + err.Error(), Retryable: false})
		}
	} else if w.PlannerRunner == nil {
		return w.failExecution(ctx, running, &execution.ExecutionError{Class: execution.ErrorUnavailable, Message: "adaptive execution requires planner runner", Retryable: false})
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
		return w.complete(ctx, running, nil)
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
	return w.complete(ctx, running, observations)
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

// claim acquires the execution lease and enforces the crash-loop guard. A
// lease held by a live peer skips the execution for this cycle; a lease held
// by the worker itself (or expired) extends/reclaims it.
func (w *Worker) claim(ctx context.Context, store execution.ClaimingStore, value execution.Execution, definition affordance.Definition) (execution.Execution, error) {
	lease := w.LeaseTTL
	if lease <= 0 {
		lease = DefaultLeaseTTL
	}
	// Never lease for less than the declared execution timeout: a slow-but-
	// alive executor must not have its in-flight work stolen mid-step.
	if timeout := time.Duration(definition.Limits.TimeoutSec)*time.Second + 5*time.Second; timeout > lease {
		lease = timeout
	}
	at := w.Now().UTC()
	startEvent, err := w.event(value, execution.StatusRunning, at, nil)
	if err != nil {
		return execution.Execution{}, err
	}
	startEvent.Payload["executor_id"] = w.ExecutorID
	claimed, err := store.ClaimExecution(ctx, value.ExecutionID, w.ExecutorID, at, lease, startEvent)
	if errors.Is(err, execution.ErrLeaseHeld) {
		return execution.Execution{}, errLeaseSkip
	}
	if err != nil {
		return execution.Execution{}, err
	}
	startEvent.Payload["attempt"] = claimed.Attempts
	maxAttempts := w.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	if claimed.Attempts > maxAttempts {
		if failErr := w.failExecution(ctx, claimed, &execution.ExecutionError{Class: execution.ErrorTimeout, Message: fmt.Sprintf("execution reclaimed %d times without completing; probable executor crash loop", claimed.Attempts), Retryable: false}); failErr != nil {
			return execution.Execution{}, failErr
		}
		return execution.Execution{}, errDrained
	}
	return claimed, nil
}

// transition commits a status change; in fenced mode only the lease owner
// may finish the execution, so a stale executor that lost its reclaim gets
// ErrFenced instead of overwriting the winner's outcome.
func (w *Worker) transition(ctx context.Context, value execution.Execution, next execution.Status, at time.Time, executionError *execution.ExecutionError, event protocol.Event) (execution.Execution, error) {
	if w.ExecutorID == "" {
		return w.Store.TransitionExecution(ctx, value.ExecutionID, next, at, executionError, event)
	}
	fencing, supported := w.Store.(execution.FencingStore)
	if !supported {
		return execution.Execution{}, errors.New("executor_id requires a fencing-capable store")
	}
	return fencing.TransitionExecutionOwned(ctx, value.ExecutionID, w.ExecutorID, next, at, executionError, event)
}

// complete commits the terminal completed transition, attaching observations
// atomically when the store supports it. Losing a fence race is success, not
// an error: a faster executor already finished this work.
func (w *Worker) complete(ctx context.Context, value execution.Execution, observations []protocol.Event) error {
	at := w.Now().UTC()
	event, err := w.event(value, execution.StatusCompleted, at, nil)
	if err != nil {
		return err
	}
	if len(observations) > 0 {
		_, err = w.transitionWithObservations(ctx, value, execution.StatusCompleted, at, nil, event, observations)
	} else {
		_, err = w.transition(ctx, value, execution.StatusCompleted, at, nil, event)
	}
	if errors.Is(err, execution.ErrFenced) {
		return nil // a faster executor already finished this work
	}
	return err
}

// transitionWithObservations commits a terminal transition together with its
// observations; fenced mode routes through the owned variant when available.
func (w *Worker) transitionWithObservations(ctx context.Context, value execution.Execution, next execution.Status, at time.Time, executionError *execution.ExecutionError, event protocol.Event, observations []protocol.Event) (execution.Execution, error) {
	if w.ExecutorID == "" {
		if store, ok := w.Store.(ObservationTransitionStore); ok {
			return store.TransitionExecutionWithObservations(ctx, value.ExecutionID, next, at, executionError, event, observations)
		}
	}
	if owned, ok := w.Store.(ownedObservationStore); w.ExecutorID != "" && ok {
		return owned.TransitionExecutionOwnedWithObservations(ctx, value.ExecutionID, w.ExecutorID, next, at, executionError, event, observations)
	}
	// Fenced store without the combined variant: append observations first,
	// then the fenced terminal transition.
	for _, observation := range observations {
		if err := w.append(observation); err != nil {
			return execution.Execution{}, err
		}
	}
	return w.transition(ctx, value, next, at, executionError, event)
}

// ownedObservationStore is the fenced flavor of the atomic observation+status
// commit.
type ownedObservationStore interface {
	TransitionExecutionOwnedWithObservations(context.Context, string, string, execution.Status, time.Time, *execution.ExecutionError, protocol.Event, []protocol.Event) (execution.Execution, error)
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
	_, err = w.transition(ctx, value, execution.StatusFailed, at, failure, event)
	if errors.Is(err, execution.ErrFenced) {
		return nil // a faster executor already finished this work
	}
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
