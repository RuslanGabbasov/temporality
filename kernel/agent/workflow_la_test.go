package agent

// Tests for the record-event-local gate: emit journals runtime events through
// a local activity (RecordEventLocal) instead of a queued kernel.record_event
// activity task. Fresh test-suite histories always resolve GetVersion to the
// newest version, so the DefaultVersion branch is exercised by injecting it
// into eventState directly — the same probe style as
// TestTitleAndHintsDefaultVersionKeepsSequentialOrder in workflow_perf_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
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

// emitLocalProbeInput is the RunInput shared by the emit probes below; SourceID
// keeps the observation events valid without knowledge-specific context.
func emitLocalProbeInput() RunInput {
	return RunInput{RunID: "la-hook-probe", Project: "repo-a", Prompt: "probe", SourceID: "test-source"}
}

// registerEmitLocalProbe runs a probe workflow that journals four events
// through emit with the given gate version, mirroring the production emit
// call sites: one activity-configured context, per-event bookkeeping left
// entirely to emit.
func registerEmitLocalProbe(env *testsuite.TestWorkflowEnvironment, recordLocal workflow.Version) {
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input RunInput) error {
		activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 25 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		state := &eventState{run: input, eventScope: eventScope(input.SourceID, input.Project, input.RunID), recordLocal: recordLocal}
		for i, kind := range []string{"la.first", "la.second", "la.third", "la.fourth"} {
			var data map[string]any
			if i > 0 {
				data = map[string]any{"n": i + 1}
			}
			if err := emit(activityCtx, state, kind, data); err != nil {
				return err
			}
		}
		return nil
	}, workflow.RegisterOptions{Name: "emitLocalProbe"})
}

// The queued activity transport serializes events through the data converter
// (int data becomes float64, []string becomes []any), while the local-activity
// hook receives the value in process unchanged — the assertions below accept
// both.
func dataFloat(t *testing.T, value any) float64 {
	t.Helper()
	switch v := value.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	case json.Number:
		parsed, err := v.Float64()
		require.NoError(t, err)
		return parsed
	default:
		t.Fatalf("unexpected numeric event data type %T", value)
		return 0
	}
}

func dataStringSlice(t *testing.T, value any) []string {
	t.Helper()
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			ref, ok := item.(string)
			require.True(t, ok, "caused_by entries must be strings, got %T", item)
			out = append(out, ref)
		}
		return out
	default:
		t.Fatalf("unexpected caused_by type %T", value)
		return nil
	}
}

// requireCausalChain checks emit's deterministic bookkeeping: stable event ids
// from the scope, per-event sequence, and a caused_by / parent link back to
// the immediately preceding event.
func requireCausalChain(t *testing.T, events []observation.Event) {
	t.Helper()
	scope := eventScope("test-source", "repo-a", "la-hook-probe")
	require.Len(t, events, 4)
	for i, event := range events {
		require.Equal(t, fmt.Sprintf("%s/event/%06d", scope, i+1), event.EventID)
		require.Equal(t, float64(i+1), dataFloat(t, event.Data["sequence"]))
		if i == 0 {
			_, chained := event.Data["caused_by"]
			require.False(t, chained, "the first event has no predecessor")
			require.Empty(t, event.Context.ParentEventID)
			continue
		}
		require.Equal(t, []string{events[i-1].EventID}, dataStringSlice(t, event.Data["caused_by"]))
		require.Equal(t, events[i-1].EventID, event.Context.ParentEventID)
	}
}

// Version 1 with a wired hook: events ride the local activity (the hook is
// invoked in emit order, one workflow task total), and no kernel.record_event
// activity task is scheduled.
func TestEmitLocalVersionJournalsThroughHook(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	activityCalls := newCallCounter()
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		activityCalls.bump()
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	hookEvents := newEventRecorder()
	setRecordEventLocalHook(t, func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		hookEvents.record(event)
		return nil
	})
	registerEmitLocalProbe(env, recordEventLocalVersion)
	env.ExecuteWorkflow("emitLocalProbe", emitLocalProbeInput())
	require.NoError(t, env.GetWorkflowError())
	require.Zero(t, activityCalls.count(), "gated version 1 with a wired hook must not schedule kernel.record_event activities")

	events := hookEvents.snapshot()
	require.Equal(t, []string{"la.first", "la.second", "la.third", "la.fourth"}, []string{events[0].Type, events[1].Type, events[2].Type, events[3].Type}, "the hook must receive events in emit order")
	requireCausalChain(t, events)
}

// Version 1 with a nil hook (worker bootstrapped without SetRecordEventLocal):
// emit keeps the queued kernel.record_event activity transport.
func TestEmitNilHookFallsBackToRecordEventActivity(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	recorder := newEventRecorder()
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorder.record(event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	setRecordEventLocalHook(t, nil)
	registerEmitLocalProbe(env, recordEventLocalVersion)
	env.ExecuteWorkflow("emitLocalProbe", emitLocalProbeInput())
	require.NoError(t, env.GetWorkflowError())

	requireCausalChain(t, recorder.snapshot())
}

// Replay gate: workflow.DefaultVersion — the version a replay of a pre-gate
// history receives — must keep the queued activity transport even when a hook
// happens to be wired, otherwise the recorded history would not match.
func TestEmitDefaultVersionKeepsRecordEventActivity(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	recorder := newEventRecorder()
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		recorder.record(event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	hookEvents := newEventRecorder()
	setRecordEventLocalHook(t, func(_ context.Context, event observation.Event) error {
		hookEvents.record(event)
		return nil
	})
	registerEmitLocalProbe(env, workflow.DefaultVersion)
	env.ExecuteWorkflow("emitLocalProbe", emitLocalProbeInput())
	require.NoError(t, env.GetWorkflowError())

	require.Empty(t, hookEvents.snapshot(), "the DefaultVersion branch must not touch the local transport")
	requireCausalChain(t, recorder.snapshot())
}

// End to end: a gated AgentRun journals every runtime event (run.started,
// memory.read, agent.summary, ...) through the local hook while the
// kernel.record_event activity stays unused.
func TestAgentRunJournalsEventsThroughLocalActivity(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	registerExtractionChild(env)
	activityCalls := newCallCounter()
	env.RegisterActivityWithOptions(func(context.Context, observation.Event) error {
		activityCalls.bump()
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, TitleRequest) (string, error) {
		return "Local title", nil
	}, activity.RegisterOptions{Name: ActivityGenerateTitle})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) {
		return nil, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, ModelRequest) (llm.Completion, error) {
		return llm.Completion{Content: "done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	hookEvents := newEventRecorder()
	setRecordEventLocalHook(t, func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		hookEvents.record(event)
		return nil
	})
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "la-agent-run", Project: "repo-a", Prompt: "please fix the login timeout", MaxTurns: 1})
	require.NoError(t, env.GetWorkflowError())
	require.Zero(t, activityCalls.count(), "a gated run with a wired hook must not schedule kernel.record_event activities")

	snapshot := hookEvents.snapshot()
	var started *observation.Event
	types := make([]string, 0, len(snapshot))
	for index := range snapshot {
		types = append(types, snapshot[index].Type)
		if snapshot[index].Type == "run.started" {
			started = &snapshot[index]
		}
	}
	require.Contains(t, types, "run.started")
	require.Contains(t, types, "memory.read")
	require.Contains(t, types, "agent.summary")
	require.NotNil(t, started)
	require.Equal(t, "Local title", started.Data["title"])
}

// The extraction workflow carries the same gate: its emit-based journal
// (knowledge.extraction.started / .completed) rides the local activity while
// knowledge facts (knowledge.proposed, via emitKnowledgeEvent) keep the queued
// activity transport — and the sequence/caused_by chain stays consistent
// across the two transports.
func TestKnowledgeExtractionJournalsEmitEventsThroughLocalActivity(t *testing.T) {
	env, recordedPtr := knowledgeExtractionTestEnv(t)
	registerExtraction(env, KnowledgeExtractResult{
		Candidates: []KnowledgeCandidate{
			{KnowledgeID: extractionKnowledgeID("repo-a", "The build requires a pre-existing out directory."), Kind: "observation", Proposition: "The build requires a pre-existing out directory.", Evidence: []string{"run-la/turn/01/call-1"}, Confidence: 0.9},
		},
	}, nil)
	hookEvents := newEventRecorder()
	setRecordEventLocalHook(t, func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		hookEvents.record(event)
		return nil
	})
	const parentSequence = 7
	env.ExecuteWorkflow(KnowledgeExtractionWorkflowName, extractionHandoff("run-la", "repo-a", parentSequence))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	scope := eventScope("temporality-agent-kernel", "repo-a", "run-la")
	local := hookEvents.snapshot()
	require.Len(t, local, 2)
	require.Equal(t, extractionStartedEvent, local[0].Type)
	require.Equal(t, float64(parentSequence+1), dataFloat(t, local[0].Data["sequence"]))
	require.Equal(t, []string{fmt.Sprintf("%s/event/%06d", scope, parentSequence)}, dataStringSlice(t, local[0].Data["caused_by"]))
	require.Equal(t, extractionCompletedEvent, local[1].Type)
	require.Equal(t, float64(parentSequence+3), dataFloat(t, local[1].Data["sequence"]))
	require.Equal(t, []string{fmt.Sprintf("%s/event/%06d", scope, parentSequence+2)}, dataStringSlice(t, local[1].Data["caused_by"]))

	activityEvents := recordedPtr.snapshot()
	require.Len(t, activityEvents, 1)
	require.Equal(t, "knowledge.proposed", activityEvents[0].Type)
	require.Equal(t, float64(parentSequence+2), dataFloat(t, activityEvents[0].Data["sequence"]))
	require.Equal(t, []string{fmt.Sprintf("%s/event/%06d", scope, parentSequence+1)}, dataStringSlice(t, activityEvents[0].Data["caused_by"]))
}
