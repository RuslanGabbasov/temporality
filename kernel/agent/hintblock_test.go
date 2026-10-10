package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// Prior knowledge must reach the model as ONE budgeted block with the
// knowledge_id contract — not a per-hint pile of system messages
// (docs/plan-priming-relevance.md §8).
func TestAgentRunInjectsHintsAsSingleBudgetedBlock(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	registerExtractionChild(env)
	var recorded []observation.Event
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		recorded = append(recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	var capturedHints []HintRequest
	env.RegisterActivityWithOptions(func(_ context.Context, request HintRequest) ([]Hint, error) {
		capturedHints = append(capturedHints, request)
		return []Hint{
			{KnowledgeID: "k-claim1", HintID: "hint-1", Proposition: "The gatekeeper CLI requires the --out flag to write the report", State: "confirmed", MatchedBy: []string{"entity:gatekeeper"}},
			{KnowledgeID: "auto/obs1", HintID: "hint-2", Proposition: "Report generation command exited 0 with the required flag", State: "proposed", MatchedBy: []string{"term:report"}, Caution: "unconfirmed hypothesis"},
		}, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	var captured []llm.Message
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		captured = append(captured, request.Messages...)
		return llm.Completion{Content: "done"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "run-hints", Project: "repo-a", Prompt: "write the gatekeeper report", MaxTurns: 1, Skills: []SkillRef{{ID: "skill-qa", Version: "3", Name: "qa", Digest: "Run gofmt before committing the migration files"}}})
	require.NoError(t, env.GetWorkflowError())

	// The run's skills ride along so the journal can demote hints that only
	// restate them (plan §7): novelty context, not exclusion.
	require.NotEmpty(t, capturedHints)
	require.Equal(t, []SkillRef{{ID: "skill-qa", Version: "3", Name: "qa", Digest: "Run gofmt before committing the migration files"}}, capturedHints[0].Skills)

	var hintBlocks []string
	for _, message := range captured {
		if message.Role == "system" && strings.Contains(message.Content, "Temporality prior knowledge") {
			hintBlocks = append(hintBlocks, message.Content)
		}
	}
	require.Len(t, hintBlocks, 1, "exactly one hint block must be injected")
	block := hintBlocks[0]
	require.Contains(t, block, "Temporality prior knowledge")
	require.Contains(t, block, "knowledge_id=k-claim1")
	require.Contains(t, block, "knowledge_id=auto/obs1")
	require.Contains(t, block, "match: entity:gatekeeper")
	require.Contains(t, block, "caution: unconfirmed hypothesis")

	var memoryRead *observation.Event
	for index := range recorded {
		if recorded[index].Type == "memory.read" {
			memoryRead = &recorded[index]
			break
		}
	}
	require.NotNil(t, memoryRead, "memory.read must be recorded")
	require.EqualValues(t, 2, memoryRead.Data["hint_count"])
	require.EqualValues(t, 2, memoryRead.Data["primed_hints"])
	require.EqualValues(t, 0, memoryRead.Data["dropped_hints"])
	require.Greater(t, memoryRead.Data["primed_tokens"], float64(0))
}
