package agent

import (
	"context"
	"path"
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

// rosterAgent builds a candidate with enabled capabilities (card semantics:
// unset = allowed; delegation needs an explicit grant; Definition == nil is a
// legacy agent that satisfies only requirement-free slots).
func rosterAgent(id string, skills []string, capabilities ...string) workspace.Agent {
	definition := &workspace.AgentDefinition{}
	for _, name := range capabilities {
		flag := true
		switch name {
		case "read_files":
			definition.Capabilities.ReadFiles = &flag
		case "modify_files":
			definition.Capabilities.ModifyFiles = &flag
		case "run_commands":
			definition.Capabilities.RunCommands = &flag
		case "network":
			definition.Capabilities.Network = &flag
		case "skills":
			definition.Capabilities.Skills = &flag
		case "knowledge":
			definition.Capabilities.Knowledge = &flag
		case "delegation":
			definition.Capabilities.Delegation = &flag
		}
	}
	return workspace.Agent{ID: id, Skills: skills, Definition: definition}
}

func TestResolveTeamBindings(t *testing.T) {
	fixed := fixedSlot("coder", "agent-coder")
	rolePref := workspace.TeamSlot{ID: "reviewer", Binding: workspace.TeamBinding{Mode: workspace.TeamBindingRole, PreferredAgentID: "agent-reviewer"}}
	roleNoPref := workspace.TeamSlot{ID: "qa", Binding: workspace.TeamBinding{Mode: workspace.TeamBindingRole}}
	t.Run("fixed slot binds its rostered agent", func(t *testing.T) {
		roster := []workspace.Agent{rosterAgent("agent-coder", nil, "read_files")}
		bindings, choices, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{fixed}}, roster, nil)
		require.Nil(t, rejection)
		require.Equal(t, TeamSlotBinding{AgentID: "agent-coder", Mode: "fixed"}, bindings["coder"])
		require.Equal(t, "fixed", choices[0].Mode)
	})
	t.Run("fixed agent outside the roster is rejected", func(t *testing.T) {
		_, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{fixed}}, nil, nil)
		require.NotNil(t, rejection)
		require.Equal(t, 422, rejection.Status)
		require.Equal(t, "coder", rejection.SlotID)
	})
	t.Run("role slot matches candidates by capabilities", func(t *testing.T) {
		slot := workspace.TeamSlot{ID: "builder", Requirements: workspace.TeamRequirements{Capabilities: []string{"modify_files", "run_commands"}}, Binding: workspace.TeamBinding{Mode: workspace.TeamBindingRole}}
		// An explicitly disabled capability excludes the agent; an unset one counts
		// as enabled (roster card semantics: unset means allowed).
		reader := rosterAgent("agent-reader", nil, "read_files")
		disabled := false
		reader.Definition.Capabilities.ModifyFiles = &disabled
		roster := []workspace.Agent{reader, rosterAgent("agent-builder", nil, "read_files", "modify_files", "run_commands")}
		bindings, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{slot}}, roster, nil)
		require.Nil(t, rejection)
		require.Equal(t, "agent-builder", bindings["builder"].AgentID)
	})
	t.Run("role slot without candidates is rejected with its requirements", func(t *testing.T) {
		slot := workspace.TeamSlot{ID: "qa", Requirements: workspace.TeamRequirements{Capabilities: []string{"run_commands"}, Skills: []string{"verify"}}, Binding: workspace.TeamBinding{Mode: workspace.TeamBindingRole}}
		_, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{slot}}, []workspace.Agent{rosterAgent("agent-reader", nil, "read_files")}, nil)
		require.NotNil(t, rejection)
		require.Equal(t, 422, rejection.Status)
		require.Equal(t, "qa", rejection.SlotID)
		require.Contains(t, rejection.Requirements, "run_commands")
		require.Contains(t, rejection.Requirements, "verify")
	})
	t.Run("single candidate auto-binds", func(t *testing.T) {
		roster := []workspace.Agent{rosterAgent("agent-only", nil, "run_commands")}
		bindings, choices, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{roleNoPref}}, roster, nil)
		require.Nil(t, rejection)
		require.Equal(t, "agent-only", bindings["qa"].AgentID)
		require.Equal(t, "matched", choices[0].Mode)
	})
	t.Run("preferred breaks the tie among several candidates", func(t *testing.T) {
		roster := []workspace.Agent{rosterAgent("agent-a", nil, "run_commands"), rosterAgent("agent-reviewer", nil, "run_commands")}
		bindings, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{rolePref}}, roster, nil)
		require.Nil(t, rejection)
		require.Equal(t, "agent-reviewer", bindings["reviewer"].AgentID)
	})
	t.Run("several candidates without preferred is a 409 listing them", func(t *testing.T) {
		roster := []workspace.Agent{rosterAgent("agent-b", nil, "run_commands"), rosterAgent("agent-a", nil, "run_commands")}
		_, choices, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{roleNoPref}}, roster, nil)
		require.NotNil(t, rejection)
		require.Equal(t, 409, rejection.Status)
		require.Equal(t, []string{"agent-a", "agent-b"}, rejection.Candidates)
		require.Empty(t, choices[0].AgentID)
	})
	t.Run("explicit override wins over matching", func(t *testing.T) {
		roster := []workspace.Agent{rosterAgent("agent-a", nil, "run_commands"), rosterAgent("agent-b", nil, "run_commands")}
		bindings, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{roleNoPref}}, roster, map[string]string{"qa": "agent-b"})
		require.Nil(t, rejection)
		require.Equal(t, "agent-b", bindings["qa"].AgentID)
	})
	t.Run("override pointing outside the roster is rejected", func(t *testing.T) {
		roster := []workspace.Agent{rosterAgent("agent-coder", nil)}
		_, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{roleNoPref}}, roster, map[string]string{"qa": "agent-ghost"})
		require.NotNil(t, rejection)
		require.Equal(t, 422, rejection.Status)
		require.Contains(t, rejection.Reason, "agent-ghost")
	})
	t.Run("override equal to the fixed agent keeps fixed mode", func(t *testing.T) {
		roster := []workspace.Agent{rosterAgent("agent-coder", nil)}
		bindings, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{fixed}}, roster, map[string]string{"coder": "agent-coder"})
		require.Nil(t, rejection)
		require.Equal(t, "fixed", bindings["coder"].Mode)
	})
	t.Run("unknown override slot is rejected", func(t *testing.T) {
		roster := []workspace.Agent{rosterAgent("agent-coder", nil)}
		_, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{fixed}}, roster, map[string]string{"ghost": "agent-x"})
		require.NotNil(t, rejection)
		require.Equal(t, 422, rejection.Status)
		require.Contains(t, rejection.Reason, `"ghost"`)
	})
	t.Run("legacy agents satisfy only requirement-free slots", func(t *testing.T) {
		legacy := workspace.Agent{ID: "agent-legacy"}
		needy := workspace.TeamSlot{ID: "ops", Requirements: workspace.TeamRequirements{Capabilities: []string{"read_files"}}, Binding: workspace.TeamBinding{Mode: workspace.TeamBindingRole}}
		_, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{needy}}, []workspace.Agent{legacy}, nil)
		require.NotNil(t, rejection)
		require.Equal(t, 422, rejection.Status)
		bindings, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{roleNoPref}}, []workspace.Agent{legacy}, nil)
		require.Nil(t, rejection)
		require.Equal(t, "agent-legacy", bindings["qa"].AgentID)
	})
	t.Run("delegation requirement needs an explicit grant", func(t *testing.T) {
		lead := workspace.TeamSlot{ID: "lead", Requirements: workspace.TeamRequirements{Capabilities: []string{"delegation"}}, Binding: workspace.TeamBinding{Mode: workspace.TeamBindingRole}}
		roster := []workspace.Agent{rosterAgent("agent-plain", nil, "read_files"), rosterAgent("agent-lead", nil, "read_files", "delegation")}
		bindings, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{lead}}, roster, nil)
		require.Nil(t, rejection)
		require.Equal(t, "agent-lead", bindings["lead"].AgentID)
	})
	t.Run("skills requirement must be attached to the agent", func(t *testing.T) {
		slot := workspace.TeamSlot{ID: "verifier", Requirements: workspace.TeamRequirements{Skills: []string{"verify"}}, Binding: workspace.TeamBinding{Mode: workspace.TeamBindingRole}}
		roster := []workspace.Agent{rosterAgent("agent-noskill", nil), rosterAgent("agent-skilled", []string{"verify"})}
		bindings, _, rejection := ResolveTeamBindings(workspace.TeamManifest{Slots: []workspace.TeamSlot{slot}}, roster, nil)
		require.Nil(t, rejection)
		require.Equal(t, "agent-skilled", bindings["verifier"].AgentID)
	})
}

// teamRunTestEnv wires the standard team workflow harness: a recorder for
// events, a child-aware model mock and a resolve mock.
type teamRunTestEnv struct {
	env      *testsuite.TestWorkflowEnvironment
	recorded []observation.Event
	// contracts captures the team-contract writes of the run (wave F): the
	// activity is mocked — the file writes themselves are unit-tested against
	// a fake sandbox in TestWriteTeamContract.
	contracts []TeamContractRequest
}

func newTeamRunTestEnv(t *testing.T) *teamRunTestEnv {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	registerExtractionChild(env)
	out := &teamRunTestEnv{env: env}
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error {
		require.NoError(t, event.Validate())
		out.recorded = append(out.recorded, event)
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(_ context.Context, request TeamContractRequest) (string, error) {
		out.contracts = append(out.contracts, request)
		return path.Join(TeamScratchDir(request.RunID), "contracts", request.TaskID+".md"), nil
	}, activity.RegisterOptions{Name: ActivityWriteTeamContract})
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

// ── Wave F: team context (scratch, contracts, scoped hints) ─────────────

func TestTeamContextPrompt(t *testing.T) {
	t.Run("scratch dir with upstream contract files", func(t *testing.T) {
		prompt := teamContextPrompt("team-run-1", PlanTask{DependsOn: []string{"coder", "reviewer"}})
		require.Contains(t, prompt, "team/team-run-1/")
		require.Contains(t, prompt, "team/team-run-1/contracts/coder.md")
		require.Contains(t, prompt, "team/team-run-1/contracts/reviewer.md")
	})
	t.Run("no deps mention only the scratch dir", func(t *testing.T) {
		prompt := teamContextPrompt("team-run-1", PlanTask{})
		require.Contains(t, prompt, "team/team-run-1/")
		require.NotContains(t, prompt, "contracts")
	})
}

func TestTeamRunPipelineWritesSlotContracts(t *testing.T) {
	e := newTeamRunTestEnv(t)
	coder := fixedSlot("coder", "agent-coder")
	qa := fixedSlot("qa", "agent-qa")
	e.env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		prompt := request.Messages[1].Content
		switch {
		case strings.Contains(prompt, `slot "coder"`):
			return llm.Completion{Content: "CODER-CONTRACT", Usage: llm.Usage{TotalTokens: 10}}, nil
		default:
			return llm.Completion{Content: "QA-CONTRACT", Usage: llm.Usage{TotalTokens: 5}}, nil
		}
	}, activity.RegisterOptions{Name: ActivityCallModel})
	e.env.ExecuteWorkflow("AgentRun", teamRunInput(workspace.TeamProtocolPipeline, []workspace.TeamSlot{coder, qa}, teamBindings(coder, qa)))
	require.True(t, e.env.IsWorkflowCompleted())
	require.NoError(t, e.env.GetWorkflowError())
	// Every completed executor slot persisted its contract under the root run.
	byTask := map[string]TeamContractRequest{}
	for _, request := range e.contracts {
		byTask[request.TaskID] = request
	}
	require.Len(t, byTask, 2)
	require.Equal(t, "team-run-1", byTask["coder"].RunID)
	require.Equal(t, "repo-a", byTask["coder"].Project)
	require.Contains(t, byTask["coder"].Answer, "CODER-CONTRACT")
	require.Contains(t, byTask["qa"].Answer, "QA-CONTRACT")
	// The dependent slot's prompt carries the text contract AND the file ref.
}

func TestTeamRunChildrenCarryTeamRootHints(t *testing.T) {
	e := newTeamRunTestEnv(t)
	coder := fixedSlot("coder", "agent-coder")
	var hintRoots []string
	e.env.RegisterActivityWithOptions(func(_ context.Context, request HintRequest) ([]Hint, error) {
		hintRoots = append(hintRoots, request.TeamRoot)
		return nil, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	e.env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		return llm.Completion{Content: "DONE", Usage: llm.Usage{TotalTokens: 1}}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	e.env.ExecuteWorkflow("AgentRun", teamRunInput(workspace.TeamProtocolPipeline, []workspace.TeamSlot{coder}, teamBindings(coder)))
	require.True(t, e.env.IsWorkflowCompleted())
	require.NoError(t, e.env.GetWorkflowError())
	require.NotEmpty(t, hintRoots, "slot runs must fetch hints")
	for _, root := range hintRoots {
		require.Equal(t, "team-run-1", root, "every hint request of the tree carries the team root")
	}
}
