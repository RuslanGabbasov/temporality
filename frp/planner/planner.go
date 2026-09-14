package planner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
)

const Version = "planner-0.3.1"

type RunStatus string

type StepStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"

	StepProposed  StepStatus = "proposed"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
)

var ErrNotFound = errors.New("planner run not found")

type Run struct {
	ExecutionID string    `json:"execution_id"`
	Context     Context   `json:"context"`
	State       State     `json:"state"`
	Status      RunStatus `json:"status"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type DurableStep struct {
	ExecutionID string      `json:"execution_id"`
	Ordinal     int         `json:"ordinal"`
	Proposal    Proposal    `json:"proposal"`
	Result      *StepResult `json:"result,omitempty"`
	Status      StepStatus  `json:"status"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type Store interface {
	EnsurePlannerRun(context.Context, Run) (Run, error)
	GetPlannerRun(context.Context, string) (Run, error)
	ListPlannerSteps(context.Context, string) ([]DurableStep, error)
	RecordPlannerProposal(context.Context, string, Proposal, State, time.Time) (DurableStep, error)
	RecordPlannerResult(context.Context, string, int, StepResult, State, time.Time) error
	FinishPlannerRun(context.Context, string, RunStatus, State, string, time.Time) error
}

type Context struct {
	ExecutionID     string                `json:"execution_id"`
	Objective       string                `json:"objective"`
	Definition      affordance.Definition `json:"affordance_contract"`
	Environment     map[string]any        `json:"environment"`
	RelevantContext []any                 `json:"relevant_context"`
	PreviousResults []StepResult          `json:"previous_results"`
	MaxSteps        int                   `json:"max_steps"`
}
type Proposal struct {
	Complete bool            `json:"complete"`
	Summary  string          `json:"summary,omitempty"`
	Step     *execution.Step `json:"step,omitempty"`
}
type StepResult struct {
	StepID  string                    `json:"step_id"`
	Success bool                      `json:"success"`
	Summary string                    `json:"summary"`
	Error   *execution.ExecutionError `json:"error,omitempty"`
}
type State struct {
	Version  string           `json:"version"`
	Context  Context          `json:"context"`
	Steps    []execution.Step `json:"steps"`
	Complete bool             `json:"complete"`
	Summary  string           `json:"summary,omitempty"`
}

func New(ctx Context) (State, error) {
	if ctx.ExecutionID == "" || strings.TrimSpace(ctx.Objective) == "" {
		return State{}, errors.New("execution_id and objective are required")
	}
	if err := ctx.Definition.Validate(); err != nil {
		return State{}, err
	}
	if ctx.Definition.ExecutionMode != affordance.ModeAdaptive {
		return State{}, errors.New("planner requires adaptive affordance")
	}
	if ctx.MaxSteps <= 0 || ctx.MaxSteps > ctx.Definition.Planner.MaxSteps {
		return State{}, errors.New("max_steps must be positive and within affordance limit")
	}
	if ctx.Environment == nil {
		ctx.Environment = map[string]any{}
	}
	if ctx.RelevantContext == nil {
		ctx.RelevantContext = []any{}
	}
	if ctx.PreviousResults == nil {
		ctx.PreviousResults = []StepResult{}
	}
	return State{Version: Version, Context: ctx, Steps: []execution.Step{}}, nil
}
func (s State) Apply(proposal Proposal) (State, error) {
	if s.Complete {
		return State{}, errors.New("planner is already complete")
	}
	if proposal.Complete {
		if proposal.Step != nil || strings.TrimSpace(proposal.Summary) == "" {
			return State{}, errors.New("completion requires summary and no step")
		}
		s.Complete = true
		s.Summary = proposal.Summary
		return s, nil
	}
	if proposal.Step == nil {
		return State{}, errors.New("planner proposal requires step")
	}
	if len(s.Steps) >= s.Context.MaxSteps {
		return State{}, errors.New("planner step limit exceeded")
	}
	plan := execution.Plan{Steps: []execution.Step{*proposal.Step}}
	if err := plan.Validate(s.Context.Definition); err != nil {
		return State{}, fmt.Errorf("planner policy denied: %w", err)
	}
	for _, step := range s.Steps {
		if step.ID == proposal.Step.ID {
			return State{}, errors.New("duplicate planner step id")
		}
	}
	s.Steps = append(append([]execution.Step(nil), s.Steps...), *proposal.Step)
	return s, nil
}

type Adapter interface {
	Next(Context) (Proposal, error)
}
type RecordedAdapter struct {
	Proposals []Proposal
	index     int
}

func (a *RecordedAdapter) Next(_ Context) (Proposal, error) {
	if a.index >= len(a.Proposals) {
		return Proposal{}, errors.New("recorded planner trace exhausted")
	}
	result := a.Proposals[a.index]
	a.index++
	return result, nil
}
