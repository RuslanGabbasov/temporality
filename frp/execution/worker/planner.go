package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/planner"
)

type PlannerRunner interface {
	Run(context.Context, execution.Execution, affordance.Definition, affordance.Request) error
}

type DurablePlannerRunner struct {
	Store   planner.Store
	Planner planner.Adapter
	Adapter Adapter
	Now     func() time.Time
}

func (r *DurablePlannerRunner) Run(ctx context.Context, value execution.Execution, definition affordance.Definition, request affordance.Request) error {
	if r.Store == nil || r.Planner == nil || r.Adapter == nil || r.Now == nil {
		return errors.New("planner runner dependencies are required")
	}
	objective, _ := request.Arguments["objective"].(string)
	if strings.TrimSpace(objective) == "" {
		objective = definition.ID
	}
	initial, err := planner.New(planner.Context{ExecutionID: value.ExecutionID, Objective: objective, Definition: definition, Environment: request.Arguments, MaxSteps: definition.Planner.MaxSteps})
	if err != nil {
		return err
	}
	now := r.Now().UTC()
	run, err := r.Store.EnsurePlannerRun(ctx, planner.Run{ExecutionID: value.ExecutionID, Context: initial.Context, State: initial, Status: planner.RunRunning, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return err
	}
	if run.Status == planner.RunCompleted {
		return nil
	}
	if run.Status == planner.RunFailed {
		return errors.New(run.Error)
	}

	state := run.State
	steps, err := r.Store.ListPlannerSteps(ctx, value.ExecutionID)
	if err != nil {
		return err
	}
	for {
		var durable *planner.DurableStep
		if len(steps) > 0 && steps[len(steps)-1].Status == planner.StepProposed && steps[len(steps)-1].Proposal.Step != nil {
			durable = &steps[len(steps)-1]
		} else {
			state.Context.PreviousResults = completedResults(steps)
			proposal, nextErr := r.Planner.Next(state.Context)
			if nextErr != nil {
				return r.fail(ctx, value.ExecutionID, state, nextErr)
			}
			next, applyErr := state.Apply(proposal)
			if applyErr != nil {
				return r.fail(ctx, value.ExecutionID, state, applyErr)
			}
			state = next
			persisted, persistErr := r.Store.RecordPlannerProposal(ctx, value.ExecutionID, proposal, state, r.Now().UTC())
			if persistErr != nil {
				return persistErr
			}
			steps = append(steps, persisted)
			if proposal.Complete {
				return r.Store.FinishPlannerRun(ctx, value.ExecutionID, planner.RunCompleted, state, "", r.Now().UTC())
			}
			durable = &steps[len(steps)-1]
		}

		output, executeErr := r.Adapter.Execute(ctx, *durable.Proposal.Step)
		result := planner.StepResult{StepID: durable.Proposal.Step.ID, Success: executeErr == nil, Summary: fmt.Sprint(output)}
		if executeErr != nil {
			result.Summary = executeErr.Error()
			result.Error = &execution.ExecutionError{Class: execution.ErrorProcessFailed, Message: executeErr.Error(), Retryable: false}
		}
		state.Context.PreviousResults = append(completedResults(steps), result)
		if err = r.Store.RecordPlannerResult(ctx, value.ExecutionID, durable.Ordinal, result, state, r.Now().UTC()); err != nil {
			return err
		}
		steps[len(steps)-1].Result = &result
		if result.Success {
			steps[len(steps)-1].Status = planner.StepCompleted
			continue
		}
		steps[len(steps)-1].Status = planner.StepFailed
		return r.fail(ctx, value.ExecutionID, state, executeErr)
	}
}

func completedResults(steps []planner.DurableStep) []planner.StepResult {
	results := make([]planner.StepResult, 0, len(steps))
	for _, step := range steps {
		if step.Result != nil {
			results = append(results, *step.Result)
		}
	}
	return results
}

func (r *DurablePlannerRunner) fail(ctx context.Context, executionID string, state planner.State, cause error) error {
	if err := r.Store.FinishPlannerRun(ctx, executionID, planner.RunFailed, state, cause.Error(), r.Now().UTC()); err != nil {
		return err
	}
	return cause
}
