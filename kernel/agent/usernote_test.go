package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// Mid-run user notes (kernel.user_note) must land in the model's conversation
// at the nearest turn boundary — the chat "send while running" path. These
// tests pin the three contract points: turn-boundary injection after a tool
// turn, reopening of a run that was about to finish, and journaled drops when
// the turn budget is spent. The first model call parks on a channel so the
// delayed signal reliably lands while the call is in flight (same mechanics
// as the nudge tests).

func newUserNoteEnv(t *testing.T, respond func(call int, request ModelRequest) llm.Completion) (*testsuite.TestWorkflowEnvironment, *[]observation.Event, *[]ModelRequest) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	registerExtractionChild(env)
	recorded := &[]observation.Event{}
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		if err := event.Validate(); err != nil {
			return err
		}
		*recorded = append(*recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	requests := &[]ModelRequest{}
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		*requests = append(*requests, request)
		return respond(len(*requests), request), nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(_ context.Context, request ToolRequest) (ToolResult, error) {
		return ToolResult{Content: "ok"}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	return env, recorded, requests
}

func eventCounts(recorded []observation.Event) map[string]int {
	counts := map[string]int{}
	for _, event := range recorded {
		counts[event.Type]++
	}
	return counts
}

// A note that arrives while turn 1's model call is in flight, where turn 1
// ends with tool calls, must be part of turn 2's prompt.
func TestUserNoteAppendedAtNextTurn(t *testing.T) {
	release := make(chan struct{})
	env, recorded, requests := newUserNoteEnv(t, func(call int, _ ModelRequest) llm.Completion {
		if call == 1 {
			<-release // park: the note must land while this call is executing
			return llm.Completion{Content: "", ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "noop"}}}
		}
		return llm.Completion{Content: "done"}
	})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(UserNoteSignal, UserNote{Text: "use postgres", ActorID: "human-1"})
	}, time.Second)
	env.RegisterDelayedCallback(func() { close(release) }, 2*time.Second)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-note-1", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "pick a database", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Len(t, *requests, 2, "turn 1 runs its tool, turn 2 consumes the note and finishes")

	second := (*requests)[1]
	last := second.Messages[len(second.Messages)-1]
	require.Equal(t, "user", last.Role, "the drained note must be the last message of turn 2")
	require.True(t, strings.HasPrefix(last.Content, "User follow-up"), "note must be framed as a follow-up, got %q", last.Content)
	require.Contains(t, last.Content, "use postgres")

	counts := eventCounts(*recorded)
	require.Equal(t, 1, counts["run.user_note"], "the drained note must be journaled exactly once")
	require.Equal(t, 0, counts["run.note_dropped"])
}

// A note that arrives while the final answer is being generated must reopen
// the run: the answer is recorded in history and another turn is spent on the
// addendum instead of returning early.
func TestUserNoteReopensFinalAnswer(t *testing.T) {
	release := make(chan struct{})
	env, _, requests := newUserNoteEnv(t, func(call int, _ ModelRequest) llm.Completion {
		if call == 1 {
			<-release
			return llm.Completion{Content: "draft answer"}
		}
		return llm.Completion{Content: "final answer with the addendum"}
	})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(UserNoteSignal, UserNote{Text: "also add retries", ActorID: "human-1"})
	}, time.Second)
	env.RegisterDelayedCallback(func() { close(release) }, 2*time.Second)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-note-2", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "write a migration", MaxTurns: 4})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Equal(t, "final answer with the addendum", result.Answer)
	require.Len(t, *requests, 2, "the would-be-final answer is reopened and followed by one more turn")

	reopened := (*requests)[1]
	require.Equal(t, "assistant", reopened.Messages[len(reopened.Messages)-2].Role, "the superseded answer stays in history")
	require.Equal(t, "draft answer", reopened.Messages[len(reopened.Messages)-2].Content)
	require.Equal(t, "user", reopened.Messages[len(reopened.Messages)-1].Role)
	require.Contains(t, reopened.Messages[len(reopened.Messages)-1].Content, "also add retries")
}

// With the turn budget spent there is no turn left to consume a note: it must
// surface as run.note_dropped, never vanish silently.
func TestUserNoteDroppedWhenBudgetExhausted(t *testing.T) {
	release := make(chan struct{})
	env, recorded, requests := newUserNoteEnv(t, func(call int, _ ModelRequest) llm.Completion {
		if call == 2 {
			<-release // park the LAST turn's call: the note lands after the final drain point
		}
		if call <= 2 {
			return llm.Completion{Content: "", ToolCalls: []llm.ToolCall{{ID: "call-" + time.Now().Format("150405.000"), Name: "noop"}}}
		}
		return llm.Completion{Content: "finale summary"}
	})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(UserNoteSignal, UserNote{Text: "one more thing", ActorID: "human-1"})
	}, time.Second)
	env.RegisterDelayedCallback(func() { close(release) }, 2*time.Second)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-note-3", Project: "repo-a", TaskID: "task-1", ActorID: "lead", Prompt: "keep working", MaxTurns: 2})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "turn_limit", result.Status)
	require.Equal(t, "finale summary", result.Answer, "the forced finale still produces a handoff")
	require.Len(t, *requests, 3, "two tool turns plus the forced finale")

	counts := eventCounts(*recorded)
	require.Equal(t, 1, counts["run.note_dropped"], "the undeliverable note must be journaled as dropped")
	require.Equal(t, 0, counts["run.user_note"])
}
