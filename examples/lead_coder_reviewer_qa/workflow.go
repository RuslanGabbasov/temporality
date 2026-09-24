// Package lead_coder_reviewer_qa contains an opt-in demonstration team. It is
// deliberately separate from the generic Agent Kernel binary.
package lead_coder_reviewer_qa

import (
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const WorkflowName = "LeadCoderReviewerQA"

type Result struct {
	RunID  string  `json:"run_id"`
	Status string  `json:"status"`
	Answer string  `json:"answer"`
	Turns  int     `json:"turns"`
	Stages []Stage `json:"stages"`
}

type Stage struct {
	Role   string `json:"role"`
	RunID  string `json:"run_id"`
	Status string `json:"status"`
	Answer string `json:"answer"`
}

// Workflow runs the reference team as four durable child AgentRuns, passing
// each handoff forward and linking every child to the delegation event.
func Workflow(ctx workflow.Context, input agent.RunInput) (Result, error) {
	result := Result{RunID: input.RunID, Status: "running"}
	if input.RunID == "" || input.Project == "" || input.Prompt == "" {
		return result, temporal.NewNonRetryableApplicationError("run_id, project and prompt are required", "InvalidRunInput", nil)
	}
	if input.ActorID == "" {
		input.ActorID = "human-requester"
	}
	if input.SourceID == "" {
		input.SourceID = "temporality-agent-kernel"
	}
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, ScheduleToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3}})
	scope := agent.EventScope(input.SourceID, input.Project, input.RunID)
	state := &eventState{input: input, scope: scope}
	if err := state.emit(activityCtx, "run.started", map[string]any{"task_id": input.TaskID, "workflow": WorkflowName}); err != nil {
		return result, err
	}
	roles := []string{"lead", "coder", "reviewer", "qa"}
	previous := input.Prompt
	var frames []string
	for index, role := range roles {
		childRunID := input.RunID + "/" + role
		frameID := fmt.Sprintf("%s/delegation/%02d", input.RunID, index+1)
		state.frame = frameID
		frames = append(frames, frameID)
		if err := state.emit(activityCtx, "delegation.started", map[string]any{"child_run_id": childRunID, "role": role, "ordinal": index + 1}); err != nil {
			return result, err
		}
		childInput := input
		childInput.RunID = childRunID
		childInput.Role = role
		childInput.ParentRunID = input.RunID
		childInput.ParentFrameID = frameID
		childInput.ParentEventID = state.previousEventID
		childInput.Prompt = handoff(role, previous)
		childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: "agent-child/" + agent.EventScope(input.SourceID, input.Project, childRunID)})
		var child agent.RunResult
		if err := workflow.ExecuteChildWorkflow(childCtx, "AgentRun", childInput).Get(childCtx, &child); err != nil {
			if eventErr := state.emit(activityCtx, "delegation.failed", map[string]any{"child_run_id": childRunID, "role": role, "error_type": "child_workflow_failed"}); eventErr != nil {
				return result, eventErr
			}
			if eventErr := state.emit(activityCtx, "run.failed", map[string]any{"role": role, "error_type": "delegated_stage_failed"}); eventErr != nil {
				return result, eventErr
			}
			result.Status = "failed"
			return result, err
		}
		result.Turns += child.Turns
		result.Stages = append(result.Stages, Stage{Role: role, RunID: childRunID, Status: child.Status, Answer: child.Answer})
		if err := state.emit(activityCtx, "delegation.completed", map[string]any{"child_run_id": childRunID, "role": role, "child_status": child.Status}); err != nil {
			return result, err
		}
		previous = child.Answer
	}
	var summary strings.Builder
	for _, stage := range result.Stages {
		fmt.Fprintf(&summary, "## %s\n%s\n\n", strings.ToUpper(stage.Role), stage.Answer)
	}
	result.Answer = strings.TrimSpace(summary.String())
	result.Status = "completed"
	if err := state.emit(activityCtx, "run.completed", map[string]any{"stage_count": len(result.Stages), "turns": result.Turns}); err != nil {
		return result, err
	}
	narrative, truncated := agent.BoundedNarrative(result.Answer)
	if err := state.emit(activityCtx, "agent.summary", map[string]any{"kind": "narrative", "answer": narrative, "truncated": truncated, "stages": len(result.Stages), "derived_from": frames}); err != nil {
		return result, err
	}
	return result, nil
}

type eventState struct {
	input           agent.RunInput
	scope           string
	frame           string
	sequence        int
	previousEventID string
}

func (s *eventState) emit(ctx workflow.Context, eventType string, data map[string]any) error {
	s.sequence++
	if data == nil {
		data = map[string]any{}
	}
	data["frame_id"] = s.frame
	data["sequence"] = s.sequence
	data["parent_run_id"] = s.input.ParentRunID
	if s.previousEventID != "" {
		data["caused_by"] = []string{s.previousEventID}
	}
	eventID := fmt.Sprintf("%s/event/%06d", s.scope, s.sequence)
	event := observation.Event{
		Schema: observation.Schema, EventID: eventID, OccurredAt: workflow.Now(ctx).UTC(),
		Source:  observation.Source{ID: s.input.SourceID, Integration: "temporality-agent-kernel", Version: "0.1"},
		Context: observation.Context{Project: s.input.Project, Run: s.input.RunID, Task: s.input.TaskID, Actor: observation.Actor{ID: s.input.ActorID, Type: "agent"}, ParentEventID: s.previousEventID},
		Type:    eventType, Data: data,
	}
	if err := workflow.ExecuteActivity(ctx, agent.ActivityRecordEvent, event).Get(ctx, nil); err != nil {
		return err
	}
	s.previousEventID = eventID
	return nil
}

func handoff(role, previous string) string {
	switch role {
	case "lead":
		return "Break the request into an implementation brief, risks and acceptance criteria. Request:\n" + previous
	case "coder":
		return "Carry out the implementation brief with available tools. Report changed files and evidence. Lead brief:\n" + previous
	case "reviewer":
		return "Review the coder handoff for correctness, regressions and missing evidence. Inspect changes if tools allow. Coder handoff:\n" + previous
	case "qa":
		return "Validate the reviewed work. Run relevant checks if tools support it and distinguish executed checks from suggestions. Reviewer handoff:\n" + previous
	default:
		return previous
	}
}
