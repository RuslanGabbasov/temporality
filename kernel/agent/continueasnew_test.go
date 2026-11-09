package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/temporality-project/temporality/kernel/harness"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// ContinueAsNew (docs/context-compaction.md §5): a run whose accumulated
// request bytes cross the budget hands the carried state to a fresh execution
// at the next clean turn boundary — the workflow history resets instead of
// growing into the server's history-size ceiling (which TERMINATES runs
// mid-work). The journal chain and the conversation must survive the handoff.
func TestAgentRunContinuesAsNewAtHistoryBudget(t *testing.T) {
	previous := harness.Current()
	budgets := previous.Budgets
	budgets.ContinueAsNewBytes = 1           // any scheduled request ends the execution
	budgets.CompactionTriggerBytes = 1 << 30 // compaction stays out of the way
	harness.SetCurrent(harness.Settings{Budgets: budgets})
	defer harness.SetCurrent(previous)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	registerExtractionChild(env)
	recorder := newEventRecorder()
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		recorder.record(event)
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
		return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "t-1", Name: "run_command", Args: map[string]any{
			"command": []string{"echo", "hi"},
		}}}}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		return ToolResult{Content: "hi"}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	registerExtraction(env, KnowledgeExtractResult{}, nil)

	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-1", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "build the thing", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())

	var continued *workflow.ContinueAsNewError
	require.ErrorAs(t, env.GetWorkflowError(), &continued, "the run must continue-as-new instead of finishing")
	require.Equal(t, 1, modelCalls, "the boundary must fire before the next model call")

	next := RunInput{}
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(continued.Input, &next))
	require.Equal(t, "run-1", next.RunID, "the run id stays the same — one run in the UI")
	require.NotNil(t, next.Resume, "the state must ride the continuation")
	require.Equal(t, 2, next.Resume.NextTurn, "the next execution continues with the next turn")
	require.NotEmpty(t, next.Resume.Messages, "the conversation must survive the handoff")
	require.Greater(t, next.Resume.EventSequence, 0, "the journal chain must continue, not restart")
	require.NotEmpty(t, next.Resume.PreviousEventID, "the caused_by chain must survive")
	require.Contains(t, next.Resume.Messages[len(next.Resume.Messages)-1].Content, "hi", "the tool replies must survive the handoff")
}
