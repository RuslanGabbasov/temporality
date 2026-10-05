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

// The cancel timeout policy (docs/org-structure.md §33) stops the run instead
// of letting the model wander without the missing answer.
func TestAgentRunAskHumanCancelPolicyStopsRun(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, NotifyChannelRequest) (NotificationDelivery, error) {
		return NotificationDelivery{Recipient: "lead", Channel: "web", Status: "delivered"}, nil
	}, activity.RegisterOptions{Name: ActivityNotifyChannel})
	env.RegisterActivityWithOptions(func(context.Context, CloseHumanRequestInput) error { return nil }, activity.RegisterOptions{Name: ActivityCloseHumanRequest})
	modelCalls := 0
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		modelCalls++
		return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "ask-1", Name: "ask_human", Args: map[string]any{
			"question": "Drop the legacy table?", "timeout_sec": 1, "timeout_policy": "cancel",
		}}}}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-1", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "clean up", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "cancelled", result.Status)
	require.Equal(t, 1, modelCalls, "no further model turns after a cancel policy")

	types := make(map[string]bool)
	for _, event := range recorded {
		types[event.Type] = true
	}
	require.True(t, types["human.timed_out"], "missing human.timed_out")
	require.False(t, types["run.completed"], "a cancelled run must not emit run.completed")
}

// The retry timeout policy (§33) waits one extra round before giving up.
func TestAgentRunAskHumanRetryPolicyWaitsSecondRound(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	notifications := 0
	env.RegisterActivityWithOptions(func(context.Context, NotifyChannelRequest) (NotificationDelivery, error) {
		notifications++
		return NotificationDelivery{Recipient: "lead", Channel: "web", Status: "delivered"}, nil
	}, activity.RegisterOptions{Name: ActivityNotifyChannel})
	env.RegisterActivityWithOptions(func(context.Context, CloseHumanRequestInput) error { return nil }, activity.RegisterOptions{Name: ActivityCloseHumanRequest})
	modelCalls := 0
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		modelCalls++
		if modelCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "ask-1", Name: "ask_human", Args: map[string]any{
				"question": "Which region?", "timeout_sec": 1, "timeout_policy": "retry",
			}}}}, nil
		}
		return llm.Completion{Content: "Proceeding with the default region."}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-1", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "deploy", MaxTurns: 4, ApprovalTimeoutSeconds: 1})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	types := make(map[string]bool)
	reasked := 0
	for _, event := range recorded {
		types[event.Type] = true
		if event.Type == "human.reasked" {
			reasked++
		}
	}
	require.Equal(t, 1, reasked, "retry policy re-asks exactly once")
	require.Equal(t, 2, notifications, "both rounds deliver a notification")
	require.True(t, types["human.timed_out"], "second round may still time out")
}

// ResolveRecipient covers the §26 logical recipients.
func TestResolveRecipient(t *testing.T) {
	users := []WorkspaceUser{
		{ID: "ruslan", Name: "Ruslan", Role: "admin", Active: true, OrgUnitID: "core"},
		{ID: "eldar", Name: "Eldar", Role: "operator", Active: true, OrgUnitID: "karma"},
		{ID: "ghost", Name: "Ghost", Role: "admin", Active: false},
	}
	tests := []struct {
		name      string
		recipient string
		actor     string
		wantID    string
		wantFound bool
	}{
		{"explicit user id", "user:ruslan", "", "ruslan", true},
		{"explicit name", "Eldar", "", "eldar", true},
		{"role admin skips inactive", "role:admin", "", "ruslan", true},
		{"org unit", "org:karma", "", "eldar", true},
		{"project owner is an admin", "project_owner", "", "ruslan", true},
		{"actor default", "", "ruslan", "ruslan", true},
		{"actor by name", "", "Eldar", "eldar", true},
		{"unknown user", "nobody", "", "", false},
		{"no recipient no actor", "", "", "", false},
		{"empty actor recipient", "user:", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := ResolveRecipient(tt.recipient, tt.actor, users)
			require.Equal(t, tt.wantFound, found)
			if tt.wantFound {
				require.Equal(t, tt.wantID, got.ID)
			}
		})
	}
}

// validTimeoutPolicy guards the tool argument against typos.
func TestValidTimeoutPolicy(t *testing.T) {
	for _, valid := range []string{"", "fail", "retry", "fallback", "escalate", "cancel"} {
		require.True(t, validTimeoutPolicy(valid), "%q must be valid", valid)
	}
	require.False(t, validTimeoutPolicy("FALLBACK"))
	require.False(t, validTimeoutPolicy("abort"))
}
