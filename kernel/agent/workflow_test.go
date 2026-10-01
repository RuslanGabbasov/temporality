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
	require.Equal(t, "line one line two", clean)

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
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "mcp-1", Name: "mcp__github__create_issue", Args: map[string]any{"title": "flaky test"}}}}, nil
		}
		return llm.Completion{Content: "issue filed"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(_ context.Context, request ToolRequest) (ToolResult, error) {
		require.Equal(t, "mcp__github__create_issue", request.Name)
		return ToolResult{Content: "issue created: #42"}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-mcp", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "file the issue", MaxTurns: 3, MCPServer: "github-mcp"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	started, completed := 0, 0
	for _, event := range events {
		switch event.Type {
		case "mcp.call.started":
			started++
			require.Equal(t, "github-mcp", event.Data["server"])
			require.Equal(t, "mcp__github__create_issue", event.Data["tool"])
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
