package lead_coder_reviewer_qa

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestWorkflowDelegatesToFourChildRuns(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(agent.AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	env.RegisterWorkflowWithOptions(Workflow, workflow.RegisterOptions{Name: WorkflowName})
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error { recorded = append(recorded, event); return nil }, activity.RegisterOptions{Name: agent.ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, agent.HintRequest) ([]agent.Hint, error) { return nil, nil }, activity.RegisterOptions{Name: agent.ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, agent.ModelRequest) (llm.Completion, error) {
		return llm.Completion{Content: "handoff accepted"}, nil
	}, activity.RegisterOptions{Name: agent.ActivityCallModel})
	env.ExecuteWorkflow(WorkflowName, agent.RunInput{RunID: "team-run", Project: "repo-a", Prompt: "Implement a fix", SourceID: "kernel-test"})
	require.NoError(t, env.GetWorkflowError())
	var result Result
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Len(t, result.Stages, 4)
	require.Equal(t, []string{"lead", "coder", "reviewer", "qa"}, []string{result.Stages[0].Role, result.Stages[1].Role, result.Stages[2].Role, result.Stages[3].Role})
	started, completed := 0, 0
	teamSummary, teamCompleted := -1, -1
	for index, event := range recorded {
		if event.Type == "delegation.started" {
			started++
		}
		if event.Type == "delegation.completed" {
			completed++
		}
		if event.Type == "agent.summary" && event.Context.Run == "team-run" {
			teamSummary = index
		}
		if event.Type == "run.completed" && event.Context.Run == "team-run" {
			teamCompleted = index
		}
		if strings.HasPrefix(event.Context.Run, "team-run/") {
			require.Equal(t, "team-run", event.Data["parent_run_id"])
			require.NotEmpty(t, event.Context.ParentEventID)
		}
	}
	require.Equal(t, 4, started)
	require.Equal(t, 4, completed)
	require.GreaterOrEqual(t, teamSummary, 0, "team-level agent.summary must be recorded")
	require.Greater(t, teamSummary, teamCompleted, "agent.summary is derived data and follows run.completed")
	summary := recorded[teamSummary]
	require.Equal(t, "narrative", summary.Data["kind"])
	require.Equal(t, []any{"team-run/delegation/01", "team-run/delegation/02", "team-run/delegation/03", "team-run/delegation/04"}, summary.Data["derived_from"])
	require.Equal(t, float64(4), summary.Data["stages"])
	require.Contains(t, summary.Data["answer"], "handoff accepted")
	require.Equal(t, false, summary.Data["truncated"])
}
