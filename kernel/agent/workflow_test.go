package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestMCPToolFailureDoesNotExposeRemoteError(t *testing.T) {
	message := toolFailureMessage("mcp__write_file", errors.New("response lost; token=secret"))
	if strings.Contains(message, "secret") || !strings.Contains(message, "uncertain") {
		t.Fatalf("unexpected MCP failure message: %q", message)
	}
}

func TestEventScopeIncludesProjectAndRun(t *testing.T) {
	first := eventScope("source-a", "project-a", "run-1")
	if first != eventScope("source-a", "project-a", "run-1") {
		t.Fatal("event scope must be stable")
	}
	if first == eventScope("source-a", "project-b", "run-1") || first == eventScope("source-a", "project-a", "run-2") || first == eventScope("source-b", "project-a", "run-1") {
		t.Fatal("event IDs must be isolated by project and run")
	}
}

func TestAgentRunRecordsApprovalAndKnowledgeTrajectory(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		if err := event.Validate(); err != nil {
			return err
		}
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	modelCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if len(request.Messages) == 0 || len(request.Tools) != 3 {
			t.Fatalf("unexpected model request: %#v", request)
		}
		modelCalls++
		switch modelCalls {
		case 1:
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "approval-1", Name: "request_approval", Args: map[string]any{"action": "apply patch", "reason": "write change"}}}}, nil
		case 2:
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "remember-1", Name: "remember", Args: map[string]any{"proposition": "The fix is verified", "evidence": []any{"test-run-1"}}}}}, nil
		default:
			return llm.Completion{Content: "completed"}, nil
		}
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ApprovalSignal, Approval{OperationID: "run-1/turn/01/approval-1", Approved: true, ActorID: "reviewer-1", Reason: "reviewed"})
	}, time.Second)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-1", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "verify the fix", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Equal(t, "completed", result.Answer)
	require.Equal(t, 3, result.Turns)

	types := make(map[string]bool)
	for index, event := range recorded {
		types[event.Type] = true
		if index > 0 {
			require.Equal(t, recorded[index-1].EventID, event.Context.ParentEventID)
		}
		require.Equal(t, "repo-a", event.Context.Project)
		require.NotEmpty(t, event.Context.Run)
	}
	for _, expected := range []string{"run.started", "model.started", "model.completed", "approval.requested", "approval.granted", "knowledge.proposed", "tool.completed", "run.completed"} {
		require.True(t, types[expected], "missing %s in recorded event stream", expected)
	}
}
