package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue               = "temporality-agent-kernel"
	ActivityRecordEvent     = "kernel.record_event"
	ActivityCallModel       = "kernel.call_model"
	ActivityRunTool         = "kernel.run_tool"
	ActivityKnowledgeHints  = "kernel.knowledge_hints"
	ActivityKnowledgeLookup = "kernel.knowledge_lookup"
	ApprovalSignal          = "kernel.approval"
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
	AutoApproveTools       []string      `json:"auto_approve_tools,omitempty"`
	Role                   string        `json:"role,omitempty"`
	SystemPrompt           string        `json:"system_prompt,omitempty"`
	Skills                 []SkillRef    `json:"skills,omitempty"`
	ParentRunID            string        `json:"parent_run_id,omitempty"`
	ParentFrameID          string        `json:"parent_frame_id,omitempty"`
	ParentEventID          string        `json:"parent_event_id,omitempty"`
	SourceID               string        `json:"source_id,omitempty"`
	ApprovalTimeoutSeconds int           `json:"approval_timeout_seconds,omitempty"`
	WorkspacePath          string        `json:"workspace_path,omitempty"`
	MCPServer              string        `json:"mcp_server,omitempty"`
	NetworkAccess          bool          `json:"network_access,omitempty"`
}

// SkillRef is a skill resolved for a run: identity plus a budgeted digest for
// prompt injection. Execution→version linkage is recorded by the kernel before
// the workflow starts (docs/living-skills.md §13).
type SkillRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Name    string `json:"name"`
	Digest  string `json:"digest,omitempty"`
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
	Project       string         `json:"project,omitempty"`
	Arguments     map[string]any `json:"arguments"`
}

type ToolResult struct {
	Content  string                 `json:"content"`
	Evidence []observation.Evidence `json:"evidence,omitempty"`
	ExitCode *int                   `json:"exit_code,omitempty"`
}

type Approval struct {
	OperationID   string `json:"operation_id"`
	ArgumentsHash string `json:"arguments_hash"`
	Approved      bool   `json:"approved"`
	ActorID       string `json:"actor_id"`
	Reason        string `json:"reason,omitempty"`
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
	var frames []string
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
	systemPrompt := systemPromptForRole(input.Role)
	if input.SystemPrompt != "" {
		systemPrompt = input.SystemPrompt
	}
	if skillSection := skillsPromptSection(input.Skills); skillSection != "" {
		systemPrompt += "\n\n" + skillSection
	}
	messages := []llm.Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: input.Prompt}}
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
		frames = append(frames, frame)
		if err := emit(activityCtx, state, "turn.started", map[string]any{"turn": turn}); err != nil {
			return result, err
		}
		turnMessages := messages
		if turn == input.MaxTurns {
			// Budget awareness: without a reminder the model reliably spends the
			// last tool turn on more calls and the run ends with an empty handoff.
			turnMessages = append(slices.Clone(messages), llm.Message{Role: "system", Content: "This is the final turn with tools. Complete only the remaining essential checks; your next message must be the final answer to the request."})
		}
		modelReq := ModelRequest{Model: input.Model, Messages: turnMessages, Tools: append(KernelTools(), input.Tools...)}
		if err := emit(activityCtx, state, "model.started", map[string]any{"turn": turn, "model": input.Model}); err != nil {
			return result, err
		}
		var completion llm.Completion
		// Model endpoints fail transiently (hangs, EOFs, empty bodies). Retries
		// with backoff live inside the llm client; the activity therefore runs
		// exactly once and its timeout must cover the client's worst case
		// (3 attempts × request timeout + backoff ≈ 9m15s). Non-retryable model
		// errors (bad request, auth) fail fast on the first client attempt.
		modelCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Minute, ScheduleToCloseTimeout: 11 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		if err := workflow.ExecuteActivity(modelCtx, ActivityCallModel, modelReq).Get(ctx, &completion); err != nil {
			failureDetail := boundedFailureDetail(err)
			if eventErr := emit(activityCtx, state, "model.failed", map[string]any{"turn": turn, "error_type": "activity_failed", "error": failureDetail}); eventErr != nil {
				return result, eventErr
			}
			if eventErr := emit(activityCtx, state, "run.failed", map[string]any{"turn": turn, "error_type": "model_call_failed", "error": failureDetail}); eventErr != nil {
				return result, eventErr
			}
			result.Status = "failed"
			return result, err
		}
		completedData := map[string]any{"turn": turn, "tool_call_count": len(completion.ToolCalls), "finish_reason": completion.Finish, "total_tokens": completion.Usage.TotalTokens}
		maps.Copy(completedData, modelObservability(completion))
		if err := emit(activityCtx, state, "model.completed", completedData); err != nil {
			return result, err
		}
		// Emit the response text so the frontend can show tokens as they arrive.
		// Truncate to 4KB to keep events manageable.
		if completion.Content != "" {
			truncated := completion.Content
			if len(truncated) > 4096 {
				truncated = truncated[:4096]
			}
			if err := emit(activityCtx, state, "model.text_delta", map[string]any{"turn": turn, "text": truncated}); err != nil {
				return result, err
			}
		}
		// Emit reasoning content if the model provided it.
		if completion.Reasoning != "" {
			truncated := completion.Reasoning
			if len(truncated) > 4096 {
				truncated = truncated[:4096]
			}
			if err := emit(activityCtx, state, "model.reasoning", map[string]any{"turn": turn, "text": truncated}); err != nil {
				return result, err
			}
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
			if err := emitAgentSummary(activityCtx, state, result.Answer, frames, turn); err != nil {
				return result, err
			}
			return result, nil
		}
		messages = append(messages, llm.Message{Role: "assistant", Content: completion.Content, ToolCalls: completion.ToolCalls})
		// Degenerate model responses emit the same call many times in one
		// completion; live runs showed 10+ identical failing verifications per
		// turn. Identical calls within one response are executed exactly once —
		// side effects must not multiply — and duplicates answer with the
		// original execution result, linked by operation id.
		type executedCall struct {
			operationID string
			result      ToolResult
		}
		executed := map[string]executedCall{}
		for _, call := range completion.ToolCalls {
			operationID := fmt.Sprintf("%s/%s", frame, call.ID)
			argumentsHash := operationArgumentsHash(call.Args)
			if call.ArgsError != "" {
				// The arguments never parsed, so the call is rejected before any
				// effect could land (effect=none); neighboring calls of this
				// response survive instead of failing the whole completion.
				argumentsHash = rawArgumentsHash(call.ArgsRaw)
				if err := emit(activityCtx, state, "tool.started", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "tool_call_id": call.ID, "arguments": compactJSON(call.Args, 500)}); err != nil {
					return result, err
				}
				if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "error_type": "malformed_arguments", "effect": "none", "detail": call.ArgsError}); err != nil {
					return result, err
				}
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: "Tool call arguments were malformed JSON; the call was not executed. Re-issue it with valid JSON arguments."})
				continue
			}
			callKey := dedupSignature(call.Name, call.Args) // legacy key format: call.Name + "\x00" + argumentsHash
			if original, duplicate := executed[callKey]; duplicate {
				if err := emit(activityCtx, state, "tool.started", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "tool_call_id": call.ID, "arguments": compactJSON(call.Args, 500), "duplicate_of": original.operationID}); err != nil {
					return result, err
				}
				if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "duplicate_of": original.operationID}); err != nil {
					return result, err
				}
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: original.result.Content})
				continue
			}
			toolCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 4 * time.Minute, ScheduleToCloseTimeout: 5 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
			isMCP := strings.HasPrefix(call.Name, "mcp__")
			recentlyApproved := false // set true after request_approval succeeds
			approvalRequired := call.Name == "request_approval" || contains(input.ApprovalTools, call.Name)
			autoApproved := call.Name == "run_command" && contains(input.AutoApproveTools, call.Name)
			toolStarted := false
			startTool := func() error {
				if toolStarted {
					return nil
				}
				toolStarted = true
				return emit(activityCtx, state, "tool.started", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "tool_call_id": call.ID, "arguments": compactJSON(call.Args, 500)})
			}
			startMCP := func() error {
				if !isMCP {
					return nil
				}
				return emit(activityCtx, state, "mcp.call.started", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "server": input.MCPServer, "approval_required": approvalRequired})
			}
			var toolResult ToolResult
			toolFailed := false
			toolBlocked := false
			if approvalRequired && !recentlyApproved {
				action, reason := call.Name, ""
				if call.Name == "request_approval" {
					action, _ = call.Args["action"].(string)
					reason, _ = call.Args["reason"].(string)
				}
				approvalDescription, details := approvalOperation(operationID, call.Name, call.Args, input.Role == "reviewer" || input.Role == "qa")
				operation := approvalDescription["operation"].(map[string]any)
				argumentsHash = operation["arguments_hash"].(string)
				approvalData := map[string]any{"operation_id": operationID, "action": approvalText(action), "reason": approvalText(reason), "operation": operation, "details": details, "risk": approvalDescription["risk"], "redaction": approvalDescription["redaction"], "arguments_hash": argumentsHash}
				if err := emit(activityCtx, state, "approval.requested", approvalData); err != nil {
					return result, err
				}
				approved, approval, timedOut, waitErr := awaitApproval(ctx, operationID, time.Duration(input.ApprovalTimeoutSeconds)*time.Second)
				if waitErr != nil {
					return result, waitErr
				}
				if timedOut {
					if err := emit(activityCtx, state, "approval.timed_out", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "timeout_seconds": input.ApprovalTimeoutSeconds}); err != nil {
						return result, err
					}
					toolResult.Content = "Approval timed out; the action was not run."
					toolBlocked = true
					if err := emit(activityCtx, state, "tool.blocked", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "reason": "approval_timeout"}); err != nil {
						return result, err
					}
					if isMCP {
						if err := emit(activityCtx, state, "mcp.call.blocked", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "server": input.MCPServer, "reason": "approval_timeout"}); err != nil {
							return result, err
						}
					}
				} else if approved && (call.Name == "request_approval" || approval.ArgumentsHash == argumentsHash) {
					if err := emit(activityCtx, state, "approval.granted", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "approver": approval.ActorID, "reason": approvalText(approval.Reason)}); err != nil {
						return result, err
					}
					if call.Name == "request_approval" {
						if err := startTool(); err != nil {
							return result, err
						}
						toolResult.Content = "Approval granted. Continue with the requested action, but perform it only through an available tool."
						recentlyApproved = true
					} else if err := startTool(); err != nil {
						return result, err
					} else if err := startMCP(); err != nil {
						return result, err
					} else if err := workflow.ExecuteActivity(toolCtx, ActivityRunTool, ToolRequest{RunID: input.RunID, OperationID: operationID, Name: call.Name, Role: input.Role, WorkspacePath: input.WorkspacePath, Project: input.Project, Arguments: call.Args}).Get(ctx, &toolResult); err != nil {
						toolFailed = true
						if eventErr := emit(activityCtx, state, "tool.failed", toolFailureData(operationID, argumentsHash, call.Name, err)); eventErr != nil {
							return result, eventErr
						}
						toolResult.Content = toolFailureMessage(call.Name, err)
					} else if eventErr := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "output": compactJSON(toolResult.Content, 1000)}); eventErr != nil {
						return result, eventErr
					}
				} else {
					rejectionReason := approvalText(approval.Reason)
					if approved {
						rejectionReason = "approved operation arguments did not match the pending operation"
					}
					if err := emit(activityCtx, state, "approval.rejected", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "approver": approval.ActorID, "reason": rejectionReason}); err != nil {
						return result, err
					}
					toolResult.Content = "Approval rejected: " + rejectionReason
					toolBlocked = true
					blockReason := "approval_rejected"
					if approved {
						blockReason = "approval_arguments_mismatch"
					}
					if err := emit(activityCtx, state, "tool.blocked", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "reason": blockReason}); err != nil {
						return result, err
					}
					if isMCP {
						if err := emit(activityCtx, state, "mcp.call.blocked", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "server": input.MCPServer, "reason": "approval_rejected"}); err != nil {
							return result, err
						}
					}
				}
				if call.Name == "request_approval" {
					if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name}); err != nil {
						return result, err
					}
				}
			} else if call.Name == "remember" {
				if err := startTool(); err != nil {
					return result, err
				}
				proposition, _ := call.Args["proposition"].(string)
				knowledgeID := fmt.Sprintf("%s/knowledge/%02d", input.RunID, state.sequence+1)
				if proposition == "" {
					toolResult.Content = "proposition is required"
				} else {
					if err := emitKnowledge(activityCtx, state, knowledgeID, proposition, operationID, call.Args); err != nil {
						return result, err
					}
					toolResult.Content = "Knowledge recorded with id " + knowledgeID
				}
				if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name}); err != nil {
					return result, err
				}
			} else {
				if autoApproved {
					description, _ := approvalOperation(operationID, call.Name, call.Args, input.Role == "reviewer" || input.Role == "qa")
					operation := description["operation"].(map[string]any)
					if err := emit(activityCtx, state, "approval.auto_granted", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "operation": operation, "risk": description["risk"], "redaction": description["redaction"], "policy_id": "sandbox.workspace.v1", "approver": "kernel-policy", "workspace_path": input.WorkspacePath}); err != nil {
						return result, err
					}
				}
				if err := startTool(); err != nil {
					return result, err
				}
				if err := startMCP(); err != nil {
					return result, err
				}
				if err := workflow.ExecuteActivity(toolCtx, ActivityRunTool, ToolRequest{RunID: input.RunID, OperationID: operationID, Name: call.Name, Role: input.Role, WorkspacePath: input.WorkspacePath, Project: input.Project, Arguments: call.Args}).Get(ctx, &toolResult); err != nil {
					toolFailed = true
					if eventErr := emit(activityCtx, state, "tool.failed", toolFailureData(operationID, argumentsHash, call.Name, err)); eventErr != nil {
						return result, eventErr
					}
					toolResult.Content = toolFailureMessage(call.Name, err)
				} else {
					completed := map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name}
					// Exit code makes tool completion a first-class trajectory fact:
					// a command exiting non-zero is a failed attempt even though the
					// tool activity itself succeeded.
					if toolResult.ExitCode != nil {
						completed["exit_code"] = *toolResult.ExitCode
					}
					// Truncated output for trace visibility.
					if toolResult.Content != "" {
						completed["output"] = compactJSON(toolResult.Content, 1000)
					}
					if eventErr := emit(activityCtx, state, "tool.completed", completed); eventErr != nil {
						return result, eventErr
					}
					if proposal := executionObservationProposal(input, call, toolResult); proposal != nil {
						if err := emitExecutionObservation(activityCtx, state, proposal, operationID, argumentsHash); err != nil {
							return result, err
						}
					}
				}
			}
			if isMCP && !toolBlocked {
				eventType := "mcp.call.completed"
				data := map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "server": input.MCPServer}
				if toolFailed {
					eventType = "mcp.call.failed"
					data["error_type"] = "activity_failed"
					data["outcome"] = "uncertain"
				} else {
					// Result is content-addressed, never stored raw: the ref plus byte
					// count let operators correlate the exact MCP payload without
					// copying tool output into the event stream.
					data["result_ref"] = resultContentRef(toolResult.Content)
					data["result_bytes"] = len(toolResult.Content)
				}
				if err := emit(activityCtx, state, eventType, data); err != nil {
					return result, err
				}
			}
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: toolResult.Content})
			for _, evidence := range toolResult.Evidence {
				if err := emit(activityCtx, state, "evidence.observed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool_call_id": call.ID, "ref": evidence.Ref, "type": evidence.Type}); err != nil {
					return result, err
				}
			}
			executed[callKey] = executedCall{operationID: operationID, result: toolResult}
		}
		if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": turn, "tool_calls": len(completion.ToolCalls)}); err != nil {
			return result, err
		}
	}
	result.Status = "turn_limit"
	// Deterministic finale: the model spent every turn on tool calls. One more
	// model call without tools must turn the accumulated conversation into a
	// final answer, so a turn-limited run still produces a handoff instead of
	// silence. The status stays honestly "turn_limit"; events record that the
	// answer came from a forced finale.
	finaleTurn := input.MaxTurns + 1
	result.Turns = finaleTurn
	if state.frame != "" {
		state.parentFrame = state.frame
	}
	state.frame = fmt.Sprintf("%s/turn/%02d", input.RunID, finaleTurn)
	frames = append(frames, state.frame)
	if err := emit(activityCtx, state, "turn.started", map[string]any{"turn": finaleTurn, "forced_finale": true}); err != nil {
		return result, err
	}
	finaleMessages := append(slices.Clone(messages), llm.Message{Role: "system", Content: "The turn budget is exhausted and no tools are available. Produce the final answer to the request now, based strictly on the conversation and tool results so far."})
	if err := emit(activityCtx, state, "model.started", map[string]any{"turn": finaleTurn, "forced_finale": true, "model": input.Model}); err != nil {
		return result, err
	}
	var finale llm.Completion
	modelCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Minute, ScheduleToCloseTimeout: 11 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	if err := workflow.ExecuteActivity(modelCtx, ActivityCallModel, ModelRequest{Model: input.Model, Messages: finaleMessages}).Get(ctx, &finale); err != nil {
		failureDetail := boundedFailureDetail(err)
		if eventErr := emit(activityCtx, state, "model.failed", map[string]any{"turn": finaleTurn, "forced_finale": true, "error_type": "activity_failed", "error": failureDetail}); eventErr != nil {
			return result, eventErr
		}
		if eventErr := emit(activityCtx, state, "run.failed", map[string]any{"turn": finaleTurn, "error_type": "finale_model_call_failed", "error": failureDetail}); eventErr != nil {
			return result, eventErr
		}
		result.Status = "failed"
		return result, err
	}
	finaleData := map[string]any{"turn": finaleTurn, "forced_finale": true, "tool_call_count": len(finale.ToolCalls), "finish_reason": finale.Finish, "total_tokens": finale.Usage.TotalTokens}
	maps.Copy(finaleData, modelObservability(finale))
	if err := emit(activityCtx, state, "model.completed", finaleData); err != nil {
		return result, err
	}
	if finale.Content != "" {
		result.Answer = finale.Content
	}
	if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": finaleTurn, "forced_finale": true, "tool_calls": len(finale.ToolCalls)}); err != nil {
		return result, err
	}
	runData := map[string]any{"status": "turn_limit", "turns": finaleTurn, "forced_finale": true}
	if result.Answer != "" {
		runData["final_answer"] = true
	}
	if err := emit(activityCtx, state, "run.completed", runData); err != nil {
		return result, err
	}
	if result.Answer != "" {
		if err := emitAgentSummary(activityCtx, state, result.Answer, frames, finaleTurn); err != nil {
			return result, err
		}
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
	// Argument-validation failures are rejected before execution: no effect,
	// deterministic, and fixable — tell the model exactly what to correct so a
	// schema slip does not burn the run budget as repeated "uncertain" failures.
	var application *temporal.ApplicationError
	if errors.As(err, &application) && application.Type() == "InvalidToolArguments" {
		return "Tool call rejected before execution, no effect: " + application.Message() + ". Re-issue the call with corrected arguments: run_command expects command as an array of strings, e.g. [\"go\",\"run\",\".\",\"check\"]."
	}
	if strings.HasPrefix(name, "mcp__") || name == "run_command" {
		return "Tool call failed; its effect may be uncertain. Do not repeat a consequential action without checking its status."
	}
	return "Tool failed: " + err.Error()
}

const (
	narrativeMaxBytes  = 4096
	failureDetailBytes = 512
)

// BoundedNarrative produces a bounded display representation of an
// agent-authored narrative: credentials are redacted, whitespace is
// collapsed and the text is truncated to narrativeMaxBytes. The second
// return value reports whether truncation happened.
func BoundedNarrative(value string) (string, bool) {
	clean := redactProse(value)
	if len(clean) > narrativeMaxBytes {
		return clean[:narrativeMaxBytes] + "…", true
	}
	return clean, false
}

// boundedFailureDetail reduces an activity error to a bounded, redacted
// description suitable for inclusion in failure events.
func boundedFailureDetail(err error) string {
	detail := redactProse(err.Error())
	if len(detail) > failureDetailBytes {
		return detail[:failureDetailBytes] + "…"
	}
	return detail
}

// modelObservability projects client-side model-call observability onto the
// model.completed event: provider identity, wall-clock latency, token usage
// split, truncation flag, attempt count and content-addressed references for
// the exact request/response payloads. References are hashes only — payload
// contents never enter the event stream.
func modelObservability(completion llm.Completion) map[string]any {
	return map[string]any{
		"provider":          completion.Provider,
		"latency_ms":        completion.LatencyMs,
		"prompt_tokens":     completion.Usage.PromptTokens,
		"completion_tokens": completion.Usage.CompletionTokens,
		"input_ref":         completion.RequestRef,
		"output_ref":        completion.ResponseRef,
		"truncated":         completion.Truncated(),
		"attempts":          completion.Attempts,
	}
}

// resultContentRef derives the sha256 content reference for an MCP tool
// result so events stay correlatable without embedding tool output.
func resultContentRef(content string) string {
	digest := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// toolFailureData builds the tool.failed payload. The effect field is the
// reconciliation contract: "none" means the call was rejected before any
// execution (fixable, safe to re-issue), "uncertain" means the failure
// happened at or after the execution boundary, where the operator — not the
// model — must decide whether the side effect landed.

// compactJSON serializes v as JSON, truncating to maxLen characters.
// Returns empty string if marshaling fails.
func compactJSON(v any, maxLen int) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if len(b) > maxLen {
		return string(b[:maxLen]) + "…"
	}
	return string(b)
}

// readOnlyTools are tools that never produce side effects — they only
// observe state. If they fail, there's nothing to reconcile.
var readOnlyTools = map[string]bool{
	"read_file": true, "mcp__read_file": true,
	"list_directory": true, "mcp__list_directory": true,
	"search": true, "mcp__search": true,
	"glob": true, "mcp__glob": true,
	"grep": true, "mcp__grep": true,
}

func toolFailureData(operationID, argumentsHash, tool string, err error) map[string]any {
	data := map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": tool, "error_type": "activity_failed"}
	if readOnlyTools[tool] {
		// Read-only tools have no side effect regardless of where they fail.
		data["effect"] = "none"
	} else {
		var application *temporal.ApplicationError
		if errors.As(err, &application) && application.NonRetryable() {
			data["effect"] = "none"
		} else {
			data["effect"] = "uncertain"
		}
	}
	return data
}

// emitAgentSummary records the agent-authored final answer as derived data:
// it never replaces the execution record and always carries the frames it
// is derived from.
func emitAgentSummary(ctx workflow.Context, state *eventState, answer string, frames []string, turns int) error {
	summary, truncated := BoundedNarrative(answer)
	return emit(ctx, state, "agent.summary", map[string]any{"kind": "narrative", "answer": summary, "truncated": truncated, "turns": turns, "derived_from": frames})
}

func systemPromptForRole(role string) string {
	base := "You are a careful project agent. Use tools when useful and converge to a final answer before the turn budget runs out. Every command sandbox has a writable /scratch directory (HOME and TMPDIR point there); use it for build and package caches, and never write caches into the workspace, which may be read-only for your role. Successful verification and build command outcomes are recorded as knowledge automatically; do not restate them with remember. Call remember only when you are highly confident in a durable conclusion that goes beyond the recorded execution results, and include evidence refs. Treat retrieved memory as fallible and respect cautions."
	if role == "" {
		return base
	}
	return base + " Your assigned team role is " + role + "; stay within that responsibility and ground handoffs in observed evidence."
}

// skillsPromptSection renders the agent's skills as a compact canonical
// section of the system prompt. Skills describe HOW the work should be done;
// per-run experience arrives separately as knowledge hints, never here
// (docs/living-skills.md §2, §15).
func skillsPromptSection(refs []SkillRef) string {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Skills\nCanonical capabilities assigned to you. When a task matches a skill, follow its procedure; use skill_inspect for the full text. Skill knowledge is contextual: record discoveries with remember (include skill_id and capability).")
	for _, ref := range refs {
		b.WriteString("\n\n")
		b.WriteString(ref.Digest)
	}
	return b.String()
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

func emitKnowledge(ctx workflow.Context, state *eventState, id, proposition, operationID string, args map[string]any) error {
	data := map[string]any{"knowledge_id": id, "proposition": proposition, "kind": "claim", "operation_id": operationID, "arguments_hash": operationArgumentsHash(args)}
	// Skill linkage: knowledge born from applying a skill points at the skill
	// (and optionally the capability) so memory stays navigable per skill
	// without ever modifying the skill itself (docs/living-skills.md §14-16).
	if skillID, _ := args["skill_id"].(string); skillID != "" {
		data["skill_id"] = skillID
	}
	if capability, _ := args["capability"].(string); capability != "" {
		data["capability"] = capability
	}
	var evidence []observation.Evidence
	if raw, ok := args["evidence"].([]any); ok {
		for _, value := range raw {
			if ref, ok := value.(string); ok && ref != "" {
				evidence = append(evidence, observation.Evidence{Ref: ref, Type: "artifact"})
			}
		}
	}
	return emitKnowledgeEvent(ctx, state, "knowledge.proposed", data, evidence)
}

// emitExecutionObservation records a heuristic knowledge event for a
// successful verification or build command. The observation identity is
// project-scoped and stable, so the authoritative projection decides the
// semantics: an unknown command outcome is proposed once, a repeated success
// of a proposed observation confirms it, and later successes record reuse.
// Terminal states (corrected, superseded, invalidated) emit nothing: the
// runtime tool.completed event already carries the fact.
func emitExecutionObservation(ctx workflow.Context, state *eventState, proposal *executionObservation, operationID, argumentsHash string) error {
	lookupCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2}})
	var lookup KnowledgeLookupResult
	if err := workflow.ExecuteActivity(lookupCtx, ActivityKnowledgeLookup, KnowledgeLookupQuery{Project: state.run.Project, KnowledgeID: proposal.KnowledgeID}).Get(ctx, &lookup); err != nil {
		// Without an authoritative lookup the kernel cannot know whether this
		// observation already exists; proposing blindly could duplicate a node.
		// The execution fact remains recorded as a runtime event.
		return nil
	}
	evidence := []observation.Evidence{{Ref: operationID, Type: "execution"}}
	switch {
	case !lookup.Exists:
		data := map[string]any{"knowledge_id": proposal.KnowledgeID, "proposition": proposal.Proposition, "kind": "observation", "policy_id": proposal.Policy, "command": proposal.Command, "command_class": proposal.Class, "exit_code": 0, "operation_id": operationID, "arguments_hash": argumentsHash}
		return emitKnowledgeEvent(ctx, state, "knowledge.proposed", data, evidence)
	case lookup.State == "proposed" || lookup.State == "challenged":
		data := map[string]any{"knowledge_id": proposal.KnowledgeID, "rule": ReverificationRule, "operation_id": operationID, "arguments_hash": argumentsHash, "command": proposal.Command}
		return emitKnowledgeEvent(ctx, state, "knowledge.confirmed", data, evidence)
	case lookup.State == "confirmed":
		data := map[string]any{"knowledge_id": proposal.KnowledgeID, "rule": ReuseRule, "operation_id": operationID, "arguments_hash": argumentsHash, "command": proposal.Command}
		return emitKnowledgeEvent(ctx, state, "knowledge.used", data, evidence)
	}
	return nil
}

func emitKnowledgeEvent(ctx workflow.Context, state *eventState, eventType string, data map[string]any, evidence []observation.Evidence) error {
	state.sequence++
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
	item := observation.Event{Schema: observation.Schema, EventID: eventID, OccurredAt: workflow.Now(ctx).UTC(), Source: observation.Source{ID: state.run.SourceID, Integration: "temporality-agent-kernel", Version: "0.1"}, Context: observation.Context{Project: state.run.Project, Run: state.run.RunID, Task: state.run.TaskID, Actor: observation.Actor{ID: state.run.ActorID, Type: "agent"}, ParentEventID: state.previousEventID}, Type: eventType, Data: data, Evidence: evidence}
	if err := workflow.ExecuteActivity(ctx, ActivityRecordEvent, item).Get(ctx, nil); err != nil {
		return err
	}
	state.previousEventID = eventID
	return nil
}

func KernelTools() []llm.ToolDef {
	return []llm.ToolDef{
		{Name: "echo", Description: "Return a short text value for debugging the harness tool path", Parameters: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}},
		{Name: "remember", Description: "Record a high-confidence durable conclusion with optional evidence refs. Verification and build outcomes are recorded automatically; use this only for conclusions you are confident in and can ground in evidence. If the conclusion came from applying a skill, include skill_id and capability", Parameters: map[string]any{"type": "object", "properties": map[string]any{"proposition": map[string]any{"type": "string"}, "evidence": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "skill_id": map[string]any{"type": "string", "description": "Skill this knowledge came from, e.g. deploy-service"}, "capability": map[string]any{"type": "string", "description": "Specific capability of the skill, e.g. verify"}}, "required": []string{"proposition"}}},
		{Name: "request_approval", Description: "Pause this run and request a human decision before a consequential action", Parameters: map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}, "required": []string{"action"}}},
		{Name: "list_triggers", Description: "List all configured triggers (schedules, webhooks, event listeners) for this project", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "create_trigger", Description: "Create a new trigger to automatically launch agent runs. Types: schedule (cron-based), webhook (HTTP endpoint), event (reacts to journal events).", Parameters: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "type": map[string]any{"type": "string", "enum": []string{"schedule", "webhook", "event"}}, "cron": map[string]any{"type": "string", "description": "Cron expression for schedule triggers, e.g. '0 9 * * 1-5'"}, "prompt": map[string]any{"type": "string", "description": "The prompt sent to the agent when the trigger fires"}, "path": map[string]any{"type": "string", "description": "URL path for webhook triggers"}, "event_type": map[string]any{"type": "string", "description": "Event type to react to for event triggers, e.g. 'tool.failed'"}, "agent_id": map[string]any{"type": "string", "description": "Agent to use (optional, uses default if empty)"}}, "required": []string{"name", "type", "prompt"}}},
		{Name: "update_trigger", Description: "Update an existing trigger's configuration (enable/disable, change cron, update prompt, etc.)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"trigger_id": map[string]any{"type": "string"}, "enabled": map[string]any{"type": "boolean"}, "cron": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}}, "required": []string{"trigger_id"}}},
		{Name: "delete_trigger", Description: "Delete a trigger by ID", Parameters: map[string]any{"type": "object", "properties": map[string]any{"trigger_id": map[string]any{"type": "string"}}, "required": []string{"trigger_id"}}},
		{Name: "skill_search", Description: "List skills available in this project with name, version, capabilities and tools", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "Optional substring to filter by name, capability or tool"}}}},
		{Name: "skill_inspect", Description: "Show a skill's full SKILL.md and manifest contract (capabilities, tools, runtime, preconditions, postconditions, evidence)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_validate", Description: "Validate a skill's manifest and return issues", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_history", Description: "List a skill's versions", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_executions", Description: "List recent executions of a skill (runs with this skill attached)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_memory", Description: "List knowledge recorded from a skill's executions (memory stays a separate temporal layer; it never modifies the skill)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
	}
}
