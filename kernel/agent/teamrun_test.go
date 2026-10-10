package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func fixedSlot(id, agentID string) workspace.TeamSlot {
	return workspace.TeamSlot{ID: id, Title: strings.ToUpper(id[:1]) + id[1:], Binding: workspace.TeamBinding{Mode: workspace.TeamBindingFixed, AgentID: agentID}}
}

func teamBindings(slots ...workspace.TeamSlot) map[string]TeamSlotBinding {
	out := make(map[string]TeamSlotBinding, len(slots))
	for _, slot := range slots {
		out[slot.ID] = TeamSlotBinding{AgentID: slot.Binding.AgentID, Mode: "fixed"}
	}
	return out
}

func TestCompileTeamProgramProtocols(t *testing.T) {
	coder := fixedSlot("coder", "agent-coder")
	reviewer := fixedSlot("reviewer", "agent-reviewer")
	qa := fixedSlot("qa", "agent-qa")
	t.Run("pipeline chains slots in order", func(t *testing.T) {
		spec, reason := CompileTeamProgram(workspace.TeamManifest{Protocol: workspace.TeamProtocol{Kind: workspace.TeamProtocolPipeline}, Slots: []workspace.TeamSlot{coder, reviewer, qa}}, teamBindings(coder, reviewer, qa), "ship it")
		require.Empty(t, reason)
		require.Len(t, spec.Tasks, 3)
		require.Equal(t, "agent-coder", spec.Tasks[0].AgentID)
		require.Empty(t, spec.Tasks[0].DependsOn)
		require.Equal(t, []string{"coder"}, spec.Tasks[1].DependsOn)
		require.Equal(t, []string{"reviewer"}, spec.Tasks[2].DependsOn)
		require.Contains(t, spec.Tasks[0].Prompt, "ship it")
		require.Contains(t, spec.Tasks[0].Prompt, "coder")
	})
	t.Run("fan_out reduces in the last slot", func(t *testing.T) {
		spec, reason := CompileTeamProgram(workspace.TeamManifest{Protocol: workspace.TeamProtocol{Kind: workspace.TeamProtocolFanOut}, Slots: []workspace.TeamSlot{coder, reviewer, qa}}, teamBindings(coder, reviewer, qa), "research")
		require.Empty(t, reason)
		require.Len(t, spec.Tasks, 3)
		require.Empty(t, spec.Tasks[0].DependsOn)
		require.Empty(t, spec.Tasks[1].DependsOn)
		require.Equal(t, []string{"coder", "reviewer"}, spec.Tasks[2].DependsOn)
	})
	t.Run("review_gate reviews executors from the last slot", func(t *testing.T) {
		spec, reason := CompileTeamProgram(workspace.TeamManifest{Protocol: workspace.TeamProtocol{Kind: workspace.TeamProtocolReviewGate}, Slots: []workspace.TeamSlot{coder, reviewer}}, teamBindings(coder, reviewer), "deliver")
		require.Empty(t, reason)
		require.Len(t, spec.Tasks, 2)
		require.Empty(t, spec.Tasks[0].ReviewOf)
		require.Equal(t, []string{"coder"}, spec.Tasks[1].ReviewOf)
	})
	t.Run("dag uses explicit steps", func(t *testing.T) {
		manifest := workspace.TeamManifest{
			Protocol: workspace.TeamProtocol{Kind: workspace.TeamProtocolDAG, Steps: []workspace.TeamStep{
				{ID: "impl", SlotID: "coder"},
				{ID: "gate", SlotID: "reviewer", ReviewOf: []string{"impl"}},
				{ID: "verify", SlotID: "qa", DependsOn: []string{"gate"}},
			}},
			Slots: []workspace.TeamSlot{coder, reviewer, qa},
		}
		spec, reason := CompileTeamProgram(manifest, teamBindings(coder, reviewer, qa), "deliver")
		require.Empty(t, reason)
		require.Len(t, spec.Tasks, 3)
		// DAG task ids are the step ids, so ReviewOf/DependsOn reference steps.
		require.Equal(t, []string{"impl"}, spec.Tasks[1].ReviewOf)
		require.Equal(t, []string{"gate"}, spec.Tasks[2].DependsOn)
	})
	t.Run("unbound slot is rejected", func(t *testing.T) {
		// A fixed slot carries its agent in the manifest; a role slot with no
		// preferred agent and no override is the genuinely unbound case.
		roleOnly := workspace.TeamSlot{ID: "reviewer", Binding: workspace.TeamBinding{Mode: workspace.TeamBindingRole}}
		_, reason := CompileTeamProgram(workspace.TeamManifest{Protocol: workspace.TeamProtocol{Kind: workspace.TeamProtocolPipeline}, Slots: []workspace.TeamSlot{coder, roleOnly}}, map[string]TeamSlotBinding{"coder": {AgentID: "agent-coder"}}, "goal")
		require.Contains(t, reason, "reviewer")
	})
	t.Run("slot contract and responsibility land in the prompt", func(t *testing.T) {
		slot := workspace.TeamSlot{ID: "coder", Title: "Coder", Responsibility: "Implements the change", Contract: "list of changed files", Binding: workspace.TeamBinding{Mode: workspace.TeamBindingFixed, AgentID: "agent-coder"}}
		spec, reason := CompileTeamProgram(workspace.TeamManifest{Protocol: workspace.TeamProtocol{Kind: workspace.TeamProtocolPipeline}, Slots: []workspace.TeamSlot{slot}}, teamBindings(slot), "goal")
		require.Empty(t, reason)
		require.Contains(t, spec.Tasks[0].Prompt, "Implements the change")
		require.Contains(t, spec.Tasks[0].Prompt, "list of changed files")
	})
	t.Run("max_turns and max_rework defaults flow into the spec", func(t *testing.T) {
		manifest := workspace.TeamManifest{Protocol: workspace.TeamProtocol{Kind: workspace.TeamProtocolReviewGate, MaxRework: 1}, Defaults: workspace.TeamDefaults{MaxTurns: 12}, Slots: []workspace.TeamSlot{coder, reviewer}}
		spec, reason := CompileTeamProgram(manifest, teamBindings(coder, reviewer), "goal")
		require.Empty(t, reason)
		require.Equal(t, 1, spec.MaxRework)
		require.Equal(t, 12, spec.Tasks[0].MaxTurns)
	})
}

func TestTeamSlotBindings(t *testing.T) {
	fixed := fixedSlot("coder", "agent-coder")
	role := workspace.TeamSlot{ID: "reviewer", Binding: workspace.TeamBinding{Mode: workspace.TeamBindingRole, PreferredAgentID: "agent-reviewer"}}
	roleNoPref := workspace.TeamSlot{ID: "qa", Binding: workspace.TeamBinding{Mode: workspace.TeamBindingRole}}
	t.Run("fixed slot uses its agent", func(t *testing.T) {
		bindings, reason := TeamSlotBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{fixed}}, nil)
		require.Empty(t, reason)
		require.Equal(t, TeamSlotBinding{AgentID: "agent-coder", Mode: "fixed"}, bindings["coder"])
	})
	t.Run("role slot uses preferred agent", func(t *testing.T) {
		bindings, reason := TeamSlotBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{role}}, nil)
		require.Empty(t, reason)
		require.Equal(t, TeamSlotBinding{AgentID: "agent-reviewer", Mode: "matched"}, bindings["reviewer"])
	})
	t.Run("role slot without preferred or override is rejected", func(t *testing.T) {
		_, reason := TeamSlotBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{roleNoPref}}, nil)
		require.Contains(t, reason, `"qa"`)
	})
	t.Run("request binding overrides and reports matched", func(t *testing.T) {
		bindings, reason := TeamSlotBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{roleNoPref}}, map[string]string{"qa": "agent-qa"})
		require.Empty(t, reason)
		require.Equal(t, TeamSlotBinding{AgentID: "agent-qa", Mode: "matched"}, bindings["qa"])
	})
	t.Run("override equal to the fixed agent keeps fixed mode", func(t *testing.T) {
		bindings, reason := TeamSlotBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{fixed}}, map[string]string{"coder": "agent-coder"})
		require.Empty(t, reason)
		require.Equal(t, "fixed", bindings["coder"].Mode)
	})
	t.Run("unknown override slot is rejected", func(t *testing.T) {
		_, reason := TeamSlotBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{fixed}}, map[string]string{"ghost": "agent-x"})
		require.Contains(t, reason, `"ghost"`)
	})
}

// teamRunTestEnv wires the standard team workflow harness: a recorder for
// events, a child-aware model mock and a resolve mock.
type teamRunTestEnv struct {
	env      *testsuite.TestWorkflowEnvironment
	recorded []observation.Event
}

func newTeamRunTestEnv(t *testing.T) *teamRunTestEnv {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	out := &teamRunTestEnv{env: env}
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		out.recorded = append(out.recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", AgentID: request.AgentID, DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, TaskID: request.TaskID, Prompt: request.Prompt, ActorID: request.ActorID, MaxTurns: 8}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		return ToolResult{}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	return out
}

// eventsOfType returns recorded events of one type in order.
func (e *teamRunTestEnv) eventsOfType(kind string) []observation.Event {
	var out []observation.Event
	for index := range e.recorded {
		if e.recorded[index].Type == kind {
			out = append(out, e.recorded[index])
		}
	}
	return out
}

func (e *teamRunTestEnv) indexOfType(kind string) int {
	for index := range e.recorded {
		if e.recorded[index].Type == kind {
			return index
		}
	}
	return -1
}

func teamRunInput(protocol string, slots []workspace.TeamSlot, bindings map[string]TeamSlotBinding) RunInput {
	return RunInput{
		RunID:   "team-run-1",
		Project: "repo-a",
		TaskID:  "team-run-1",
		ActorID: "ops",
		Prompt:  "ship the feature",
		Team: &TeamRunInput{
			TeamID:   "code-delivery",
			TeamName: "Code Delivery",
			Version:  "1.0.0",
			Goal:     "ship the feature",
			Bindings: bindings,
			Manifest: workspace.TeamManifest{Protocol: workspace.TeamProtocol{Kind: protocol}, Slots: slots},
		},
	}
}

func TestTeamRunPipelineEventOrder(t *testing.T) {
	e := newTeamRunTestEnv(t)
	coder := workspace.TeamSlot{ID: "coder", Title: "Coder", Responsibility: "implement the feature", Contract: "changed files", Binding: workspace.TeamBinding{Mode: workspace.TeamBindingFixed, AgentID: "agent-coder"}}
	reviewer := fixedSlot("reviewer", "agent-reviewer")
	qa := fixedSlot("qa", "agent-qa")
	// Child runs answer with usage so the subtree rollup has real numbers; the
	// team root itself must not call the model at all.
	e.env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		if !strings.Contains(request.Messages[0].Content, "child-system-prompt") {
			t.Fatalf("team program root must not call the model")
		}
		prompt := request.Messages[1].Content
		// Match the slot identity marker: upstream handoffs appended to a
		// later slot's prompt mention earlier slots (e.g. the reviewer's
		// answer attributed to "reviewer"), so substring checks on role names
		// would mis-route.
		switch {
		case strings.Contains(prompt, `slot "coder"`):
			return llm.Completion{Content: "CODER-DONE", Usage: llm.Usage{TotalTokens: 100}}, nil
		case strings.Contains(prompt, `slot "reviewer"`):
			return llm.Completion{Content: "REVIEW-DONE", Usage: llm.Usage{TotalTokens: 50}}, nil
		default:
			return llm.Completion{Content: "QA-DONE", Usage: llm.Usage{TotalTokens: 25}}, nil
		}
	}, activity.RegisterOptions{Name: ActivityCallModel})
	e.env.RegisterActivityWithOptions(func(_ context.Context, request ResolveAgentRequest) (RunInput, error) {
		require.Equal(t, "repo-a", request.Project)
		require.Equal(t, 1, request.DelegationDepth)
		return RunInput{SystemPrompt: "child-system-prompt", Model: "child-model", DelegationDepth: request.DelegationDepth, RunID: request.RunID, Project: request.Project, Prompt: request.Prompt, MaxTurns: 8}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgent})
	e.env.ExecuteWorkflow("AgentRun", teamRunInput(workspace.TeamProtocolPipeline, []workspace.TeamSlot{coder, reviewer, qa}, teamBindings(coder, reviewer, qa)))
	require.True(t, e.env.IsWorkflowCompleted())
	require.NoError(t, e.env.GetWorkflowError())
	var result RunResult
	require.NoError(t, e.env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 175, result.TotalTokens)

	// Lifecycle: team.started → slot.bound ×N → plan.started → tasks →
	// plan.completed → team.completed → run.completed.
	startedAt := e.indexOfType("team.started")
	boundAt := e.indexOfType("slot.bound")
	planStartedAt := e.indexOfType("plan.started")
	planDoneAt := e.indexOfType("plan.completed")
	teamDoneAt := e.indexOfType("team.completed")
	// Child runs record their own run.completed first; the team root's is the
	// last one in the stream.
	runDoneAt := -1
	for index := range e.recorded {
		if e.recorded[index].Type == "run.completed" {
			runDoneAt = index
		}
	}
	for _, idx := range []int{startedAt, boundAt, planStartedAt, planDoneAt, teamDoneAt, runDoneAt} {
		require.GreaterOrEqual(t, idx, 0)
	}
	require.Less(t, startedAt, boundAt)
	require.Less(t, boundAt, planStartedAt)
	require.Less(t, planDoneAt, teamDoneAt)
	require.Less(t, teamDoneAt, runDoneAt)
	require.Len(t, e.eventsOfType("slot.bound"), 3)
	teamStarted := e.eventsOfType("team.started")[0]
	require.Equal(t, "code-delivery", teamStarted.Data["team_id"])
	require.Equal(t, "1.0.0", teamStarted.Data["version"])
	require.Equal(t, "ship the feature", teamStarted.Data["goal"])
	// Sequential pipeline: reviewer starts only after coder completed.
	var coderDoneAt, reviewerStartedAt int
	for index := range e.recorded {
		event := e.recorded[index]
		if event.Type == "plan.task.completed" && event.Data["task_id"] == "coder" {
			coderDoneAt = index
		}
		if event.Type == "plan.task.started" && event.Data["task_id"] == "reviewer" {
			reviewerStartedAt = index
		}
	}
	require.Less(t, coderDoneAt, reviewerStartedAt)
	// The reviewer's prompt includes the coder's answer (contract handoff).
	teamCompleted := e.eventsOfType("team.completed")[0]
	require.Equal(t, "completed", teamCompleted.Data["status"])
	require.Equal(t, float64(175), teamCompleted.Data["total_tokens"])
	rootRunCompleted := e.eventsOfType("run.completed")[len(e.eventsOfType("run.completed"))-1]
	require.Equal(t, float64(175), rootRunCompleted.Data["total_tokens"])
}

func TestTeamRunReviewGateReworkLoop(t *testing.T) {
	e := newTeamRunTestEnv(t)
	coder := workspace.TeamSlot{ID: "coder", Responsibility: "implement", Binding: workspace.TeamBinding{Mode: workspace.TeamBindingFixed, AgentID: "agent-coder"}}
	reviewer := fixedSlot("reviewer", "agent-reviewer")
	buildLaunches := 0
	e.env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		prompt := request.Messages[1].Content
		switch {
		case strings.Contains(prompt, "implement"):
			buildLaunches++
			if buildLaunches == 1 {
				return llm.Completion{Content: "CODER-V1", Usage: llm.Usage{TotalTokens: 10}}, nil
			}
			return llm.Completion{Content: "CODER-V2", Usage: llm.Usage{TotalTokens: 20}}, nil
		default:
			// Reviewer: injected submit_review verdicts.
			if buildLaunches == 1 {
				return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "v1", Name: "submit_review", Args: map[string]any{"verdict": "rework", "feedback": map[string]any{"coder": "deliver V2"}, "summary": "v1 rejected"}}}, Usage: llm.Usage{TotalTokens: 5}}, nil
			}
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "v2", Name: "submit_review", Args: map[string]any{"verdict": "accept", "summary": "v2 passes"}}}, Usage: llm.Usage{TotalTokens: 5}}, nil
		}
	}, activity.RegisterOptions{Name: ActivityCallModel})
	e.env.ExecuteWorkflow("AgentRun", teamRunInput(workspace.TeamProtocolReviewGate, []workspace.TeamSlot{coder, reviewer}, teamBindings(coder, reviewer)))
	require.True(t, e.env.IsWorkflowCompleted())
	require.NoError(t, e.env.GetWorkflowError())
	var result RunResult
	require.NoError(t, e.env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 2, buildLaunches, "coder must run twice: initial + rework")
	teamCompleted := e.eventsOfType("team.completed")
	require.Len(t, teamCompleted, 1)
	require.Equal(t, float64(1), teamCompleted[0].Data["rework_rounds"], "one rejection must surface as one rework round")
	require.Equal(t, float64(40), teamCompleted[0].Data["total_tokens"])
}

func TestTeamRunFailureEmitsTeamFailed(t *testing.T) {
	e := newTeamRunTestEnv(t)
	coder := workspace.TeamSlot{ID: "coder", Responsibility: "implement", Binding: workspace.TeamBinding{Mode: workspace.TeamBindingFixed, AgentID: "agent-coder"}}
	qa := fixedSlot("qa", "agent-qa")
	e.env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		prompt := request.Messages[1].Content
		if strings.Contains(prompt, "implement") {
			// A child workflow fails when the model activity errors.
			return llm.Completion{}, context.DeadlineExceeded
		}
		return llm.Completion{Content: "QA-DONE"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	e.env.ExecuteWorkflow("AgentRun", teamRunInput(workspace.TeamProtocolPipeline, []workspace.TeamSlot{coder, qa}, teamBindings(coder, qa)))
	require.True(t, e.env.IsWorkflowCompleted())
	require.NoError(t, e.env.GetWorkflowError())
	var result RunResult
	require.NoError(t, e.env.GetWorkflowResult(&result))
	require.Equal(t, "failed", result.Status)
	failures := e.eventsOfType("team.failed")
	require.Len(t, failures, 1)
	require.Equal(t, "coder", failures[0].Data["step"])
	require.NotEmpty(t, failures[0].Data["error"])
	require.Empty(t, e.eventsOfType("team.completed"))
	require.NotEmpty(t, e.eventsOfType("run.failed"))
	// The dependent step is honestly skipped, not silently dropped.
	skipped := e.eventsOfType("plan.task.skipped")
	require.NotEmpty(t, skipped)
}

func TestTeamRunLeadWorkers(t *testing.T) {
	e := newTeamRunTestEnv(t)
	lead := workspace.TeamSlot{ID: "lead", Title: "Lead", Responsibility: "decompose and deliver", Binding: workspace.TeamBinding{Mode: workspace.TeamBindingFixed, AgentID: "agent-lead"}}
	coder := fixedSlot("coder", "agent-coder")
	var leadPrompt string
	e.env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		leadPrompt = request.Messages[1].Content
		return llm.Completion{Content: "LEAD-DONE", Usage: llm.Usage{TotalTokens: 42}}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	e.env.ExecuteWorkflow("AgentRun", teamRunInput(workspace.TeamProtocolLeadWorkers, []workspace.TeamSlot{lead, coder}, teamBindings(lead, coder)))
	require.True(t, e.env.IsWorkflowCompleted())
	require.NoError(t, e.env.GetWorkflowError())
	var result RunResult
	require.NoError(t, e.env.GetWorkflowResult(&result))
	require.Equal(t, "completed", result.Status)
	require.Contains(t, result.Answer, "LEAD-DONE")
	require.Contains(t, leadPrompt, "agent-coder", "the roster must list the bound agents")
	require.Contains(t, leadPrompt, "ship the feature")
	require.Equal(t, 42, result.TotalTokens)
	require.Len(t, e.eventsOfType("delegation.started"), 1)
	require.Len(t, e.eventsOfType("team.completed"), 1)
}
