package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

func registerPolicyEnv(t *testing.T, model func(call int, request ModelRequest) (llm.Completion, error)) (*testsuite.TestWorkflowEnvironment, *[]observation.Event, *bool) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	recorded := &[]observation.Event{}
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		*recorded = append(*recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	calls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		calls++
		return model(calls, request)
	}, activity.RegisterOptions{Name: ActivityCallModel})
	executed := false
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		executed = true
		return ToolResult{Content: "tool ok"}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	return env, recorded, &executed
}

func eventByType(events []observation.Event, kind string) (observation.Event, bool) {
	for _, event := range events {
		if event.Type == kind {
			return event, true
		}
	}
	return observation.Event{}, false
}

// approval_mode: tools (docs/org-structure.md §24) forces a human decision
// before consequential tools even when the run configuration allows them.
func TestAgentRunPolicyToolApprovalForcesConfirmation(t *testing.T) {
	env, recorded, executed := registerPolicyEnv(t, func(call int, _ ModelRequest) (llm.Completion, error) {
		if call == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "run_command", Args: map[string]any{"command": []any{"ls"}}}}}, nil
		}
		return llm.Completion{Content: "blocked by policy"}, nil
	})
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "policy-approval", Project: "repo-a", Prompt: "list files", RequireToolApproval: true, ApprovalTimeoutSeconds: 1})
	require.NoError(t, env.GetWorkflowError())
	require.False(t, *executed, "a consequential tool must not execute without approval under the policy")
	events := *recorded
	require.NotEqual(t, -1, indexEvent(events, "approval.requested"))
	require.NotEqual(t, -1, indexEvent(events, "tool.blocked"))
	require.Equal(t, -1, indexEvent(events, "tool.started"))
}

// Policy-safe tools (observation, escalation) keep running without approval.
func TestAgentRunPolicySafeToolsRunWithoutApproval(t *testing.T) {
	env, recorded, executed := registerPolicyEnv(t, func(call int, _ ModelRequest) (llm.Completion, error) {
		if call == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "skill_search", Args: map[string]any{}}}}, nil
		}
		return llm.Completion{Content: "searched"}, nil
	})
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "policy-safe", Project: "repo-a", Prompt: "find skills", RequireToolApproval: true})
	require.NoError(t, env.GetWorkflowError())
	require.True(t, *executed, "policy-safe tools must execute without approval")
	events := *recorded
	require.Equal(t, -1, indexEvent(events, "approval.requested"))
	require.NotEqual(t, -1, indexEvent(events, "tool.completed"))
}

// A token budget exhausted mid-run stops the loop and forces an immediate
// final answer instead of failing silently.
func TestAgentRunPolicyTokenBudgetStopsRunWithForcedFinale(t *testing.T) {
	var modelRequests []ModelRequest
	env, recorded, _ := registerPolicyEnv(t, func(_ int, request ModelRequest) (llm.Completion, error) {
		modelRequests = append(modelRequests, request)
		if len(request.Tools) == 0 {
			return llm.Completion{Content: "partial report"}, nil
		}
		return llm.Completion{
			Usage:     llm.Usage{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150},
			ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "run_command", Args: map[string]any{"command": []any{"ls"}}}},
		}, nil
	})
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "policy-tokens", Project: "repo-a", Prompt: "work", MaxTurns: 6, TokenBudget: 100})
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "token_limit", result.Status)
	require.Equal(t, "partial report", result.Answer)
	events := *recorded
	limit, ok := eventByType(events, "policy.limit")
	require.True(t, ok, "policy.limit must be recorded")
	require.Equal(t, "tokens_exhausted", limit.Data["reason"])
	require.Equal(t, float64(150), limit.Data["used_tokens"])
	completed, ok := eventByType(events, "run.completed")
	require.True(t, ok)
	require.Equal(t, "token_limit", completed.Data["status"])
	// The finale runs without tools and without dangling unanswered tool calls.
	require.NotEmpty(t, modelRequests)
	require.Empty(t, modelRequests[len(modelRequests)-1].Tools)
	require.Equal(t, -1, indexEvent(events, "tool.started"), "no tool may run after the budget is exhausted")
}

// A USD budget is scored from the run-config-resolved model prices.
func TestAgentRunPolicyBudgetLimitStopsRun(t *testing.T) {
	env, recorded, _ := registerPolicyEnv(t, func(_ int, request ModelRequest) (llm.Completion, error) {
		if len(request.Tools) == 0 {
			return llm.Completion{Content: "stopped"}, nil
		}
		return llm.Completion{
			Usage:     llm.Usage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000},
			ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "run_command", Args: map[string]any{"command": []any{"ls"}}}},
		}, nil
	})
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "policy-budget", Project: "repo-a", Prompt: "work", MaxTurns: 6, MaxBudgetUSD: 0.01, ModelPromptPricePer1k: 1, ModelCompPricePer1k: 1})
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "budget_limit", result.Status)
	events := *recorded
	limit, ok := eventByType(events, "policy.limit")
	require.True(t, ok)
	require.Equal(t, "budget_exhausted", limit.Data["reason"])
}

// The time budget is enforced at turn boundaries: elapsed mock time (an
// unanswered ask_human wait) exhausts it before the next turn starts.
func TestAgentRunPolicyTimeoutStopsRun(t *testing.T) {
	env, recorded, _ := registerPolicyEnv(t, func(call int, _ ModelRequest) (llm.Completion, error) {
		if call <= 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "ask-1", Name: "ask_human", Args: map[string]any{
				"question": "Which region?", "timeout_sec": 1, "timeout_policy": "fallback",
			}}}}, nil
		}
		return llm.Completion{Content: "time is up report"}, nil
	})
	env.RegisterActivityWithOptions(func(context.Context, NotifyChannelRequest) (NotificationDelivery, error) {
		return NotificationDelivery{Recipient: "lead", Channel: "web", Status: "delivered"}, nil
	}, activity.RegisterOptions{Name: ActivityNotifyChannel})
	env.RegisterActivityWithOptions(func(context.Context, CloseHumanRequestInput) error { return nil }, activity.RegisterOptions{Name: ActivityCloseHumanRequest})
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "policy-time", Project: "repo-a", Prompt: "deploy", MaxTurns: 6, TimeoutSeconds: 1, ApprovalTimeoutSeconds: 1})
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "time_limit", result.Status)
	events := *recorded
	limit, ok := eventByType(events, "policy.limit")
	require.True(t, ok)
	require.Equal(t, "time_exhausted", limit.Data["reason"])
}
