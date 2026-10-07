package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// registerExtraction registers a stub knowledge-extraction activity so
// completed runs exercise the extraction phase without a model.
func registerExtraction(env *testsuite.TestWorkflowEnvironment, result KnowledgeExtractResult, err error) {
	env.RegisterActivityWithOptions(func(context.Context, KnowledgeExtractRequest) (KnowledgeExtractResult, error) {
		return result, err
	}, activity.RegisterOptions{Name: ActivityExtractKnowledge})
}

func TestMCPToolFailureDoesNotExposeRemoteError(t *testing.T) {
	message := toolFailureMessage("mcp__write_file", errors.New("response lost; token=secret"))
	if strings.Contains(message, "secret") || !strings.Contains(message, "uncertain") {
		t.Fatalf("unexpected MCP failure message: %q", message)
	}
}

func TestRejectedToolArgumentsAreReportedAsFixable(t *testing.T) {
	rejected := temporal.NewNonRetryableApplicationError("command must be an array of strings", "InvalidToolArguments", nil)
	message := toolFailureMessage("run_command", rejected)
	if !strings.Contains(message, "no effect") || !strings.Contains(message, "array of strings") {
		t.Fatalf("argument rejection must explain the fix: %q", message)
	}
	uncertain := toolFailureMessage("run_command", errors.New("docker daemon gone"))
	if !strings.Contains(uncertain, "uncertain") {
		t.Fatalf("execution failures keep the uncertain-effect caution: %q", uncertain)
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
		if len(request.Messages) == 0 {
			t.Fatalf("unexpected model request: %#v", request)
		}
		// Kernel tools grow over time (triggers, skills); assert the ones this
		// trajectory exercises instead of an exact count.
		names := make(map[string]bool, len(request.Tools))
		for _, tool := range request.Tools {
			names[tool.Name] = true
		}
		for _, required := range []string{"remember", "request_approval", "skill_inspect"} {
			if !names[required] {
				t.Fatalf("model request missing kernel tool %q: %#v", required, request.Tools)
			}
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
	registerExtraction(env, KnowledgeExtractResult{}, nil)
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

func TestAgentRunAskHumanDeliversResponseAndRecordsTrajectory(t *testing.T) {
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
	notified := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request NotifyChannelRequest) (NotificationDelivery, error) {
		notified++
		require.Equal(t, "run-1", request.RunID)
		require.Equal(t, "postgres", request.Options[0])
		return NotificationDelivery{Recipient: "lead", Channel: "web", Status: "delivered"}, nil
	}, activity.RegisterOptions{Name: ActivityNotifyChannel})
	closed := 0
	env.RegisterActivityWithOptions(func(_ context.Context, input CloseHumanRequestInput) error {
		closed++
		require.Equal(t, "run-1/turn/01/ask-1", input.OperationID)
		require.Equal(t, "answered", input.Status)
		require.Equal(t, "postgres", input.Response)
		return nil
	}, activity.RegisterOptions{Name: ActivityCloseHumanRequest})
	modelCalls := 0
	var answerSeen string
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		modelCalls++
		if modelCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "ask-1", Name: "ask_human", Args: map[string]any{"question": "Which database should I use?", "context": "Setting up the service", "options": []any{"postgres", "sqlite"}, "timeout_sec": 120}}}}, nil
		}
		// The tool result from turn 1 must carry the human's answer verbatim.
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "Human response:") {
				answerSeen = message.Content
			}
		}
		return llm.Completion{Content: "completed"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ApprovalSignal, Approval{OperationID: "run-1/turn/01/ask-1", Approved: true, ActorID: "human-1", Response: "postgres"})
	}, time.Second)
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-1", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "set up the database", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Contains(t, answerSeen, "Human response: postgres")

	types := make(map[string]bool)
	for _, event := range recorded {
		types[event.Type] = true
	}
	for _, expected := range []string{"human.requested", "human.answered", "notification.sent", "tool.completed", "run.completed"} {
		require.True(t, types[expected], "missing %s in recorded event stream", expected)
	}
	for _, forbidden := range []string{"approval.requested", "approval.granted"} {
		require.False(t, types[forbidden], "ask_human must not emit approval events, found %s", forbidden)
	}
	require.Equal(t, 1, notified, "the question is delivered exactly once")
	require.Equal(t, 1, closed, "the request row is closed exactly once")
}

func TestAgentRunTimesOutAnUnansweredApproval(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error { recorded = append(recorded, event); return nil }, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	modelCalls := 0
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		modelCalls++
		if modelCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "approval-1", Name: "request_approval", Args: map[string]any{"action": "write"}}}}, nil
		}
		return llm.Completion{Content: "approval expired"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "timeout-run", Project: "repo-a", Prompt: "write", ApprovalTimeoutSeconds: 2})
	require.NoError(t, env.GetWorkflowError())
	types := map[string]bool{}
	for _, event := range recorded {
		types[event.Type] = true
	}
	require.True(t, types["approval.requested"])
	require.True(t, types["approval.timed_out"])
	require.False(t, types["approval.granted"])
}

func TestAgentRunRecordsGeneratedTitle(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error { recorded = append(recorded, event); return nil }, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, TitleRequest) (string, error) { return "Fix login timeout", nil }, activity.RegisterOptions{Name: ActivityGenerateTitle})
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		return llm.Completion{Content: "done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-titled", Project: "repo-a", Prompt: "please fix the login timeout", MaxTurns: 1})
	require.NoError(t, env.GetWorkflowError())
	var started *observation.Event
	for index := range recorded {
		if recorded[index].Type == "run.started" {
			started = &recorded[index]
			break
		}
	}
	require.NotNil(t, started, "run.started must be recorded")
	require.Equal(t, "Fix login timeout", started.Data["title"])
}

func TestApprovedToolOperationIsVisibleAndHashBound(t *testing.T) {
	run := func(t *testing.T, signalHash string) ([]observation.Event, bool) {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
		var recorded []observation.Event
		env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
			require.NoError(t, event.Validate())
			recorded = append(recorded, event)
			return nil
		}, activity.RegisterOptions{Name: ActivityRecordEvent})
		env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
		modelCalls := 0
		env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
			modelCalls++
			if modelCalls > 1 {
				return llm.Completion{Content: "tests complete"}, nil
			}
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "run_command", Args: map[string]any{"command": []any{"sh", "-c", "TOKEN=top-secret go test ./..."}}}}}, nil
		}, activity.RegisterOptions{Name: ActivityCallModel})
		executed := false
		env.RegisterActivityWithOptions(func(_ context.Context, request ToolRequest) (ToolResult, error) {
			executed = true
			require.Equal(t, "run-approval/turn/01/call-1", request.OperationID)
			return ToolResult{Content: "exit_code=0"}, nil
		}, activity.RegisterOptions{Name: ActivityRunTool})
		args := map[string]any{"command": []any{"sh", "-c", "TOKEN=top-secret go test ./..."}}
		if signalHash == "<correct>" {
			signalHash = operationArgumentsHash(args)
		}
		hash := signalHash
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow(ApprovalSignal, Approval{OperationID: "run-approval/turn/01/call-1", ArgumentsHash: hash, Approved: true, ActorID: "reviewer", Reason: "approved"})
		}, time.Second)
		registerExtraction(env, KnowledgeExtractResult{}, nil)
		env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-approval", Project: "repo-a", ActorID: "coder", Prompt: "run tests", ApprovalTimeoutSeconds: 3, ApprovalTools: []string{"run_command"}})
		require.NoError(t, env.GetWorkflowError())
		return recorded, executed
	}

	t.Run("matching approval runs the exact operation", func(t *testing.T) {
		events, executed := run(t, "<correct>")
		require.True(t, executed)
		byType := map[string]observation.Event{}
		for _, event := range events {
			byType[event.Type] = event
		}
		requested, granted := byType["approval.requested"], byType["approval.granted"]
		started, completed := byType["tool.started"], byType["tool.completed"]
		require.NotEmpty(t, requested.EventID)
		require.Less(t, indexEvent(events, "approval.requested"), indexEvent(events, "approval.granted"))
		require.Less(t, indexEvent(events, "approval.granted"), indexEvent(events, "tool.started"))
		require.Less(t, indexEvent(events, "tool.started"), indexEvent(events, "tool.completed"))
		require.Equal(t, requested.EventID, granted.Context.ParentEventID)
		require.Equal(t, granted.EventID, started.Context.ParentEventID)
		require.Equal(t, started.EventID, completed.Context.ParentEventID)
		for _, event := range []observation.Event{requested, granted, started, completed} {
			require.Equal(t, "run-approval/turn/01/call-1", event.Data["operation_id"])
			require.Equal(t, operationArgumentsHash(map[string]any{"command": []any{"sh", "-c", "TOKEN=top-secret go test ./..."}}), event.Data["arguments_hash"])
			require.Equal(t, "run-approval", event.Context.Run)
			require.Equal(t, "run-approval/turn/01", event.Data["frame_id"])
		}
		encoded, err := json.Marshal(requested)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "top-secret")
		display := requested.Data["operation"].(map[string]any)["arguments"]
		require.Contains(t, fmt.Sprint(display), "[REDACTED]")
	})

	t.Run("mismatched hash cannot execute", func(t *testing.T) {
		events, executed := run(t, "sha256:not-the-requested-arguments")
		require.False(t, executed)
		require.Equal(t, -1, indexEvent(events, "tool.started"))
		require.Equal(t, -1, indexEvent(events, "tool.completed"))
		require.NotEqual(t, -1, indexEvent(events, "approval.rejected"))
		require.NotEqual(t, -1, indexEvent(events, "tool.blocked"))
	})
}

func TestSandboxAutoApprovalIsRecordedAndRunsWithoutHumanSignal(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var events []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error { events = append(events, event); return nil }, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, KnowledgeLookupQuery) (KnowledgeLookupResult, error) {
		return KnowledgeLookupResult{}, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeLookup})
	calls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		calls++
		if calls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "sandbox-call", Name: "run_command", Args: map[string]any{"command": []any{"go", "test", "./..."}}}}}, nil
		}
		return llm.Completion{Content: "tests passed"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	run := false
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		run = true
		return ToolResult{Content: "exit_code=0"}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "sandbox-auto", Project: "repo", Prompt: "run tests", WorkspacePath: "/workspace/task", AutoApproveTools: []string{"run_command"}})
	require.NoError(t, env.GetWorkflowError())
	require.True(t, run)
	require.Equal(t, -1, indexEvent(events, "approval.requested"))
	require.Equal(t, -1, indexEvent(events, "approval.granted"))
	auto, started, completed := indexEvent(events, "approval.auto_granted"), indexEvent(events, "tool.started"), indexEvent(events, "tool.completed")
	require.GreaterOrEqual(t, auto, 0)
	require.Less(t, auto, started)
	require.Less(t, started, completed)
	autoEvent := events[auto]
	require.Equal(t, "sandbox.workspace.v1", autoEvent.Data["policy_id"])
	require.Equal(t, "sandbox-auto/turn/01/sandbox-call", autoEvent.Data["operation_id"])
	require.Equal(t, "/workspace/task", autoEvent.Data["workspace_path"])
}

func indexEvent(events []observation.Event, kind string) int {
	for index, event := range events {
		if event.Type == kind {
			return index
		}
	}
	return -1
}

func knowledgeEvents(events []observation.Event, kind string) []observation.Event {
	var matched []observation.Event
	for _, event := range events {
		if event.Type == kind {
			matched = append(matched, event)
		}
	}
	return matched
}

func TestAgentRunForcesFinalAnswerAtTurnLimit(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var events []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		events = append(events, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	var modelRequests []ModelRequest
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		modelRequests = append(modelRequests, request)
		if len(request.Tools) == 0 {
			return llm.Completion{Content: "final report"}, nil
		}
		return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "run_command", Args: map[string]any{"command": []any{"ls"}}}}}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	exit := 1
	env.RegisterActivityWithOptions(func(_ context.Context, _ ToolRequest) (ToolResult, error) {
		return ToolResult{Content: "exit_code=1\nboom", ExitCode: &exit}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "finale-run", Project: "repo", Prompt: "work", MaxTurns: 3, AutoApproveTools: []string{"run_command"}})
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "turn_limit", result.Status)
	require.Equal(t, "final report", result.Answer)
	require.Equal(t, 4, result.Turns)

	// Three loop calls with tools (the last one budget-aware) plus one forced
	// finale call without any tools.
	require.Len(t, modelRequests, 4)
	lastLoop, finale := modelRequests[2], modelRequests[3]
	require.NotEmpty(t, lastLoop.Tools)
	require.Empty(t, finale.Tools)
	require.Contains(t, lastLoop.Messages[len(lastLoop.Messages)-1].Content, "final turn with tools")
	require.Contains(t, finale.Messages[len(finale.Messages)-1].Content, "turn budget is exhausted")

	var finaleStarted, runCompleted, summary bool
	for _, event := range events {
		switch event.Type {
		case "turn.started":
			if event.Data["forced_finale"] == true {
				finaleStarted = true
			}
		case "run.completed":
			runCompleted = event.Data["forced_finale"] == true && event.Data["final_answer"] == true && event.Data["status"] == "turn_limit"
		case "agent.summary":
			summary = event.Data["answer"] == "final report"
		}
	}
	require.True(t, finaleStarted, "missing forced finale turn.started")
	require.True(t, runCompleted, "run.completed must record the forced finale answer")
	require.True(t, summary, "agent.summary must carry the forced finale answer")
}

func TestDuplicateToolCallsWithinOneResponseExecuteOnce(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var events []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		events = append(events, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	calls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, _ ModelRequest) (llm.Completion, error) {
		calls++
		if calls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{
				{ID: "dup-1", Name: "run_command", Args: map[string]any{"command": []any{"go", "test", "./..."}}},
				{ID: "dup-2", Name: "run_command", Args: map[string]any{"command": []any{"go", "test", "./..."}}},
				{ID: "dup-3", Name: "run_command", Args: map[string]any{"command": []any{"go", "test", "./..."}}},
			}}, nil
		}
		return llm.Completion{Content: "done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	executions := 0
	env.RegisterActivityWithOptions(func(_ context.Context, _ ToolRequest) (ToolResult, error) {
		executions++
		exit := 0
		return ToolResult{Content: "exit_code=0\nok", ExitCode: &exit}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	lookups := 0
	env.RegisterActivityWithOptions(func(_ context.Context, _ KnowledgeLookupQuery) (KnowledgeLookupResult, error) {
		lookups++
		return KnowledgeLookupResult{}, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeLookup})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "dedup-batch", Project: "repo", Prompt: "work", MaxTurns: 2, AutoApproveTools: []string{"run_command"}})
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Equal(t, "done", result.Answer)
	// The identical batch executed exactly once.
	require.Equal(t, 1, executions)
	require.Equal(t, 1, lookups)
	var originals, duplicates int
	for _, event := range events {
		if event.Type != "tool.completed" {
			continue
		}
		if _, ok := event.Data["duplicate_of"]; ok {
			duplicates++
			require.NotEmpty(t, event.Data["duplicate_of"])
		} else {
			originals++
		}
	}
	require.Equal(t, 1, originals)
	require.Equal(t, 2, duplicates)
}

func TestSuccessfulVerificationCommandsRecordExecutionKnowledge(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var events []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		if err := event.Validate(); err != nil {
			return err
		}
		events = append(events, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	lookups := 0
	env.RegisterActivityWithOptions(func(_ context.Context, query KnowledgeLookupQuery) (KnowledgeLookupResult, error) {
		require.NotEmpty(t, query.Project)
		require.True(t, strings.HasPrefix(query.KnowledgeID, "auto/"))
		lookups++
		switch lookups {
		case 1:
			return KnowledgeLookupResult{}, nil
		case 2:
			return KnowledgeLookupResult{Exists: true, State: "proposed"}, nil
		}
		return KnowledgeLookupResult{Exists: true, State: "confirmed"}, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeLookup})
	calls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		calls++
		switch calls {
		case 1:
			return llm.Completion{ToolCalls: []llm.ToolCall{
				{ID: "verify-1", Name: "run_command", Args: map[string]any{"command": []any{"go", "test", "./..."}}},
				{ID: "list-1", Name: "run_command", Args: map[string]any{"command": []any{"ls", "-la"}}},
			}}, nil
		case 2:
			return llm.Completion{ToolCalls: []llm.ToolCall{
				{ID: "verify-2", Name: "run_command", Args: map[string]any{"command": []any{"go", "test", "./..."}}},
			}}, nil
		}
		return llm.Completion{Content: "verification recorded"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	exit := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ToolRequest) (ToolResult, error) {
		return ToolResult{Content: "exit_code=0\nok", ExitCode: &exit}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "knowledge-auto", Project: "repo", Prompt: "run tests", WorkspacePath: "/workspace/task", AutoApproveTools: []string{"run_command"}})
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 2, lookups)

	proposed := knowledgeEvents(events, "knowledge.proposed")
	confirmed := knowledgeEvents(events, "knowledge.confirmed")
	require.Len(t, proposed, 1)
	require.Len(t, confirmed, 1)

	first := proposed[0]
	require.Equal(t, "observation", first.Data["kind"])
	require.Equal(t, ExecutionObservationPolicy, first.Data["policy_id"])
	require.Equal(t, "go test ./...", first.Data["command"])
	require.True(t, strings.HasPrefix(first.Data["knowledge_id"].(string), "auto/"))
	require.Contains(t, first.Data["proposition"], "`go test ./...` exited 0")
	require.NotEmpty(t, first.Evidence)
	require.Equal(t, "execution", first.Evidence[0].Type)

	second := confirmed[0]
	require.Equal(t, first.Data["knowledge_id"], second.Data["knowledge_id"])
	require.Equal(t, ReverificationRule, second.Data["rule"])

	// The proposal and confirmation must follow the tool executions that
	// justify them, and the non-verification command must not produce knowledge.
	firstTool, secondTool := -1, -1
	for index, event := range events {
		if event.Type != "tool.completed" {
			continue
		}
		if strings.HasSuffix(event.Data["operation_id"].(string), "verify-1") {
			firstTool = index
		}
		if strings.HasSuffix(event.Data["operation_id"].(string), "verify-2") {
			secondTool = index
		}
	}
	require.Greater(t, firstTool, -1)
	require.Greater(t, secondTool, -1)
	require.Less(t, firstTool, indexEvent(events, "knowledge.proposed"))
	require.Less(t, secondTool, indexEvent(events, "knowledge.confirmed"))
	require.Len(t, knowledgeEvents(events, "tool.completed"), 3)
}

func TestBoundedNarrativeRedactsCollapsesAndTruncates(t *testing.T) {
	clean, truncated := BoundedNarrative("line one\n\n   line two")
	require.False(t, truncated)
	require.Equal(t, "line one\n\nline two", clean)

	// Markdown narratives must keep their layout: headings, lists, tables.
	markdown := "## Report\n\n| Step | Result |\n|---|---|\n| Build | OK |\n\n- item one\n- item two"
	structured, _ := BoundedNarrative(markdown)
	require.Equal(t, markdown, structured)

	// Credential lookahead must survive a line break: bearer on one line,
	// the secret on the next.
	redactedLines, _ := BoundedNarrative("token is Bearer\nabcdef123456 done")
	require.NotContains(t, redactedLines, "abcdef123456")
	require.Contains(t, redactedLines, "[REDACTED]")
	require.Equal(t, "token is Bearer\n[REDACTED] done", redactedLines)

	redacted, _ := BoundedNarrative("token is Bearer abcdef123456")
	require.NotContains(t, redacted, "abcdef123456")
	require.Contains(t, redacted, "[REDACTED]")

	redacted, _ = BoundedNarrative("used token=abc and password=hunter2")
	require.NotContains(t, redacted, "hunter2")
	require.Contains(t, redacted, "token=[REDACTED]")
	require.Contains(t, redacted, "password=[REDACTED]")

	_, truncated = BoundedNarrative(strings.Repeat("a", narrativeMaxBytes+10))
	require.True(t, truncated)
}

func TestAgentSummaryIsDerivedDataWithFrameProvenance(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var events []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		events = append(events, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		return llm.Completion{Content: "No approval was required."}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "summary-run", Project: "repo-a", Prompt: "summarize"})
	require.NoError(t, env.GetWorkflowError())

	summaryIndex := indexEvent(events, "agent.summary")
	require.GreaterOrEqual(t, summaryIndex, 0, "agent.summary must be recorded")
	require.Greater(t, summaryIndex, indexEvent(events, "run.completed"), "agent.summary is derived data and follows run.completed")
	summary := events[summaryIndex]
	require.Equal(t, "narrative", summary.Data["kind"])
	require.Equal(t, "No approval was required.", summary.Data["answer"])
	require.Equal(t, false, summary.Data["truncated"])
	derived, ok := summary.Data["derived_from"].([]any)
	require.True(t, ok, "derived_from must be a frame list")
	require.Equal(t, []any{"summary-run/turn/01"}, derived)
	require.Equal(t, summary.Data["frame_id"], derived[0])
}

func TestModelFailureRecordsBoundedErrorDetail(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var events []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		events = append(events, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		return llm.Completion{}, errors.New("provider status 403: access denied by security policy")
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "fail-run", Project: "repo-a", Prompt: "work"})
	require.Error(t, env.GetWorkflowError())

	modelFailure := events[indexEvent(events, "model.failed")]
	require.Contains(t, modelFailure.Data["error"], "provider status 403")
	runFailure := events[indexEvent(events, "run.failed")]
	require.Contains(t, runFailure.Data["error"], "provider status 403")
	require.Less(t, indexEvent(events, "model.failed"), indexEvent(events, "run.failed"))
}

func TestToolFailureDataEffectSemantics(t *testing.T) {
	rejected := temporal.NewNonRetryableApplicationError("command must be an array of strings", "InvalidToolArguments", nil)
	data := toolFailureData("op-1", "sha256:x", "run_command", rejected)
	require.Equal(t, "none", data["effect"], "argument rejection happens before execution")

	crashed := errors.New("docker daemon gone")
	data = toolFailureData("op-2", "sha256:y", "run_command", crashed)
	require.Equal(t, "uncertain", data["effect"], "execution-boundary failures leave the effect unknown")
	require.Equal(t, "activity_failed", data["error_type"])

	timeout := temporal.NewTimeoutError(enums.TIMEOUT_TYPE_START_TO_CLOSE, nil)
	data = toolFailureData("op-3", "sha256:z", "mcp__github__create_issue", timeout)
	require.Equal(t, "uncertain", data["effect"], "timeouts after dispatch leave the effect unknown")
}

func TestMCPServerNameEnvResolution(t *testing.T) {
	t.Setenv("KERNEL_MCP_SERVER_NAME", "")
	t.Setenv("KERNEL_MCP_COMMAND", "")
	require.Empty(t, MCPServerName())

	t.Setenv("KERNEL_MCP_COMMAND", "/usr/local/bin/github-mcp-server")
	require.Equal(t, "github-mcp-server", MCPServerName())

	t.Setenv("KERNEL_MCP_COMMAND", "go")
	require.Equal(t, "go", MCPServerName())

	t.Setenv("KERNEL_MCP_SERVER_NAME", "github-prod")
	require.Equal(t, "github-prod", MCPServerName())
}

func TestMCPCallEventsCarryServerAndResultRef(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var events []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		events = append(events, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	modelCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, _ ModelRequest) (llm.Completion, error) {
		modelCalls++
		if modelCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "mcp-1", Name: "mcp__create_issue", Args: map[string]any{"title": "flaky test"}}}}, nil
		}
		return llm.Completion{Content: "issue filed"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(_ context.Context, request ToolRequest) (ToolResult, error) {
		require.Equal(t, "mcp__create_issue", request.Name)
		return ToolResult{Content: "issue created: #42"}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-mcp", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "file the issue", MaxTurns: 3, MCPServer: "github-mcp"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	started, completed := 0, 0
	for _, event := range events {
		switch event.Type {
		case "mcp.call.started":
			started++
			require.Equal(t, "github-mcp", event.Data["server"])
			require.Equal(t, "mcp__create_issue", event.Data["tool"])
			require.Equal(t, "run-mcp/turn/01/mcp-1", event.Data["operation_id"])
		case "mcp.call.completed":
			completed++
			require.Equal(t, "github-mcp", event.Data["server"])
			ref, ok := event.Data["result_ref"].(string)
			require.True(t, ok, "result_ref must be a string")
			require.Len(t, ref, len("sha256:")+64)
			require.True(t, strings.HasPrefix(ref, "sha256:"))
			require.EqualValues(t, len("issue created: #42"), event.Data["result_bytes"])
			require.Equal(t, resultContentRef("issue created: #42"), ref)
		}
	}
	require.Equal(t, 1, started)
	require.Equal(t, 1, completed)
}

func TestAgentRunDelegatesToAnotherAgent(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	// The model mock serves both runs: the child is recognized by its resolved
	// system prompt, the parent delegates once and then answers.
	sawChildAnswer := false
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if len(request.Messages) == 0 {
			t.Fatalf("unexpected model request: %#v", request)
		}
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			return llm.Completion{Content: "child done"}, nil
		}
		parentCalls++
		if parentCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "delegate-1", Name: "delegate", Args: map[string]any{"agent_id": "repo-a-reviewer", "prompt": "review the diff"}}}}, nil
		}
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "child done") {
				sawChildAnswer = true
			}
		}
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	resolveCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		resolveCalls++
		require.Equal(t, "repo-a", request.Project)
		require.Equal(t, "repo-a-reviewer", request.AgentID)
		require.Equal(t, "run-del/delegate/01", request.RunID)
		require.Equal(t, 1, request.DelegationDepth)
		require.Equal(t, "review the diff", request.Prompt)
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: "repo-a-reviewer", DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		t.Fatalf("delegate must not reach the tool activity")
		return ToolResult{}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-del", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "delegate the review", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 1, resolveCalls)
	var parentResult RunResult
	require.NoError(t, env.GetWorkflowResult(&parentResult))
	require.Equal(t, "completed", parentResult.Status)
	require.Equal(t, "parent done", parentResult.Answer)
	var delegationStarted, delegationCompleted, childRunStarted *observation.Event
	for index := range recorded {
		event := recorded[index]
		switch {
		case event.Type == "delegation.started" && delegationStarted == nil:
			delegationStarted = &recorded[index]
		case event.Type == "delegation.completed" && delegationCompleted == nil:
			delegationCompleted = &recorded[index]
		case event.Type == "run.started" && event.Context.Run == "run-del/delegate/01":
			childRunStarted = &recorded[index]
		}
	}
	require.NotNil(t, delegationStarted, "delegation.started missing")
	require.NotNil(t, delegationCompleted, "delegation.completed missing")
	require.NotNil(t, childRunStarted, "child run.started missing")
	require.Equal(t, "run-del/delegate/01", delegationStarted.Data["child_run_id"])
	require.Equal(t, "repo-a-reviewer", delegationStarted.Data["agent_id"])
	require.Equal(t, float64(1), delegationStarted.Data["ordinal"])
	require.Equal(t, float64(1), delegationStarted.Data["depth"])
	require.Equal(t, "completed", delegationCompleted.Data["child_status"])
	// Causal chain: the child run chains to the delegation event that started it.
	require.Equal(t, delegationStarted.EventID, childRunStarted.Context.ParentEventID)
	require.Equal(t, delegationStarted.Context.Run, childRunStarted.Data["parent_run_id"])
	require.True(t, sawChildAnswer, "parent must receive the child answer as the tool result")
}

func TestAgentRunDelegationDepthLimit(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	modelCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, _ ModelRequest) (llm.Completion, error) {
		modelCalls++
		if modelCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "delegate-1", Name: "delegate", Args: map[string]any{"agent_id": "repo-a-reviewer", "prompt": "review"}}}}, nil
		}
		return llm.Completion{Content: "did it myself"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(context.Context, ResolveAgentRequest) (RunInput, error) {
		t.Fatalf("resolve must not be called at the depth limit")
		return RunInput{}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-deep", Project: "repo-a", Prompt: "review", MaxTurns: 3, DelegationDepth: MaxDelegationDepth})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Equal(t, "did it myself", result.Answer)

	var depthBlocked *observation.Event
	for index := range recorded {
		if recorded[index].Type == "tool.failed" && recorded[index].Data["error_type"] == "delegation_depth_exceeded" {
			depthBlocked = &recorded[index]
			break
		}
	}
	require.NotNil(t, depthBlocked, "delegation at the depth limit must fail the tool call")
	require.Equal(t, "none", depthBlocked.Data["effect"])
	for _, event := range recorded {
		require.NotEqual(t, "delegation.started", event.Type, "no child run may start at the depth limit")
	}
}

func TestAgentRunDelegationChildFailureClassification(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	// The model mock serves both runs: the child is recognized by its resolved
	// system prompt and fails on its first model call; the parent delegates
	// twice (first child leaves an unresolved operation, second is fully
	// settled) and then answers.
	parentCalls := 0
	sawUnresolved := false
	sawSettled := false
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if len(request.Messages) == 0 {
			t.Fatalf("unexpected model request: %#v", request)
		}
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			return llm.Completion{}, fmt.Errorf("child model unavailable")
		}
		parentCalls++
		if parentCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "delegate-1", Name: "delegate", Args: map[string]any{"agent_id": "repo-a-reviewer", "prompt": "review the diff"}}}}, nil
		}
		if parentCalls == 2 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "delegate-2", Name: "delegate", Args: map[string]any{"agent_id": "repo-a-reviewer", "prompt": "review again"}}}}, nil
		}
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "unresolved") {
				sawUnresolved = true
			}
			if message.Role == "tool" && strings.Contains(message.Content, "fully recorded") {
				sawSettled = true
			}
		}
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		require.Equal(t, "repo-a-reviewer", request.AgentID)
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	summarizeCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ChildRunSummaryRequest) (ChildRunSummary, error) {
		summarizeCalls++
		require.Equal(t, "repo-a", request.Project)
		require.Contains(t, request.RunID, "/delegate/")
		if summarizeCalls == 1 {
			return ChildRunSummary{Total: 3, Unresolved: 1}, nil
		}
		return ChildRunSummary{Total: 2, Unresolved: 0}, nil
	}, activity.RegisterOptions{Name: ActivitySummarizeChildRun})

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-del-fail", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "delegate the review", MaxTurns: 6})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 2, summarizeCalls)
	var parentResult RunResult
	require.NoError(t, env.GetWorkflowResult(&parentResult))
	require.Equal(t, "completed", parentResult.Status)
	require.Equal(t, "parent done", parentResult.Answer)
	require.True(t, sawUnresolved, "the parent must see which child operations are unresolved")
	require.True(t, sawSettled, "the parent must see when all child effects are settled")

	var uncertain, occurred *observation.Event
	delegationFailures := 0
	for index := range recorded {
		event := recorded[index]
		switch {
		case event.Type == "tool.failed" && event.Data["error_type"] == "delegated_run_failed" && event.Data["effect"] == "uncertain":
			require.Nil(t, uncertain, "only the first delegation may be uncertain")
			uncertain = &recorded[index]
		case event.Type == "tool.failed" && event.Data["error_type"] == "delegated_run_failed" && event.Data["effect"] == "occurred":
			require.Nil(t, occurred, "only the second delegation may be occurred")
			occurred = &recorded[index]
		case event.Type == "delegation.failed":
			delegationFailures++
		}
	}
	require.NotNil(t, uncertain, "first child failure with unresolved ops must stay uncertain")
	require.NotNil(t, occurred, "second child failure with settled ops must be classified as occurred")
	require.Equal(t, "run-del-fail/delegate/01", uncertain.Data["child_run_id"])
	require.Equal(t, float64(3), uncertain.Data["child_ops_total"])
	require.Equal(t, float64(1), uncertain.Data["child_ops_unresolved"])
	require.Equal(t, "run-del-fail/delegate/02", occurred.Data["child_run_id"])
	require.Equal(t, float64(2), occurred.Data["child_ops_total"])
	require.Equal(t, float64(0), occurred.Data["child_ops_unresolved"])
	require.Equal(t, 2, delegationFailures)
}

func TestAgentRunParallelDelegation(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	// The parent issues three delegate calls in one model response; the three
	// children must all start before the parent awaits any of them.
	sawAnswers := map[string]bool{}
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if len(request.Messages) == 0 {
			t.Fatalf("unexpected model request: %#v", request)
		}
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			return llm.Completion{Content: "child done: " + request.Messages[1].Content}, nil
		}
		parentCalls++
		if parentCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{
				{ID: "delegate-1", Name: "delegate", Args: map[string]any{"agent_id": "repo-a-reviewer", "prompt": "part one"}},
				{ID: "delegate-2", Name: "delegate", Args: map[string]any{"agent_id": "repo-a-reviewer", "prompt": "part two"}},
				{ID: "delegate-3", Name: "delegate", Args: map[string]any{"agent_id": "repo-a-reviewer", "prompt": "part three"}},
			}}, nil
		}
		for _, message := range request.Messages {
			if message.Role != "tool" {
				continue
			}
			for _, part := range []string{"part one", "part two", "part three"} {
				if strings.Contains(message.Content, "child done: "+part) {
					sawAnswers[part] = true
				}
			}
		}
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	resolveCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		resolveCalls++
		require.Equal(t, "repo-a", request.Project)
		require.Equal(t, "repo-a-reviewer", request.AgentID)
		require.Equal(t, 1, request.DelegationDepth)
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		t.Fatalf("delegate must not reach the tool activity")
		return ToolResult{}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-par", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "split the work", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 3, resolveCalls)
	var parentResult RunResult
	require.NoError(t, env.GetWorkflowResult(&parentResult))
	require.Equal(t, "completed", parentResult.Status)
	require.Equal(t, "parent done", parentResult.Answer)

	var startedIdx, completedIdx []int
	ordinals := map[float64]string{}
	for index := range recorded {
		switch recorded[index].Type {
		case "delegation.started":
			startedIdx = append(startedIdx, index)
			ordinals[recorded[index].Data["ordinal"].(float64)] = recorded[index].Data["child_run_id"].(string)
		case "delegation.completed":
			completedIdx = append(completedIdx, index)
		}
	}
	require.Len(t, startedIdx, 3, "three children must start")
	require.Len(t, completedIdx, 3, "three children must be awaited")
	require.Equal(t, "run-par/delegate/01", ordinals[1])
	require.Equal(t, "run-par/delegate/02", ordinals[2])
	require.Equal(t, "run-par/delegate/03", ordinals[3])
	// Every child starts before the parent awaits the first one: that is the
	// concurrency guarantee parallel delegation exists for.
	for _, completed := range completedIdx {
		require.Greater(t, completed, startedIdx[len(startedIdx)-1], "all delegation.started must precede every delegation.completed")
	}
	// The turn closes only after every child outcome landed. Children emit their
	// own turn.completed into the same stream, so scope to the parent run.
	parentTurnCompleted := -1
	for index := range recorded {
		if recorded[index].Type == "turn.completed" && recorded[index].Context.Run == "run-par" {
			parentTurnCompleted = index
			break
		}
	}
	require.NotEqual(t, -1, parentTurnCompleted)
	for _, index := range startedIdx {
		require.Less(t, index, parentTurnCompleted)
	}
	for _, index := range completedIdx {
		require.Less(t, index, parentTurnCompleted)
	}
	require.True(t, sawAnswers["part one"], "parent must receive child answer for part one")
	require.True(t, sawAnswers["part two"], "parent must receive child answer for part two")
	require.True(t, sawAnswers["part three"], "parent must receive child answer for part three")
}

func TestAgentRunDelegationWidthLimit(t *testing.T) {
	if MaxDelegationWidth < 2 {
		t.Fatal("width limit test needs MaxDelegationWidth >= 2")
	}
	const delegates = 10 // MaxDelegationWidth + 2
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	toolAnswers := 0
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			return llm.Completion{Content: "child done"}, nil
		}
		parentCalls++
		if parentCalls == 1 {
			calls := make([]llm.ToolCall, 0, delegates)
			for i := 1; i <= delegates; i++ {
				calls = append(calls, llm.ToolCall{ID: fmt.Sprintf("delegate-%02d", i), Name: "delegate", Args: map[string]any{"agent_id": "repo-a-reviewer", "prompt": fmt.Sprintf("work item %02d", i)}})
			}
			return llm.Completion{ToolCalls: calls}, nil
		}
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "child done") {
				toolAnswers++
			}
		}
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		t.Fatalf("delegate must not reach the tool activity")
		return ToolResult{}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-wide", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "fan out", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, delegates, toolAnswers, "every delegated result must reach the parent")

	var startedIdx, completedIdx []int
	for index := range recorded {
		switch recorded[index].Type {
		case "delegation.started":
			startedIdx = append(startedIdx, index)
		case "delegation.completed":
			completedIdx = append(completedIdx, index)
		}
	}
	require.Len(t, startedIdx, delegates)
	require.Len(t, completedIdx, delegates)
	// The first MaxDelegationWidth children start without waiting.
	require.Less(t, startedIdx[MaxDelegationWidth-1], completedIdx[0], "the first width-limit children must start before any await")
	// Launching beyond the width limit waits for a free slot: the 9th child
	// starts only after the 1st was awaited, and the 10th after the 2nd.
	require.Greater(t, startedIdx[MaxDelegationWidth], completedIdx[0])
	require.Greater(t, startedIdx[MaxDelegationWidth+1], completedIdx[1])
	// Every await frees exactly one slot: the completions stay FIFO.
	require.Less(t, completedIdx[0], completedIdx[1])
}

func TestAgentRunDelegationWithApproval(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	sawChildAnswer := false
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			return llm.Completion{Content: "child done"}, nil
		}
		parentCalls++
		if parentCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "delegate-1", Name: "delegate", Args: map[string]any{"agent_id": "repo-a-reviewer", "prompt": "review the diff"}}}}, nil
		}
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "child done") {
				sawChildAnswer = true
			}
		}
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		require.Equal(t, "repo-a-reviewer", request.AgentID)
		require.Equal(t, "run-approve-del/delegate/01", request.RunID)
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		t.Fatalf("delegate must not reach the tool activity")
		return ToolResult{}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	// The approval signal answers the delegate operation exactly as the UI does;
	// before the parallel-delegation fix this path executed ActivityRunTool and
	// failed with `tool "delegate" is not registered`.
	args := map[string]any{"agent_id": "repo-a-reviewer", "prompt": "review the diff"}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ApprovalSignal, Approval{OperationID: "run-approve-del/turn/01/delegate-1", ArgumentsHash: operationArgumentsHash(args), Approved: true, ActorID: "lead", Reason: "go ahead"})
	}, time.Second)

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-approve-del", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "delegate the review", MaxTurns: 4, RequireToolApproval: true, ApprovalTimeoutSeconds: 5})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var parentResult RunResult
	require.NoError(t, env.GetWorkflowResult(&parentResult))
	require.Equal(t, "completed", parentResult.Status)
	require.Equal(t, "parent done", parentResult.Answer)
	require.True(t, sawChildAnswer, "the approved delegation must run and its answer must reach the parent")

	require.Less(t, indexEvent(recorded, "approval.requested"), indexEvent(recorded, "approval.granted"))
	require.Less(t, indexEvent(recorded, "approval.granted"), indexEvent(recorded, "delegation.started"))
	require.Less(t, indexEvent(recorded, "delegation.started"), indexEvent(recorded, "delegation.completed"))
	completed := indexEvent(recorded, "tool.completed")
	require.NotEqual(t, -1, completed, "the delegate call must complete as a tool outcome")
	for _, event := range recorded {
		require.NotEqual(t, "tool.blocked", event.Type, "the approved delegate call must not be blocked")
	}
}

func TestAgentRunPlanExecutesDAG(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	// The model mock serves every run: children are recognized by their resolved
	// system prompt and answer by marker; the parent issues one plan call and
	// then reads the summary. DAG: a and b in parallel, mid after both, end last.
	var childPrompts []string
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if len(request.Messages) == 0 {
			t.Fatalf("unexpected model request: %#v", request)
		}
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			prompt := request.Messages[1].Content
			childPrompts = append(childPrompts, prompt)
			switch {
			case strings.Contains(prompt, "work a"):
				return llm.Completion{Content: "A-DONE"}, nil
			case strings.Contains(prompt, "work b"):
				return llm.Completion{Content: "B-DONE"}, nil
			case strings.Contains(prompt, "merge work"):
				return llm.Completion{Content: "MID-DONE"}, nil
			case strings.Contains(prompt, "final work"):
				return llm.Completion{Content: "END-DONE"}, nil
			}
			t.Fatalf("unexpected child prompt: %s", prompt)
		}
		parentCalls++
		if parentCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "plan-1", Name: "plan", Args: map[string]any{
				"goal": "ship the feature",
				"tasks": []any{
					map[string]any{"id": "a", "agent_id": "repo-a-coder", "prompt": "work a"},
					map[string]any{"id": "b", "agent_id": "repo-a-coder", "prompt": "work b"},
					map[string]any{"id": "mid", "agent_id": "repo-a-reviewer", "prompt": "merge work", "depends_on": []any{"a", "b"}},
					map[string]any{"id": "end", "agent_id": "repo-a-qa", "prompt": "final work", "depends_on": []any{"mid"}},
				},
			}}}}, nil
		}
		sawSummary := false
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "MID-DONE") && strings.Contains(message.Content, "END-DONE") {
				sawSummary = true
			}
		}
		require.True(t, sawSummary, "the parent must receive the plan summary with every answer")
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	resolveRuns := map[string]bool{}
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		require.Equal(t, "repo-a", request.Project)
		require.Equal(t, 1, request.DelegationDepth)
		resolveRuns[request.RunID] = true
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt, MaxTurns: 4}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		t.Fatalf("plan must not reach the tool activity")
		return ToolResult{}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-plan", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "run the plan", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var parentResult RunResult
	require.NoError(t, env.GetWorkflowResult(&parentResult))
	require.Equal(t, "completed", parentResult.Status)
	require.Equal(t, "parent done", parentResult.Answer)

	// Every task is resolved up front, before any child starts.
	for _, id := range []string{"run-plan/plan/01-a", "run-plan/plan/02-b", "run-plan/plan/03-mid", "run-plan/plan/04-end"} {
		require.True(t, resolveRuns[id], "task run %s must be resolved up front", id)
	}
	// Dependent prompts are composed from upstream answers.
	var midPrompt, endPrompt string
	for _, prompt := range childPrompts {
		if strings.Contains(prompt, "merge work") {
			midPrompt = prompt
		}
		if strings.Contains(prompt, "final work") {
			endPrompt = prompt
		}
	}
	require.Contains(t, midPrompt, "Upstream results")
	require.Contains(t, midPrompt, "## a\nA-DONE")
	require.Contains(t, midPrompt, "## b\nB-DONE")
	require.Contains(t, endPrompt, "Upstream results")
	require.Contains(t, endPrompt, "## mid\nMID-DONE")

	planIdx := map[string]int{}
	taskStarted := map[string]int{}
	taskCompleted := map[string]int{}
	childRunIDs := map[string]string{"a": "run-plan/plan/01-a", "b": "run-plan/plan/02-b", "mid": "run-plan/plan/03-mid", "end": "run-plan/plan/04-end"}
	for index := range recorded {
		event := recorded[index]
		switch event.Type {
		case "plan.started", "plan.completed":
			if _, ok := planIdx[event.Type]; !ok {
				planIdx[event.Type] = index
			}
		case "plan.task.started":
			taskID := event.Data["task_id"].(string)
			taskStarted[taskID] = index
			require.Equal(t, childRunIDs[taskID], event.Data["child_run_id"])
		case "plan.task.completed":
			taskCompleted[event.Data["task_id"].(string)] = index
		}
	}
	require.Contains(t, planIdx, "plan.started")
	require.Contains(t, planIdx, "plan.completed")
	require.Len(t, taskStarted, 4)
	require.Len(t, taskCompleted, 4)
	// Ordering: the DAG gates are respected.
	require.Less(t, planIdx["plan.started"], taskStarted["a"])
	require.Less(t, taskCompleted["a"], taskStarted["mid"])
	require.Less(t, taskCompleted["b"], taskStarted["mid"])
	require.Less(t, taskCompleted["mid"], taskStarted["end"])
	require.Less(t, taskCompleted["end"], planIdx["plan.completed"])
}

func TestAgentRunPlanReworkLoop(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	// DAG: build -> gate(review_of build) -> ship. The gate rejects the first
	// build result; the rework round fixes it and the gate accepts on rerun.
	buildLaunches, gateLaunches := 0, 0
	var buildPrompts, gatePrompts []string
	var gateTools []string
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			prompt := request.Messages[1].Content
			switch {
			case strings.Contains(prompt, "build the feature"):
				buildLaunches++
				buildPrompts = append(buildPrompts, prompt)
				if buildLaunches == 1 {
					return llm.Completion{Content: "BUILD-V1"}, nil
				}
				return llm.Completion{Content: "BUILD-V2"}, nil
			case strings.Contains(prompt, "review the build"):
				gateLaunches++
				gatePrompts = append(gatePrompts, prompt)
				for _, tool := range request.Tools {
					gateTools = append(gateTools, tool.Name)
				}
				if gateLaunches == 1 {
					return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "verdict-1", Name: "submit_review", Args: map[string]any{"verdict": "rework", "feedback": map[string]any{"build": "deliver V2 instead"}, "summary": "v1 is not acceptable"}}}}, nil
				}
				return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "verdict-2", Name: "submit_review", Args: map[string]any{"verdict": "accept", "summary": "v2 passes the review"}}}}, nil
			case strings.Contains(prompt, "ship the result"):
				return llm.Completion{Content: "SHIP-DONE"}, nil
			}
			t.Fatalf("unexpected child prompt: %s", prompt)
		}
		parentCalls++
		if parentCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "plan-1", Name: "plan", Args: map[string]any{
				"goal": "ship with a gate",
				"tasks": []any{
					map[string]any{"id": "build", "agent_id": "repo-a-coder", "prompt": "build the feature"},
					map[string]any{"id": "gate", "agent_id": "repo-a-reviewer", "prompt": "review the build", "review_of": []any{"build"}},
					map[string]any{"id": "ship", "agent_id": "repo-a-qa", "prompt": "ship the result", "depends_on": []any{"gate"}},
				},
			}}}}, nil
		}
		sawSummary := false
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "BUILD-V2") && strings.Contains(message.Content, "SHIP-DONE") && strings.Contains(message.Content, "Rework history") {
				sawSummary = true
			}
		}
		require.True(t, sawSummary, "the parent must receive the summary with the rework history")
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt, MaxTurns: 4}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		t.Fatalf("plan must not reach the tool activity")
		return ToolResult{}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-rework", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "run the plan", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var parentResult RunResult
	require.NoError(t, env.GetWorkflowResult(&parentResult))
	require.Equal(t, "completed", parentResult.Status)

	require.Equal(t, 2, buildLaunches, "build must run twice: initial + rework round")
	require.Equal(t, 2, gateLaunches, "the gate must re-judge the fixed work")
	// The rework prompt carries the reviewer feedback and the previous result.
	require.Len(t, buildPrompts, 2)
	require.Contains(t, buildPrompts[1], "Rework round 2")
	require.Contains(t, buildPrompts[1], "deliver V2 instead")
	require.Contains(t, buildPrompts[1], "BUILD-V1")
	// The gate got the verdict contract and the injected tool on every run.
	for _, prompt := range gatePrompts {
		require.Contains(t, prompt, "Acceptance gate")
	}
	require.Contains(t, gateTools, "submit_review")

	started := map[string]int{}
	taskRuns := map[string][]string{}
	completed := 0
	rejected, reopened, invalidated := 0, 0, 0
	gateAcceptAt, shipStartedAt := -1, -1
	for index := range recorded {
		event := recorded[index]
		switch event.Type {
		case "plan.task.started":
			started[event.Data["task_id"].(string)]++
			taskRuns[event.Data["task_id"].(string)] = append(taskRuns[event.Data["task_id"].(string)], event.Data["child_run_id"].(string))
			if event.Data["task_id"] == "ship" {
				shipStartedAt = index
			}
		case "plan.task.completed":
			completed++
			if event.Data["task_id"] == "gate" && event.Data["verdict"] == "accept" {
				gateAcceptAt = index
			}
		case "plan.task.rejected":
			rejected++
			require.Equal(t, "build", event.Data["task_id"])
			require.Equal(t, "gate", event.Data["rejected_by"])
			require.Equal(t, "1", fmt.Sprint(event.Data["round"]))
			require.Contains(t, event.Data["feedback"], "deliver V2 instead")
		case "plan.task.reopened":
			reopened++
			require.Equal(t, "build", event.Data["task_id"])
		case "plan.task.invalidated":
			invalidated++
		case "plan.task.failed":
			t.Fatalf("no task may fail inside a bounded rework loop: %#v", event.Data)
		}
	}
	require.Equal(t, 2, started["build"])
	require.Equal(t, 2, started["gate"])
	require.Equal(t, 1, started["ship"])
	// Every launch is its own durable run: workflow ids and event scopes derive
	// from the run id, so a rework round must not reuse the previous attempt's
	// id (the append-only outbox rejects a reused id with new content).
	require.Equal(t, []string{"run-rework/plan/01-build", "run-rework/plan/01-build-r2"}, taskRuns["build"])
	require.Equal(t, []string{"run-rework/plan/02-gate", "run-rework/plan/02-gate-r2"}, taskRuns["gate"])
	require.Equal(t, []string{"run-rework/plan/03-ship"}, taskRuns["ship"])
	require.Equal(t, 5, completed) // build ×2, gate ×2 (rework + accept), ship
	require.Equal(t, 1, rejected)
	require.Equal(t, 1, reopened)
	require.Equal(t, 1, invalidated) // the gate itself while build was reopened
	require.Less(t, gateAcceptAt, shipStartedAt)
}

func TestAgentRunPlanFailureSkipsDependents(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	// DAG: a completes, b fails on its first model call, mid depends on a+b and
	// must be skipped, independent c still finishes.
	sawSummary := false
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			prompt := request.Messages[1].Content
			switch {
			case strings.Contains(prompt, "sabotage"):
				return llm.Completion{}, fmt.Errorf("child model unavailable")
			case strings.Contains(prompt, "work a"):
				return llm.Completion{Content: "A-DONE"}, nil
			case strings.Contains(prompt, "work c"):
				return llm.Completion{Content: "C-DONE"}, nil
			}
			t.Fatalf("unexpected child prompt: %s", prompt)
		}
		parentCalls++
		if parentCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "plan-1", Name: "plan", Args: map[string]any{
				"goal": "resilient plan",
				"tasks": []any{
					map[string]any{"id": "a", "agent_id": "repo-a-coder", "prompt": "work a"},
					map[string]any{"id": "b", "agent_id": "repo-a-coder", "prompt": "sabotage the build"},
					map[string]any{"id": "mid", "agent_id": "repo-a-reviewer", "prompt": "merge work", "depends_on": []any{"a", "b"}},
					map[string]any{"id": "c", "agent_id": "repo-a-qa", "prompt": "work c"},
				},
			}}}}, nil
		}
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "A-DONE") && strings.Contains(message.Content, "skipped") && strings.Contains(message.Content, "C-DONE") {
				sawSummary = true
			}
		}
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	resolveCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		resolveCalls++
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt, MaxTurns: 4}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	env.RegisterActivityWithOptions(func(_ context.Context, request ChildRunSummaryRequest) (ChildRunSummary, error) {
		require.Contains(t, request.RunID, "/plan/")
		return ChildRunSummary{Total: 0, Unresolved: 0}, nil
	}, activity.RegisterOptions{Name: ActivitySummarizeChildRun})

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-plan-fail", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "run the plan", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var parentResult RunResult
	require.NoError(t, env.GetWorkflowResult(&parentResult))
	require.Equal(t, "completed", parentResult.Status)
	require.Equal(t, "parent done", parentResult.Answer)
	require.Equal(t, 4, resolveCalls, "every task resolves up front, even the one that will be skipped")
	require.True(t, sawSummary, "the summary must carry completed, failed and skipped outcomes")

	var failed, skipped, planDone *observation.Event
	childRuns := map[string]bool{}
	for index := range recorded {
		event := recorded[index]
		switch {
		case event.Type == "run.started" && strings.HasPrefix(event.Context.Run, "run-plan-fail/plan/"):
			childRuns[event.Context.Run] = true
		case event.Type == "plan.task.failed":
			failed = &recorded[index]
		case event.Type == "plan.task.skipped":
			skipped = &recorded[index]
		case event.Type == "plan.completed":
			planDone = &recorded[index]
		}
	}
	require.NotNil(t, failed, "the failed task must be recorded")
	require.Equal(t, "b", failed.Data["task_id"])
	require.Equal(t, "run-plan-fail/plan/02-b", failed.Data["child_run_id"])
	require.Equal(t, "delegated_run_failed", failed.Data["error_type"])
	require.Equal(t, "none", failed.Data["effect"], "a child with zero operations has no effect")
	require.NotNil(t, skipped, "the dependent task must be skipped")
	require.Equal(t, "mid", skipped.Data["task_id"])
	require.Equal(t, "upstream_failed", skipped.Data["reason"])
	require.Equal(t, "b", skipped.Data["blocked_by"])
	require.NotNil(t, planDone)
	statuses, ok := planDone.Data["statuses"].(map[string]any)
	require.True(t, ok, "plan.completed must carry per-task statuses")
	require.Equal(t, "completed", statuses["a"])
	require.Equal(t, "failed", statuses["b"])
	require.Equal(t, "skipped", statuses["mid"])
	require.Equal(t, "completed", statuses["c"])
	// The skipped task never runs.
	require.False(t, childRuns["run-plan-fail/plan/03-mid"], "the skipped task must not start a child run")
	require.True(t, childRuns["run-plan-fail/plan/01-a"])
	require.True(t, childRuns["run-plan-fail/plan/02-b"])
	require.True(t, childRuns["run-plan-fail/plan/04-c"])
}

func TestAgentRunPlanWidthLimit(t *testing.T) {
	if MaxDelegationWidth < 2 {
		t.Fatal("width limit test needs MaxDelegationWidth >= 2")
	}
	const tasks = 10 // MaxDelegationWidth + 2
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			return llm.Completion{Content: "child done"}, nil
		}
		parentCalls++
		if parentCalls == 1 {
			planTasks := make([]any, 0, tasks)
			for i := 1; i <= tasks; i++ {
				planTasks = append(planTasks, map[string]any{"id": fmt.Sprintf("t%02d", i), "agent_id": "repo-a-coder", "prompt": fmt.Sprintf("work item %02d", i)})
			}
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "plan-1", Name: "plan", Args: map[string]any{"goal": "fan out", "tasks": planTasks}}}}, nil
		}
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt, MaxTurns: 4}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-plan-wide", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "fan out", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var startedIdx, completedIdx []int
	for index := range recorded {
		switch recorded[index].Type {
		case "plan.task.started":
			startedIdx = append(startedIdx, index)
		case "plan.task.completed":
			completedIdx = append(completedIdx, index)
		}
	}
	require.Len(t, startedIdx, tasks, "every task must start")
	require.Len(t, completedIdx, tasks, "every task must complete")
	// The first MaxDelegationWidth tasks start without waiting.
	require.Less(t, startedIdx[MaxDelegationWidth-1], completedIdx[0])
	// Launching beyond the width limit waits for a free slot.
	require.Greater(t, startedIdx[MaxDelegationWidth], completedIdx[0])
	// Awaits stay FIFO.
	require.Less(t, completedIdx[0], completedIdx[1])
}

func TestAgentRunPlanValidationRejectsCycle(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	sawRejection := false
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		parentCalls++
		if parentCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "plan-1", Name: "plan", Args: map[string]any{
				"tasks": []any{
					map[string]any{"id": "a", "agent_id": "repo-a-coder", "prompt": "work a", "depends_on": []any{"b"}},
					map[string]any{"id": "b", "agent_id": "repo-a-coder", "prompt": "work b", "depends_on": []any{"a"}},
				},
			}}}}, nil
		}
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "Plan rejected before execution") && strings.Contains(message.Content, "cycle") {
				sawRejection = true
			}
		}
		return llm.Completion{Content: "did it myself"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(context.Context, ResolveAgentRequest) (RunInput, error) {
		t.Fatalf("an invalid plan must not resolve any agent")
		return RunInput{}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-plan-cycle", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "run the plan", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var parentResult RunResult
	require.NoError(t, env.GetWorkflowResult(&parentResult))
	require.Equal(t, "completed", parentResult.Status)
	require.Equal(t, "did it myself", parentResult.Answer)
	require.True(t, sawRejection, "the parent must see the rejection reason")

	var rejected *observation.Event
	for index := range recorded {
		event := recorded[index]
		require.NotEqual(t, "plan.started", event.Type, "an invalid plan must not start")
		require.NotEqual(t, "plan.task.started", event.Type, "an invalid plan must not launch tasks")
		if event.Type == "tool.failed" && event.Data["error_type"] == "plan_invalid" {
			rejected = &recorded[index]
		}
	}
	require.NotNil(t, rejected, "the cycle must be rejected as plan_invalid")
	require.Equal(t, "none", rejected.Data["effect"])
	require.Equal(t, "plan", rejected.Data["tool"])
}

func TestAgentRunPlanWithApproval(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	sawChildAnswer := false
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			return llm.Completion{Content: "child done"}, nil
		}
		parentCalls++
		if parentCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "plan-1", Name: "plan", Args: map[string]any{
				"goal":  "approved work",
				"tasks": []any{map[string]any{"id": "a", "agent_id": "repo-a-coder", "prompt": "work a"}},
			}}}}, nil
		}
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "child done") {
				sawChildAnswer = true
			}
		}
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		require.Equal(t, "run-approve-plan/plan/01-a", request.RunID)
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt, MaxTurns: 4}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		t.Fatalf("plan must not reach the tool activity")
		return ToolResult{}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	args := map[string]any{
		"goal":  "approved work",
		"tasks": []any{map[string]any{"id": "a", "agent_id": "repo-a-coder", "prompt": "work a"}},
	}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ApprovalSignal, Approval{OperationID: "run-approve-plan/turn/01/plan-1", ArgumentsHash: operationArgumentsHash(args), Approved: true, ActorID: "lead", Reason: "go ahead"})
	}, time.Second)

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-approve-plan", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "run the plan", MaxTurns: 4, RequireToolApproval: true, ApprovalTimeoutSeconds: 5})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var parentResult RunResult
	require.NoError(t, env.GetWorkflowResult(&parentResult))
	require.Equal(t, "completed", parentResult.Status)
	require.Equal(t, "parent done", parentResult.Answer)
	require.True(t, sawChildAnswer, "the approved plan must run and its answers must reach the parent")

	require.Less(t, indexEvent(recorded, "approval.requested"), indexEvent(recorded, "approval.granted"))
	require.Less(t, indexEvent(recorded, "approval.granted"), indexEvent(recorded, "plan.started"))
	require.Less(t, indexEvent(recorded, "plan.started"), indexEvent(recorded, "plan.completed"))
	for _, event := range recorded {
		require.NotEqual(t, "tool.blocked", event.Type, "the approved plan must not be blocked")
	}
}

func TestAgentRunPlanTemplatesAndReplan(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	// Turn 1: plan one runs `first`. Turn 2: a re-plan references {{first.answer}}
	// as a prior answer, and a dependency placeholder {{reuse.answer}} inline.
	sawSummary := false
	parentCalls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			prompt := request.Messages[1].Content
			switch {
			case strings.Contains(prompt, "do the first step"):
				return llm.Completion{Content: "FIRST-ANSWER"}, nil
			case strings.Contains(prompt, "Summarize"):
				require.Contains(t, prompt, "FIRST-ANSWER", "prior-plan placeholder must be interpolated")
				return llm.Completion{Content: "REUSE-DONE"}, nil
			case strings.Contains(prompt, "Act on"):
				require.Contains(t, prompt, "REUSE-DONE", "dependency placeholder must be interpolated")
				require.NotContains(t, prompt, "Upstream results", "explicit placeholders suppress the auto section")
				return llm.Completion{Content: "JOINED-DONE"}, nil
			}
			t.Fatalf("unexpected child prompt: %s", prompt)
		}
		parentCalls++
		if parentCalls == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "plan-1", Name: "plan", Args: map[string]any{
				"tasks": []any{map[string]any{"id": "first", "agent_id": "repo-a-coder", "prompt": "do the first step"}},
			}}}}, nil
		}
		if parentCalls == 2 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "plan-2", Name: "plan", Args: map[string]any{
				"tasks": []any{
					map[string]any{"id": "reuse", "agent_id": "repo-a-coder", "prompt": "Summarize {{first.answer}} in one line"},
					map[string]any{"id": "joined", "agent_id": "repo-a-reviewer", "prompt": "Act on {{reuse.answer}} now", "depends_on": []any{"reuse"}},
				},
			}}}}, nil
		}
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "JOINED-DONE") {
				sawSummary = true
			}
		}
		return llm.Completion{Content: "parent done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt, MaxTurns: 4}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})

	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-plan-tmpl", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "run the plans", MaxTurns: 6})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var parentResult RunResult
	require.NoError(t, env.GetWorkflowResult(&parentResult))
	require.Equal(t, "completed", parentResult.Status)
	require.Equal(t, "parent done", parentResult.Answer)
	require.True(t, sawSummary, "the second plan summary must reach the parent")
	completedPlans := 0
	childRunIDs := map[string]bool{}
	for index := range recorded {
		if recorded[index].Type == "plan.completed" {
			completedPlans++
		}
		if recorded[index].Type == "plan.task.started" {
			childRunIDs[recorded[index].Data["child_run_id"].(string)] = true
		}
	}
	require.Equal(t, 2, completedPlans, "both plans must execute")
	require.True(t, childRunIDs["run-plan-tmpl/plan/01-first"])
	require.True(t, childRunIDs["run-plan-tmpl/plan2/01-reuse"], "the second plan call must namespace its task runs")
	require.True(t, childRunIDs["run-plan-tmpl/plan2/02-joined"])
}

func TestAgentRunExtractsKnowledgeAfterCompletion(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) {
		return []Hint{{KnowledgeID: "ext/hint", HintID: "hint-1", Proposition: "The fetch command accepts a -limit flag.", State: "confirmed"}}, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		return llm.Completion{Content: "the workspace builds and tests pass"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	newID := extractionKnowledgeID("repo-a", "The build requires a pre-existing out directory.")
	existingID := extractionKnowledgeID("repo-a", "The fetch command accepts a -limit flag, not --count.")
	registerExtraction(env, KnowledgeExtractResult{
		Candidates: []KnowledgeCandidate{
			{KnowledgeID: newID, Kind: "observation", Proposition: "The build requires a pre-existing out directory.", Evidence: []string{"run-ext/turn/01/call-1"}, Confidence: 0.9},
			{KnowledgeID: existingID, Kind: "claim", Proposition: "The fetch command accepts a -limit flag, not --count.", Evidence: []string{"run-ext/turn/01/call-1"}, Existing: true},
		},
		Duplicates: 1,
		Invalid:    2,
		HintFeedback: []HintFeedback{
			{HintID: "hint-1", KnowledgeID: "ext/hint", Proposition: "The fetch command accepts a -limit flag.", Used: true, MatchedBy: []string{"term:fetch", "term:limit"}},
			{HintID: "hint-2", KnowledgeID: "ext/other", Proposition: "The indexer skips vendored directories.", Used: false},
		},
	}, nil)
	env.RegisterActivityWithOptions(func(_ context.Context, query KnowledgeLookupQuery) (KnowledgeLookupResult, error) {
		require.Equal(t, existingID, query.KnowledgeID)
		return KnowledgeLookupResult{Exists: true, State: "proposed"}, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeLookup})

	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-ext", Project: "repo-a", Prompt: "run the build"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)

	var proposed, confirmed, started, completed, hintUsed, hintIgnored *observation.Event
	knowledgeUsedWithHint := false
	for index := range recorded {
		event := &recorded[index]
		switch event.Type {
		case "knowledge.extraction.started":
			started = event
		case "knowledge.extraction.completed":
			completed = event
		case "knowledge.proposed":
			if event.Data["producer"] == "knowledge-extractor" {
				proposed = event
			}
		case "knowledge.confirmed":
			if event.Data["extraction_id"] != nil {
				confirmed = event
			}
		case "knowledge.used":
			if event.Data["hint_id"] != nil {
				knowledgeUsedWithHint = true
			}
		case "hint.used":
			hintUsed = event
		case "hint.ignored":
			hintIgnored = event
		}
	}
	require.NotNil(t, started, "knowledge.extraction.started missing")
	require.NotNil(t, completed, "knowledge.extraction.completed missing")
	require.NotNil(t, proposed, "extraction must propose new knowledge")
	require.NotNil(t, confirmed, "extraction must strengthen existing knowledge")

	require.Equal(t, extractionIdentity("run-ext"), started.Data["extraction_id"])
	require.Equal(t, ExtractorVersion, started.Data["extractor_version"])
	require.Equal(t, newID, proposed.Data["knowledge_id"])
	require.Equal(t, "observation", proposed.Data["kind"])
	require.Equal(t, 0.9, proposed.Data["confidence"])
	require.Equal(t, "knowledge-extractor", proposed.Data["producer"])
	require.NotEmpty(t, proposed.Evidence)
	require.Equal(t, "run-ext/turn/01/call-1", proposed.Evidence[0].Ref)

	require.Equal(t, existingID, confirmed.Data["knowledge_id"])
	require.Equal(t, ExtractionReverificationRule, confirmed.Data["rule"])

	require.EqualValues(t, 2, completed.Data["candidates_count"])
	require.EqualValues(t, 1, completed.Data["proposed"])
	require.EqualValues(t, 1, completed.Data["strengthened"])
	require.EqualValues(t, 1, completed.Data["duplicates_skipped"])
	require.EqualValues(t, 2, completed.Data["invalid_skipped"])

	// Hint usage is classified from the run output, not recorded at injection time.
	require.NotNil(t, hintUsed, "hint.used missing")
	require.Equal(t, "hint-1", hintUsed.Data["hint_id"])
	require.Equal(t, "ext/hint", hintUsed.Data["knowledge_id"])
	require.Equal(t, []any{"term:fetch", "term:limit"}, hintUsed.Data["matched_by"])
	require.NotNil(t, hintIgnored, "hint.ignored missing")
	require.Equal(t, "hint-2", hintIgnored.Data["hint_id"])
	require.Equal(t, "hint-usage-lexical.v1", hintIgnored.Data["matcher"])
	require.False(t, knowledgeUsedWithHint, "injection-time knowledge.used with hint_id must not be emitted")

	// Extraction runs after the run's own terminal events.
	require.Greater(t, indexEvent(recorded, "knowledge.extraction.started"), indexEvent(recorded, "agent.summary"))
	require.Greater(t, indexEvent(recorded, "knowledge.extraction.completed"), indexEvent(recorded, "knowledge.proposed"))
}

func TestAgentRunChallengesContradictedAndAgedKnowledge(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		return llm.Completion{Content: "done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})

	staleProposition := "The fetch command accepts a -limit flag, not --count."
	newProposition := "The fetch command accepts a --count flag, not -limit."
	newID := extractionKnowledgeID("repo-a", newProposition)
	registerExtraction(env, KnowledgeExtractResult{
		Candidates: []KnowledgeCandidate{
			{KnowledgeID: newID, Kind: "observation", Proposition: newProposition, Evidence: []string{"run-ch/turn/01/call-1"}, Contradicts: "ext/stale"},
		},
		Aging: []AgingCandidate{
			{KnowledgeID: "ext/aging", Proposition: "Old unused proposal.", AgeDays: 21},
			{KnowledgeID: "ext/gone", Proposition: "Already retired.", AgeDays: 30},
		},
	}, nil)
	env.RegisterActivityWithOptions(func(_ context.Context, query KnowledgeLookupQuery) (KnowledgeLookupResult, error) {
		switch query.KnowledgeID {
		case "ext/stale":
			return KnowledgeLookupResult{Exists: true, State: "confirmed", Proposition: staleProposition}, nil
		case "ext/aging":
			return KnowledgeLookupResult{Exists: true, State: "proposed", Proposition: "Old unused proposal."}, nil
		case "ext/gone":
			return KnowledgeLookupResult{Exists: true, State: "invalidated", Proposition: "Already retired."}, nil
		}
		return KnowledgeLookupResult{}, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeLookup})

	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-ch", Project: "repo-a", Prompt: "run the fetch"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var proposedEvent *observation.Event
	challenged := map[string]*observation.Event{}
	var completed *observation.Event
	for index := range recorded {
		event := &recorded[index]
		switch event.Type {
		case "knowledge.proposed":
			if event.Data["producer"] == "knowledge-extractor" {
				proposedEvent = event
			}
		case "knowledge.challenged":
			challenged[event.Data["knowledge_id"].(string)] = event
		case extractionCompletedEvent:
			completed = event
		}
	}
	require.NotNil(t, proposedEvent, "the corrected fact must be proposed")
	require.Equal(t, newID, proposedEvent.Data["knowledge_id"])

	require.Len(t, challenged, 2, "the stale and the aging item are challenged, the terminal one is skipped")
	contradiction := challenged["ext/stale"]
	require.NotNil(t, contradiction)
	require.Equal(t, ExtractionContradictionRule, contradiction.Data["rule"])
	require.Equal(t, newID, contradiction.Data["contradicted_by"])
	require.Equal(t, staleProposition, contradiction.Data["proposition"], "the challenge must carry the target text, not a bare id")
	require.Contains(t, contradiction.Data["reason"], newProposition)
	require.Equal(t, "run-ch/turn/01/call-1", contradiction.Evidence[0].Ref)

	aging := challenged["ext/aging"]
	require.NotNil(t, aging)
	require.Equal(t, AgingRule, aging.Data["rule"])
	require.Equal(t, "Old unused proposal.", aging.Data["proposition"])
	require.Contains(t, aging.Data["reason"], "21 days")

	require.NotNil(t, completed)
	require.EqualValues(t, 1, completed.Data["challenged"])
	require.EqualValues(t, 1, completed.Data["aged"])
}

func TestAgentRunExtractionFailureDoesNotFailRun(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		return llm.Completion{Content: "done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	registerExtraction(env, KnowledgeExtractResult{}, errors.New("model returned invalid JSON"))

	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-ext-fail", Project: "repo-a", Prompt: "work"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "extraction failure must not fail the run")
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Equal(t, "done", result.Answer)

	failed := eventsOfType(recorded, "knowledge.extraction.failed")
	require.Len(t, failed, 1)
	require.Contains(t, failed[0].Data["error"], "invalid JSON")
	require.Equal(t, extractionIdentity("run-ext-fail"), failed[0].Data["extraction_id"])
	require.Empty(t, eventsOfType(recorded, "knowledge.extraction.completed"))
}

func eventsOfType(events []observation.Event, eventType string) []observation.Event {
	var matched []observation.Event
	for _, event := range events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}
