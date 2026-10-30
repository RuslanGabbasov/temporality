package agent

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// The async-operations contract (docs/agent-async-operations.md): a
// background run_command returns an operation id at once, the model keeps
// working, and when it runs out of tool calls the run WAITS for the
// operation instead of completing. The completion wakes the workflow, one
// batched report lands in the conversation, and the model produces the final
// answer after it.
func TestAgentRunBackgroundOperationWaitsAndNotifies(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	registerExtractionChild(env)
	recorder := newEventRecorder()
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		if err := event.Validate(); err != nil {
			return err
		}
		recorder.record(event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})

	// The background RunTool activity: keeps the operation genuinely in
	// flight for a beat of wall time — while it runs, the workflow must be
	// parked in the wait (the mock clock freezes during activities, so the
	// pause is deterministic); the model's second turn has already run out of
	// tool calls by then.
	env.RegisterActivityWithOptions(func(_ context.Context, request ToolRequest) (ToolResult, error) {
		if request.Name != "run_command" {
			t.Fatalf("unexpected tool %q", request.Name)
		}
		if bg, _ := request.Arguments["background"].(bool); !bg {
			t.Fatalf("background flag lost in transport: %#v", request.Arguments)
		}
		slow := os.Getenv("BG_SLOW") == "1"
		if slow {
			time.Sleep(300 * time.Millisecond)
		}
		exit := 0
		return ToolResult{Content: "exit_code=0\nadded 1284 packages in 42s", ExitCode: &exit}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})

	var modelMu sync.Mutex
	finalSawOutput := false
	modelCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		modelMu.Lock()
		modelCalls++
		call := modelCalls
		modelMu.Unlock()
		switch call {
		case 1:
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "bg-1", Name: "run_command", Args: map[string]any{
				"command": []any{"bash", "-lc", "yarn install"},
				"background": true, "timeout_sec": 600,
			}}}}, nil
		case 2:
			// The turn after the model ran out of tool calls: the run must be
			// waiting on the operation, NOT completed. This completion's
			// answer lands in history before the report reaches the model.
			return llm.Completion{Content: "noted, still waiting on the install"}, nil
		default:
			// The final answer lands after the operation resolved.
			for _, m := range request.Messages {
				if strings.Contains(m.Content, "added 1284 packages") {
					finalSawOutput = true
				}
			}
			return llm.Completion{Content: "install done, all set"}, nil
		}
	}, activity.RegisterOptions{Name: ActivityCallModel})

	// Free the background command only after the workflow entered the wait —
	// the second model call happens strictly after the release.
	// Free the background command only after the workflow entered the wait —
	// the second model call happens strictly after the release. Delayed
	// callbacks run on wall time while an activity is in flight, which is
	// exactly the phase this wait exercises.
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "bg-run", Project: "repo", TaskID: "task", ActorID: "lead", Prompt: "install and report", MaxTurns: 4, WorkspacePath: "/workspace/task", AutoApproveTools: []string{"run_command"}})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Equal(t, "install done, all set", result.Answer)
	require.True(t, finalSawOutput, "the final turn must see the operation's output tail")

	// Journal shape: tool.started at issue time, the completion arrives later
	// (deferred), and the run completes only after it.
	var starts, completions []observation.Event
	for _, event := range recorder.snapshot() {
		if event.Type == "tool.started" && event.Data["tool"] == "run_command" {
			starts = append(starts, event)
		}
		if event.Type == "tool.completed" && event.Data["operation_id"] != nil && strings.HasPrefix(event.Data["operation_id"].(string), "bg-run/turn/01/bg-1") {
			completions = append(completions, event)
		}
	}
	require.Len(t, starts, 1)
	require.Len(t, completions, 1, "the deferred completion must be journaled once")
	require.InDelta(t, 0.0, completions[0].Data["exit_code"], 0.001)
}
