package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/protocol"
)

type Store interface {
	execution.Store
	affordance.Store
}
type Adapter interface {
	Execute(context.Context, execution.Step) (map[string]any, error)
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
	if definition.ExecutionMode == affordance.ModeAdaptive {
		if err = w.PlannerRunner.Run(ctx, running, definition, request); err != nil {
			return w.failExecution(ctx, running, err)
		}
	} else {
		for _, step := range plan.Steps {
			if _, err = w.Adapter.Execute(ctx, step); err != nil {
				failure := &execution.ExecutionError{Class: execution.ErrorProcessFailed, Message: err.Error(), Retryable: false}
				at := w.Now().UTC()
				event, eventErr := w.event(running, execution.StatusFailed, at, failure)
				if eventErr != nil {
					return eventErr
				}
				_, transitionErr := w.Store.TransitionExecution(ctx, running.ExecutionID, execution.StatusFailed, at, failure, event)
				return transitionErr
			}
		}
	}
	at := w.Now().UTC()
	event, err := w.event(running, execution.StatusCompleted, at, nil)
	if err != nil {
		return err
	}
	_, err = w.Store.TransitionExecution(ctx, running.ExecutionID, execution.StatusCompleted, at, nil, event)
	return err
}

func (w *Worker) failExecution(ctx context.Context, value execution.Execution, cause error) error {
	failure := &execution.ExecutionError{Class: execution.ErrorProcessFailed, Message: cause.Error(), Retryable: false}
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
