package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue              = "temporality-agent-kernel"
	ActivityRecordEvent    = "kernel.record_event"
	ActivityCallModel      = "kernel.call_model"
	ActivityRunTool        = "kernel.run_tool"
	ActivityKnowledgeHints = "kernel.knowledge_hints"
	ApprovalSignal         = "kernel.approval"
)

type RunInput struct {
	RunID                  string        `json:"run_id"`
	Project                string        `json:"project"`
	TaskID                 string        `json:"task_id"`
	ActorID                string        `json:"actor_id"`
	Prompt                 string        `json:"prompt"`
	Model                  string        `json:"model,omitempty"`
	MaxTurns               int           `json:"max_turns,omitempty"`
	Tools                  []llm.ToolDef `json:"tools,omitempty"`
	ApprovalTools          []string      `json:"approval_tools,omitempty"`
	Role                   string        `json:"role,omitempty"`
	ParentRunID            string        `json:"parent_run_id,omitempty"`
	ParentFrameID          string        `json:"parent_frame_id,omitempty"`
	ParentEventID          string        `json:"parent_event_id,omitempty"`
	SourceID               string        `json:"source_id,omitempty"`
	ApprovalTimeoutSeconds int           `json:"approval_timeout_seconds,omitempty"`
	WorkspacePath          string        `json:"workspace_path,omitempty"`
}

type RunResult struct {
	RunID  string `json:"run_id"`
	Answer string `json:"answer"`
	Turns  int    `json:"turns"`
	Status string `json:"status"`
}

type ModelRequest struct {
	Model    string        `json:"model"`
	Messages []llm.Message `json:"messages"`
	Tools    []llm.ToolDef `json:"tools"`
}

type ToolRequest struct {
	RunID         string         `json:"run_id"`
	OperationID   string         `json:"operation_id"`
	Name          string         `json:"name"`
	Role          string         `json:"role,omitempty"`
	WorkspacePath string         `json:"workspace_path,omitempty"`
	Arguments     map[string]any `json:"arguments"`
}

type ToolResult struct {
	Content  string                 `json:"content"`
	Evidence []observation.Evidence `json:"evidence,omitempty"`
}

type Approval struct {
	OperationID string `json:"operation_id"`
	Approved    bool   `json:"approved"`
	ActorID     string `json:"actor_id"`
	Reason      string `json:"reason,omitempty"`
}

func AgentRun(ctx workflow.Context, input RunInput) (RunResult, error) {
	result := RunResult{RunID: input.RunID, Status: "running"}
	if input.RunID == "" || input.Project == "" || input.Prompt == "" {
		return result, temporal.NewNonRetryableApplicationError("run_id, project and prompt are required", "InvalidRunInput", nil)
	}
	if input.ActorID == "" {
		input.ActorID = "agent"
	}
	if input.SourceID == "" {
		input.SourceID = "temporality-agent-kernel"
	}
	if input.MaxTurns <= 0 || input.MaxTurns > 24 {
		input.MaxTurns = 8
	}
	if input.ApprovalTimeoutSeconds <= 0 {
		input.ApprovalTimeoutSeconds = 3600
	}
	if input.ApprovalTimeoutSeconds > 86400 {
		input.ApprovalTimeoutSeconds = 86400
	}
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    4 * time.Minute,
		ScheduleToCloseTimeout: 5 * time.Minute,
		RetryPolicy:            &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3},
	})
	state := &eventState{run: input, sequence: 0, eventScope: eventScope(input.SourceID, input.Project, input.RunID), parentFrame: input.ParentFrameID, previousEventID: input.ParentEventID}
	defer func() {
		if errors.Is(ctx.Err(), workflow.ErrCanceled) {
			cleanupCtx, _ := workflow.NewDisconnectedContext(ctx)
			cleanupCtx = workflow.WithActivityOptions(cleanupCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 5}})
			_ = emit(cleanupCtx, state, "run.cancelled", nil)
		}
	}()
	if err := emit(activityCtx, state, "run.started", map[string]any{"task_id": input.TaskID, "role": input.Role, "parent_run_id": input.ParentRunID}); err != nil {
		return result, err
	}
	var priorHints []Hint
	hintsCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	if err := workflow.ExecuteActivity(hintsCtx, ActivityKnowledgeHints, HintRequest{Project: input.Project, RunID: input.RunID, TaskID: input.TaskID, ActorID: input.ActorID, Query: input.Prompt}).Get(ctx, &priorHints); err != nil {
		// Retrieval is best-effort, but the miss is still part of the trajectory.
		if eventErr := emit(activityCtx, state, "memory.read.failed", map[string]any{"error_type": "activation_unavailable"}); eventErr != nil {
			return result, eventErr
		}
		priorHints = nil
	} else {
		if err := emit(activityCtx, state, "memory.read", map[string]any{"hint_count": len(priorHints)}); err != nil {
			return result, err
		}
	}
	messages := []llm.Message{{Role: "system", Content: systemPromptForRole(input.Role)}, {Role: "user", Content: input.Prompt}}
	for _, hint := range priorHints {
		messages = append(messages, llm.Message{Role: "system", Content: "Temporality context (prior knowledge, inspect provenance): " + hint.Proposition + " [" + hint.State + "] " + hint.Caution})
		if hint.HintID != "" {
			if err := emit(activityCtx, state, "knowledge.used", map[string]any{"knowledge_id": hint.KnowledgeID, "hint_id": hint.HintID}); err != nil {
				return result, err
			}
		}
	}
	for turn := 1; turn <= input.MaxTurns; turn++ {
		result.Turns = turn
		frame := fmt.Sprintf("%s/turn/%02d", input.RunID, turn)
		if state.frame != "" {
			state.parentFrame = state.frame
		}
		state.frame = frame
		if err := emit(activityCtx, state, "turn.started", map[string]any{"turn": turn}); err != nil {
			return result, err
		}
		modelReq := ModelRequest{Model: input.Model, Messages: messages, Tools: append(KernelTools(), input.Tools...)}
		if err := emit(activityCtx, state, "model.started", map[string]any{"turn": turn, "model": input.Model}); err != nil {
			return result, err
		}
		var completion llm.Completion
		modelCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 4 * time.Minute, ScheduleToCloseTimeout: 5 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		if err := workflow.ExecuteActivity(modelCtx, ActivityCallModel, modelReq).Get(ctx, &completion); err != nil {
			if eventErr := emit(activityCtx, state, "model.failed", map[string]any{"turn": turn, "error_type": "activity_failed"}); eventErr != nil {
				return result, eventErr
			}
			if eventErr := emit(activityCtx, state, "run.failed", map[string]any{"turn": turn, "error_type": "model_call_failed"}); eventErr != nil {
				return result, eventErr
			}
			result.Status = "failed"
			return result, err
		}
		if err := emit(activityCtx, state, "model.completed", map[string]any{"turn": turn, "tool_call_count": len(completion.ToolCalls), "finish_reason": completion.Finish, "total_tokens": completion.Usage.TotalTokens}); err != nil {
			return result, err
		}
		if len(completion.ToolCalls) == 0 {
			result.Answer = completion.Content
			result.Status = "completed"
			if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": turn}); err != nil {
				return result, err
			}
			if err := emit(activityCtx, state, "run.completed", map[string]any{"turns": turn}); err != nil {
				return result, err
			}
			return result, nil
		}
		messages = append(messages, llm.Message{Role: "assistant", Content: completion.Content, ToolCalls: completion.ToolCalls})
		for _, call := range completion.ToolCalls {
			operationID := fmt.Sprintf("%s/%s", frame, call.ID)
			toolCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 4 * time.Minute, ScheduleToCloseTimeout: 5 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
			if err := emit(activityCtx, state, "tool.started", map[string]any{"operation_id": operationID, "tool": call.Name, "tool_call_id": call.ID}); err != nil {
				return result, err
			}
			isMCP := strings.HasPrefix(call.Name, "mcp__")
			if isMCP {
				if err := emit(activityCtx, state, "mcp.call.started", map[string]any{"operation_id": operationID, "tool": call.Name, "approval_required": contains(input.ApprovalTools, call.Name)}); err != nil {
					return result, err
				}
			}
			var toolResult ToolResult
			toolFailed := false
			toolBlocked := false
			if call.Name == "request_approval" || contains(input.ApprovalTools, call.Name) {
				action, reason := call.Name, ""
				if call.Name == "request_approval" {
					action, _ = call.Args["action"].(string)
					reason, _ = call.Args["reason"].(string)
				}
				if err := emit(activityCtx, state, "approval.requested", map[string]any{"operation_id": operationID, "action": action, "reason": reason}); err != nil {
					return result, err
				}
				approved, approval, timedOut, waitErr := awaitApproval(ctx, operationID, time.Duration(input.ApprovalTimeoutSeconds)*time.Second)
				if waitErr != nil {
					return result, waitErr
				}
				if timedOut {
					if err := emit(activityCtx, state, "approval.timed_out", map[string]any{"operation_id": operationID, "timeout_seconds": input.ApprovalTimeoutSeconds}); err != nil {
						return result, err
					}
					toolResult.Content = "Approval timed out; the action was not run."
					toolBlocked = true
					if isMCP {
						if err := emit(activityCtx, state, "mcp.call.blocked", map[string]any{"operation_id": operationID, "reason": "approval_timeout"}); err != nil {
							return result, err
						}
					}
				} else if approved {
					if err := emit(activityCtx, state, "approval.granted", map[string]any{"operation_id": operationID, "approver": approval.ActorID, "reason": approval.Reason}); err != nil {
						return result, err
					}
					if call.Name == "request_approval" {
						toolResult.Content = "Approval granted. Continue with the requested action, but perform it only through an available tool."
					} else if err := workflow.ExecuteActivity(toolCtx, ActivityRunTool, ToolRequest{RunID: input.RunID, OperationID: operationID, Name: call.Name, Role: input.Role, WorkspacePath: input.WorkspacePath, Arguments: call.Args}).Get(ctx, &toolResult); err != nil {
						toolFailed = true
						if eventErr := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "tool": call.Name, "error_type": "activity_failed"}); eventErr != nil {
							return result, eventErr
						}
						toolResult.Content = toolFailureMessage(call.Name, err)
					} else if eventErr := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "tool": call.Name}); eventErr != nil {
						return result, eventErr
					}
				} else {
					if err := emit(activityCtx, state, "approval.rejected", map[string]any{"operation_id": operationID, "approver": approval.ActorID, "reason": approval.Reason}); err != nil {
						return result, err
					}
					toolResult.Content = "Approval rejected: " + approval.Reason
					toolBlocked = true
					if isMCP {
						if err := emit(activityCtx, state, "mcp.call.blocked", map[string]any{"operation_id": operationID, "reason": "approval_rejected"}); err != nil {
							return result, err
						}
					}
				}
				if call.Name == "request_approval" {
					if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "tool": call.Name}); err != nil {
						return result, err
					}
				}
			} else if call.Name == "remember" {
				proposition, _ := call.Args["proposition"].(string)
				knowledgeID := fmt.Sprintf("%s/knowledge/%02d", input.RunID, state.sequence+1)
				if proposition == "" {
					toolResult.Content = "proposition is required"
				} else {
					if err := emitKnowledge(activityCtx, state, knowledgeID, proposition, call.Args); err != nil {
						return result, err
					}
					toolResult.Content = "Knowledge recorded with id " + knowledgeID
				}
				if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "tool": call.Name}); err != nil {
					return result, err
				}
			} else {
				if err := workflow.ExecuteActivity(toolCtx, ActivityRunTool, ToolRequest{RunID: input.RunID, OperationID: operationID, Name: call.Name, Role: input.Role, WorkspacePath: input.WorkspacePath, Arguments: call.Args}).Get(ctx, &toolResult); err != nil {
					toolFailed = true
					if eventErr := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "tool": call.Name, "error_type": "activity_failed"}); eventErr != nil {
						return result, eventErr
					}
					toolResult.Content = toolFailureMessage(call.Name, err)
				} else {
					if eventErr := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "tool": call.Name}); eventErr != nil {
						return result, eventErr
					}
				}
			}
			if isMCP && !toolBlocked {
				eventType := "mcp.call.completed"
				data := map[string]any{"operation_id": operationID, "tool": call.Name}
				if toolFailed {
					eventType = "mcp.call.failed"
					data["error_type"] = "activity_failed"
					data["outcome"] = "uncertain"
				}
				if err := emit(activityCtx, state, eventType, data); err != nil {
					return result, err
				}
			}
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: toolResult.Content})
			for _, evidence := range toolResult.Evidence {
				if err := emit(activityCtx, state, "evidence.observed", map[string]any{"tool_call_id": call.ID, "ref": evidence.Ref, "type": evidence.Type}); err != nil {
					return result, err
				}
			}
		}
		if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": turn, "tool_calls": len(completion.ToolCalls)}); err != nil {
			return result, err
		}
	}
	result.Status = "turn_limit"
	if err := emit(activityCtx, state, "run.completed", map[string]any{"status": "turn_limit", "turns": input.MaxTurns}); err != nil {
		return result, err
	}
	return result, nil
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func eventScope(sourceID, project, runID string) string {
	digest := sha256.Sum256([]byte(sourceID + "\x00" + project + "\x00" + runID))
	return hex.EncodeToString(digest[:16])
}

// EventScope returns a stable source/project/run namespace for event IDs and
// workflow identities used by optional integrations.
func EventScope(sourceID, project, runID string) string { return eventScope(sourceID, project, runID) }

func toolFailureMessage(name string, err error) string {
	if strings.HasPrefix(name, "mcp__") || name == "run_command" {
		return "Tool call failed; its effect may be uncertain. Do not repeat a consequential action without checking its status."
	}
	return "Tool failed: " + err.Error()
}

func systemPromptForRole(role string) string {
	base := "You are a careful project agent. Use tools when useful. For factual conclusions, call remember with a concise proposition and evidence refs. Treat retrieved memory as fallible and respect cautions."
	if role == "" {
		return base
	}
	return base + " Your assigned team role is " + role + "; stay within that responsibility and ground handoffs in observed evidence."
}

func awaitApproval(ctx workflow.Context, operationID string, timeout time.Duration) (bool, Approval, bool, error) {
	channel := workflow.GetSignalChannel(ctx, ApprovalSignal)
	var approval Approval
	matched := false
	ok, err := workflow.AwaitWithTimeout(ctx, timeout, func() bool {
		for channel.ReceiveAsync(&approval) {
			if approval.OperationID == operationID {
				matched = true
				return true
			}
		}
		return false
	})
	if err != nil {
		return false, approval, false, err
	}
	if !ok {
		return false, approval, true, nil
	}
	if !matched {
		return false, approval, false, errors.New("approval wait completed without matching operation")
	}
	return approval.Approved, approval, false, nil
}

type eventState struct {
	run             RunInput
	frame           string
	parentFrame     string
	sequence        int
	eventScope      string
	previousEventID string
}

func emit(ctx workflow.Context, state *eventState, kind string, data map[string]any) error {
	state.sequence++
	if data == nil {
		data = map[string]any{}
	}
	data["frame_id"] = state.frame
	data["parent_frame_id"] = state.parentFrame
	if state.run.ParentRunID != "" {
		data["parent_run_id"] = state.run.ParentRunID
	}
	data["sequence"] = state.sequence
	eventID := fmt.Sprintf("%s/event/%06d", state.eventScope, state.sequence)
	if state.previousEventID != "" {
		data["caused_by"] = []string{state.previousEventID}
	}
	item := observation.Event{
		Schema: observation.Schema, EventID: eventID, OccurredAt: workflow.Now(ctx).UTC(),
		Source:  observation.Source{ID: state.run.SourceID, Integration: "temporality-agent-kernel", Version: "0.1"},
		Context: observation.Context{Project: state.run.Project, Run: state.run.RunID, Task: state.run.TaskID, Actor: observation.Actor{ID: state.run.ActorID, Type: "agent"}, ParentEventID: state.previousEventID},
		Type:    kind, Data: data,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityRecordEvent, item).Get(ctx, nil); err != nil {
		return err
	}
	state.previousEventID = eventID
	return nil
}

func emitKnowledge(ctx workflow.Context, state *eventState, id, proposition string, args map[string]any) error {
	state.sequence++
	data := map[string]any{"knowledge_id": id, "proposition": proposition, "kind": "claim", "frame_id": state.frame, "parent_frame_id": state.parentFrame, "sequence": state.sequence}
	if state.run.ParentRunID != "" {
		data["parent_run_id"] = state.run.ParentRunID
	}
	eventID := fmt.Sprintf("%s/event/%06d", state.eventScope, state.sequence)
	if state.previousEventID != "" {
		data["caused_by"] = []string{state.previousEventID}
	}
	item := observation.Event{Schema: observation.Schema, EventID: eventID, OccurredAt: workflow.Now(ctx).UTC(), Source: observation.Source{ID: state.run.SourceID, Integration: "temporality-agent-kernel", Version: "0.1"}, Context: observation.Context{Project: state.run.Project, Run: state.run.RunID, Task: state.run.TaskID, Actor: observation.Actor{ID: state.run.ActorID, Type: "agent"}, ParentEventID: state.previousEventID}, Type: "knowledge.proposed", Data: data}
	if raw, ok := args["evidence"].([]any); ok {
		for _, value := range raw {
			if ref, ok := value.(string); ok && ref != "" {
				item.Evidence = append(item.Evidence, observation.Evidence{Ref: ref, Type: "artifact"})
			}
		}
	}
	if err := workflow.ExecuteActivity(ctx, ActivityRecordEvent, item).Get(ctx, nil); err != nil {
		return err
	}
	state.previousEventID = eventID
	return nil
}

func KernelTools() []llm.ToolDef {
	return []llm.ToolDef{
		{Name: "echo", Description: "Return a short text value for debugging the harness tool path", Parameters: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}},
		{Name: "remember", Description: "Record an explicit knowledge proposition with optional evidence refs", Parameters: map[string]any{"type": "object", "properties": map[string]any{"proposition": map[string]any{"type": "string"}, "evidence": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "required": []string{"proposition"}}},
		{Name: "request_approval", Description: "Pause this run and request a human decision before a consequential action", Parameters: map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}, "required": []string{"action"}}},
	}
}
