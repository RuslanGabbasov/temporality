package agent

// Tests for the Phase-4 hot-path audit changes (docs/perf-audit-plan.md §4):
// the hoisted advertisedTools surface and the replay-gated parallel scheduling
// of the title and knowledge-hints startup activities. Activity mocks run on
// separate goroutines, so every shared log here is mutex-guarded like
// eventRecorder in workflow_test.go.

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// activityOrder records the mock invocation order of the startup activities
// (generate_title, knowledge_hints, call_model) and of the journaled events
// ("record:<type>" from the RecordEvent mock).
type activityOrder struct {
	mu   sync.Mutex
	step []string
}

func (o *activityOrder) record(step string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.step = append(o.step, step)
}

func (o *activityOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.step)
}

func newActivityOrder() *activityOrder { return &activityOrder{} }

// modelToolsLog captures the advertised tool surface of each call_model
// request; add returns the 1-based invocation ordinal.
type modelToolsLog struct {
	mu    sync.Mutex
	turns [][]llm.ToolDef
}

func (l *modelToolsLog) add(tools []llm.ToolDef) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turns = append(l.turns, slices.Clone(tools))
	return len(l.turns)
}

func (l *modelToolsLog) snapshot() [][]llm.ToolDef {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.turns)
}

func newModelToolsLog() *modelToolsLog { return &modelToolsLog{} }

func containsToolNamed(tools []llm.ToolDef, name string) bool {
	return slices.ContainsFunc(tools, func(tool llm.ToolDef) bool { return tool.Name == name })
}

// Version 1 (every fresh execution): a root run that needs BOTH the model
// title and knowledge hints schedules both activities up front — both mocks
// run before the first call_model and before run.started is journaled, while
// run.started still carries the generated title and the memory.read entry
// still follows it.
func TestAgentRunTitleAndHintsRunInParallelBeforeFirstModelCall(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	registerExtractionChild(env)
	recorder := newEventRecorder()
	order := newActivityOrder()
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		order.record("record:" + event.Type)
		recorder.record(event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, TitleRequest) (string, error) {
		order.record("generate_title")
		return "Parallel title", nil
	}, activity.RegisterOptions{Name: ActivityGenerateTitle})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) {
		order.record("knowledge_hints")
		return nil, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		order.record("call_model")
		return llm.Completion{Content: "done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "perf-title-parallel", Project: "repo-a", Prompt: "please fix the login timeout", MaxTurns: 1})
	require.NoError(t, env.GetWorkflowError())

	seq := order.snapshot()
	titleIdx := slices.Index(seq, "generate_title")
	hintsIdx := slices.Index(seq, "knowledge_hints")
	startedIdx := slices.Index(seq, "record:run.started")
	modelIdx := slices.Index(seq, "call_model")
	require.GreaterOrEqual(t, titleIdx, 0, "generate_title must run")
	require.GreaterOrEqual(t, hintsIdx, 0, "knowledge_hints must run")
	require.Greater(t, modelIdx, titleIdx, "title must be scheduled before the first model call")
	require.Greater(t, modelIdx, hintsIdx, "hints must be scheduled before the first model call")
	// The version-1 wait order: both activities complete before run.started is
	// journaled (the sequential path journals run.started between them).
	require.Less(t, titleIdx, startedIdx)
	require.Less(t, hintsIdx, startedIdx)
	require.Contains(t, seq, "record:memory.read")

	var started *observation.Event
	recorded := recorder.snapshot()
	for index := range recorded {
		if recorded[index].Type == "run.started" {
			started = &recorded[index]
			break
		}
	}
	require.NotNil(t, started, "run.started must be recorded")
	require.Equal(t, "Parallel title", started.Data["title"])
}

// Replay gate: workflow.DefaultVersion — the version a replay of a pre-gate
// history receives — must keep the recorded sequential command order: title
// activity, run.started, hints activity. The probe workflow calls
// runTitleAndHints directly with that version because the test environment
// always executes fresh histories, which resolve GetVersion to the newest
// version.
func TestTitleAndHintsDefaultVersionKeepsSequentialOrder(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	order := newActivityOrder()
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		order.record("record:" + event.Type)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, TitleRequest) (string, error) {
		order.record("generate_title")
		return "Sequential title", nil
	}, activity.RegisterOptions{Name: ActivityGenerateTitle})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) {
		order.record("knowledge_hints")
		return nil, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input RunInput) (string, error) {
		activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 25 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		state := &eventState{run: input, eventScope: eventScope(input.SourceID, input.Project, input.RunID)}
		return runTitleAndHints(ctx, activityCtx, state, input, map[string]any{"task_id": input.TaskID}, workflow.DefaultVersion)
	}, workflow.RegisterOptions{Name: "titleHintsSequentialProbe"})
	env.ExecuteWorkflow("titleHintsSequentialProbe", RunInput{RunID: "perf-title-sequential", Project: "repo-a", Prompt: "please fix the login timeout", SourceID: "test-source"})
	require.NoError(t, env.GetWorkflowError())
	// Exact pre-gate order: title first, run.started between the two
	// activities, hints second.
	require.Equal(t, []string{"generate_title", "record:run.started", "knowledge_hints", "record:memory.read"}, order.snapshot())
}

// The version-1 branch of runTitleAndHints itself: both activities are
// scheduled in one workflow task and both finish before run.started. Unlike
// the sequential order this interleaving is not strictly alternating, so the
// assertion checks the scheduling relations, not an exact sequence.
func TestTitleAndHintsParallelVersionSchedulesBothUpFront(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	order := newActivityOrder()
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		order.record("record:" + event.Type)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, TitleRequest) (string, error) {
		order.record("generate_title")
		return "Parallel title", nil
	}, activity.RegisterOptions{Name: ActivityGenerateTitle})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) {
		order.record("knowledge_hints")
		return nil, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input RunInput) (string, error) {
		activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 25 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		state := &eventState{run: input, eventScope: eventScope(input.SourceID, input.Project, input.RunID)}
		return runTitleAndHints(ctx, activityCtx, state, input, map[string]any{"task_id": input.TaskID}, 1)
	}, workflow.RegisterOptions{Name: "titleHintsParallelProbe"})
	env.ExecuteWorkflow("titleHintsParallelProbe", RunInput{RunID: "perf-title-parallel-probe", Project: "repo-a", Prompt: "please fix the login timeout", SourceID: "test-source"})
	require.NoError(t, env.GetWorkflowError())
	seq := order.snapshot()
	titleIdx := slices.Index(seq, "generate_title")
	hintsIdx := slices.Index(seq, "knowledge_hints")
	startedIdx := slices.Index(seq, "record:run.started")
	require.GreaterOrEqual(t, titleIdx, 0)
	require.GreaterOrEqual(t, hintsIdx, 0)
	require.Less(t, titleIdx, startedIdx, "run.started must be journaled only after both activities complete")
	require.Less(t, hintsIdx, startedIdx, "run.started must be journaled only after both activities complete")
	require.Contains(t, seq, "record:memory.read")
}

// advertisedTools is computed once before the turn loop, so every call_model
// request carries the same filtered tool surface (here: remember denied, echo
// kept) — captured per invocation by the call_model mock.
func TestAgentRunAdvertisesSameToolsOnEveryTurn(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	registerExtractionChild(env)
	env.RegisterActivityWithOptions(func(context.Context, observation.Event) error { return nil }, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, TitleRequest) (string, error) { return "", nil }, activity.RegisterOptions{Name: ActivityGenerateTitle})
	tools := newModelToolsLog()
	env.RegisterActivityWithOptions(func(_ context.Context, req ModelRequest) (llm.Completion, error) {
		if tools.add(req.Tools) == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "echo-1", Name: "echo", Args: map[string]any{"text": "hi"}}}}, nil
		}
		return llm.Completion{Content: "done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		return ToolResult{Content: "hi"}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	env.RegisterActivityWithOptions(func(context.Context, KnowledgeLookupQuery) (KnowledgeLookupResult, error) {
		return KnowledgeLookupResult{}, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeLookup})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "perf-tools-stable", Project: "repo-a", Prompt: "echo hi with the echo tool, then finish", MaxTurns: 2, DenyTools: []string{"remember"}})
	require.NoError(t, env.GetWorkflowError())

	turns := tools.snapshot()
	require.Len(t, turns, 2, "the run must make exactly two model calls")
	require.NotEmpty(t, turns[0])
	require.Equal(t, turns[0], turns[1], "the advertised tool surface must be identical on every turn")
	require.False(t, containsToolNamed(turns[0], "remember"), "denied tools must never be advertised")
	require.True(t, containsToolNamed(turns[0], "echo"), "kernel tools must stay advertised")
}
