// Package harness is a deliberately ordinary tool-calling agent loop: system
// prompt, task, tool calls, results. It knows nothing about memory internals;
// an optional Hooks implementation attaches from the side, mirroring how an
// external memory layer would integrate with any existing harness.
package harness

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/aml/llm"
)

// Hint is a short memory-layer message the agent may consider. The harness
// only renders its text.
type Hint struct {
	Text string
}

// ToolResult is the environment outcome of one tool call. Cause is the
// classified failure reason ("", auth, parameter, not_found, server) used by
// the memory layer for outcome attribution.
type ToolResult struct {
	OK     bool
	Status int
	Text   string
	Cause  string
}

// Environment executes tools and describes them.
type Environment interface {
	Tools() []llm.ToolDef
	Execute(call llm.ToolCall) ToolResult
}

// Hooks is the memory integration surface. Every method must be cheap and
// must never block the loop on failure; implementations decide what (if
// anything) to inject.
type Hooks interface {
	TaskStart(ctx context.Context, sessionID, taskID, taskText string) []Hint
	BeforeToolCall(ctx context.Context, sessionID string, call llm.ToolCall) []Hint
	AfterToolCall(ctx context.Context, sessionID string, call llm.ToolCall, result ToolResult)
	TaskEnd(ctx context.Context, sessionID, taskID string, success bool, answer string)
}

// PostToolHintHooks can optionally return context after seeing a tool result.
// Keeping this separate preserves compatibility with simple hook adapters.
type PostToolHintHooks interface {
	HintsAfterToolCall(ctx context.Context, sessionID string, call llm.ToolCall, result ToolResult) []Hint
}

// StepRecord captures one model turn for forensics.
type StepRecord struct {
	Step         int
	ToolCalls    []llm.ToolCall
	ResultStatus []int
	FinishReason string
}

// Result is one task run.
type Result struct {
	TaskID      string
	SessionID   string
	Success     bool
	Answer      string
	Steps       int
	ToolCalls   int
	FailedCalls int
	FirstOKStep int // 1-based step of the first successful call; 0 if none
	Usage       llm.Usage
	WallMS      int64
	Trace       []StepRecord
	StoppedOn   string // "answer" | "budget" | "error"
}

const systemPrompt = `You are an operations agent working with internal HTTP APIs.
You have two tools: api_call(service, resource, auth, params) to call an API, and api_docs(service) to read a service's public documentation.
Gateway policies, auth requirements and parameter rules are not fully documented; probe and adapt when calls fail.
Work economically: avoid repeating a call that already failed with identical arguments.
When you have the requested information, reply with the final answer containing the requested values and no tool calls.`

// Runner executes task runs against one environment.
type Runner struct {
	Client   *llm.Client
	MaxSteps int
	// SystemPrompt optionally overrides the world description; empty means the
	// default operations-agent prompt. The reasoning loop is identical.
	SystemPrompt string
}

// Run executes one task. hooks may be nil (no memory attached). check, when
// non-nil, grades the final answer so the memory layer observes the true
// task outcome.
func (r *Runner) Run(ctx context.Context, env Environment, hooks Hooks, sessionID, taskID, taskText string, check func(answer string) bool) Result {
	started := time.Now()
	result := Result{TaskID: taskID, SessionID: sessionID}

	taskMessage := taskText
	if hooks != nil {
		if hints := hooks.TaskStart(ctx, sessionID, taskID, taskText); len(hints) > 0 {
			taskMessage = taskText + "\n\n" + renderHints(hints)
		}
	}
	system := r.SystemPrompt
	if system == "" {
		system = systemPrompt
	}
	messages := []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: taskMessage},
	}
	tools := env.Tools()

	for step := 1; step <= r.MaxSteps; step++ {
		completion, err := r.Client.Complete(ctx, messages, tools)
		if err != nil {
			if len(result.Trace) == 0 && result.Steps == 0 {
				result.StoppedOn = "error"
				result.WallMS = time.Since(started).Milliseconds()
				return result
			}
			result.StoppedOn = "error"
			break
		}
		result.Steps = step
		result.Usage.PromptTokens += completion.Usage.PromptTokens
		result.Usage.CompletionTokens += completion.Usage.CompletionTokens
		result.Usage.TotalTokens += completion.Usage.TotalTokens

		if len(completion.ToolCalls) == 0 {
			result.Answer = completion.Content
			result.StoppedOn = "answer"
			break
		}
		messages = append(messages, llm.Message{Role: "assistant", Content: completion.Content, ToolCalls: completion.ToolCalls})

		record := StepRecord{Step: step, FinishReason: completion.Finish}
		var postHints []Hint
		for _, call := range completion.ToolCalls {
			if hooks != nil {
				postHints = append(postHints, hooks.BeforeToolCall(ctx, sessionID, call)...)
			}
			outcome := env.Execute(call)
			result.ToolCalls++
			if !outcome.OK {
				result.FailedCalls++
			} else if result.FirstOKStep == 0 {
				result.FirstOKStep = step
			}
			record.ToolCalls = append(record.ToolCalls, call)
			record.ResultStatus = append(record.ResultStatus, outcome.Status)
			if hooks != nil {
				hooks.AfterToolCall(ctx, sessionID, call, outcome)
			}
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: outcome.Text})
			if postTool, ok := hooks.(PostToolHintHooks); ok {
				postHints = append(postHints, postTool.HintsAfterToolCall(ctx, sessionID, call, outcome)...)
			}
		}
		result.Trace = append(result.Trace, record)
		if len(postHints) > 0 {
			messages = append(messages, llm.Message{Role: "user", Content: renderHints(postHints)})
		}
	}

	if result.StoppedOn == "" {
		// Step budget exhausted: force a final answer without tools.
		messages = append(messages, llm.Message{Role: "user", Content: "Step budget exhausted. Provide your final answer now based on what you have."})
		if completion, err := r.Client.Complete(ctx, messages, nil); err == nil {
			result.Usage.PromptTokens += completion.Usage.PromptTokens
			result.Usage.CompletionTokens += completion.Usage.CompletionTokens
			result.Usage.TotalTokens += completion.Usage.TotalTokens
			result.Answer = completion.Content
			result.StoppedOn = "budget"
		} else {
			result.StoppedOn = "error"
		}
	}

	if check != nil {
		result.Success = check(result.Answer)
	}
	if hooks != nil {
		hooks.TaskEnd(ctx, sessionID, taskID, result.Success, result.Answer)
	}
	result.WallMS = time.Since(started).Milliseconds()
	return result
}

func renderHints(hints []Hint) string {
	var b strings.Builder
	b.WriteString("[memory] Relevant prior experience from previous sessions:\n")
	for _, hint := range hints {
		b.WriteString("- " + hint.Text + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Check verifies that every marker appears in the answer (case-insensitive).
func Check(answer string, markers []string) bool {
	lowered := strings.ToLower(answer)
	for _, marker := range markers {
		if !strings.Contains(lowered, strings.ToLower(marker)) {
			return false
		}
	}
	return len(lowered) > 0
}

// Describe renders a short one-line summary for logs.
func (r Result) Describe() string {
	return fmt.Sprintf("task=%s steps=%d calls=%d failed=%d first_ok_step=%d stopped=%s", r.TaskID, r.Steps, r.ToolCalls, r.FailedCalls, r.FirstOKStep, r.StoppedOn)
}
