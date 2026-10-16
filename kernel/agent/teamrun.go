package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// TeamRunInput snapshots a team definition for one team run
// (docs/agent-teams.md §9): the manifest of the launched version is fixed in
// the workflow input, so definition changes after start cannot affect a
// running team (snapshot semantics of docs/org-structure.md §35).
type TeamRunInput struct {
	TeamID   string                     `json:"team_id"`
	TeamName string                     `json:"team_name,omitempty"`
	Version  string                     `json:"version"`
	Goal     string                     `json:"goal"`
	Bindings map[string]TeamSlotBinding `json:"bindings"`
	Manifest workspace.TeamManifest     `json:"manifest"`
}

// TeamSlotBinding is the launch-time resolution of one role slot: which agent
// fills it and how it was chosen — fixed = the manifest's fixed agent,
// matched = user override, preferred pick or automatic role matching
// (docs/agent-teams.md §7).
type TeamSlotBinding struct {
	AgentID string `json:"agent_id"`
	Mode    string `json:"mode"` // fixed | matched
}

// TeamSlotChoice explains one slot's resolution for the launch preview
// (`GET /v1/agent/teams/{id}/resolve`): who fills it, why, and — when the
// choice is ambiguous — which other candidates existed.
type TeamSlotChoice struct {
	SlotID       string   `json:"slot_id"`
	AgentID      string   `json:"agent_id,omitempty"`
	Mode         string   `json:"mode,omitempty"`         // fixed | matched
	Reason       string   `json:"reason,omitempty"`       // human-readable why
	Candidates   []string `json:"candidates,omitempty"`   // ambiguous role slots
	Requirements []string `json:"requirements,omitempty"` // unresolvable role slots
}

// TeamBindingRejection is why a team cannot start as requested. Status splits
// the two launch-time outcomes (plan wave C): 422 = no agent can ever satisfy
// the slot here (unresolvable), 409 = several agents could and the caller must
// pick one explicitly via `bindings`.
type TeamBindingRejection struct {
	Status       int      `json:"-"`
	SlotID       string   `json:"slot_id,omitempty"`
	Reason       string   `json:"reason"`
	Candidates   []string `json:"candidates,omitempty"`
	Requirements []string `json:"requirements,omitempty"`
}

func (e *TeamBindingRejection) Error() string { return e.Reason }

// ResolveTeamBindings resolves every slot at launch (docs/agent-teams.md §7,
// wave C). The roster is the caller's visible agents (already filtered to the
// project scope); matching is deterministic and modest: capabilities and
// skills must be a superset of the slot's requirements, preferred_agent_id is
// a tie-breaker — never an obligation to pick a weaker candidate.
//
// fixed slots bind their agent (which must be in the roster); role slots bind
// an explicit override, else the preferred agent among matching candidates,
// else the single matching candidate; ambiguity without a preferred pick is a
// 409 rejection listing the candidates, an empty match set is a 422 listing
// the requirements. A team never starts partially bound: the first
// unresolvable slot aborts with effect=none.
func ResolveTeamBindings(manifest workspace.TeamManifest, roster []workspace.Agent, overrides map[string]string) (map[string]TeamSlotBinding, []TeamSlotChoice, *TeamBindingRejection) {
	byID := make(map[string]workspace.Agent, len(roster))
	for _, candidate := range roster {
		byID[candidate.ID] = candidate
	}
	for slotID, agentID := range overrides {
		if strings.TrimSpace(agentID) == "" {
			continue
		}
		known := false
		for _, slot := range manifest.Slots {
			if slot.ID == slotID {
				known = true
				break
			}
		}
		if !known {
			return nil, nil, &TeamBindingRejection{Status: 422, Reason: fmt.Sprintf("bindings reference unknown slot %q", slotID)}
		}
	}
	bindings := make(map[string]TeamSlotBinding, len(manifest.Slots))
	choices := make([]TeamSlotChoice, 0, len(manifest.Slots))
	for _, slot := range manifest.Slots {
		// Explicit launch-time override wins; it must land inside the roster so
		// an invisible agent can never be smuggled into a team.
		if agentID, ok := overrides[slot.ID]; ok && strings.TrimSpace(agentID) != "" {
			agentID = strings.TrimSpace(agentID)
			if _, visible := byID[agentID]; !visible {
				return nil, choices, &TeamBindingRejection{Status: 422, SlotID: slot.ID, Reason: fmt.Sprintf("slot %q: agent %q is not available in this launch scope", slot.ID, agentID)}
			}
			mode := "matched"
			if slot.Binding.Mode == workspace.TeamBindingFixed && agentID == slot.Binding.AgentID {
				mode = "fixed"
			}
			bindings[slot.ID] = TeamSlotBinding{AgentID: agentID, Mode: mode}
			choices = append(choices, TeamSlotChoice{SlotID: slot.ID, AgentID: agentID, Mode: mode, Reason: "explicit launch binding"})
			continue
		}
		switch slot.Binding.Mode {
		case workspace.TeamBindingFixed:
			if _, visible := byID[slot.Binding.AgentID]; !visible {
				return nil, choices, &TeamBindingRejection{Status: 422, SlotID: slot.ID, Reason: fmt.Sprintf("slot %q is fixed to agent %q which is not visible in this launch scope", slot.ID, slot.Binding.AgentID)}
			}
			bindings[slot.ID] = TeamSlotBinding{AgentID: slot.Binding.AgentID, Mode: "fixed"}
			choices = append(choices, TeamSlotChoice{SlotID: slot.ID, AgentID: slot.Binding.AgentID, Mode: "fixed", Reason: "fixed binding from the team definition"})
		default: // role
			var candidates []string
			for _, candidate := range roster {
				if candidate.SatisfiesRequirements(slot.Requirements) {
					candidates = append(candidates, candidate.ID)
				}
			}
			requirements := append(append([]string{}, slot.Requirements.Capabilities...), slot.Requirements.Skills...)
			sort.Strings(candidates)
			if len(candidates) == 0 {
				choices = append(choices, TeamSlotChoice{SlotID: slot.ID, Requirements: requirements, Reason: "no visible agent satisfies the slot requirements"})
				return nil, choices, &TeamBindingRejection{
					Status:       422,
					SlotID:       slot.ID,
					Reason:       fmt.Sprintf("slot %q has a role binding but no visible agent satisfies its requirements (%s)", slot.ID, strings.Join(requirements, ", ")),
					Requirements: requirements,
				}
			}
			preferred := strings.TrimSpace(slot.Binding.PreferredAgentID)
			if len(candidates) == 1 {
				agentID := candidates[0]
				bindings[slot.ID] = TeamSlotBinding{AgentID: agentID, Mode: "matched"}
				choices = append(choices, TeamSlotChoice{SlotID: slot.ID, AgentID: agentID, Mode: "matched", Reason: "the only visible agent satisfying the slot requirements"})
				continue
			}
			if preferred != "" && contains(candidates, preferred) {
				bindings[slot.ID] = TeamSlotBinding{AgentID: preferred, Mode: "matched"}
				choices = append(choices, TeamSlotChoice{SlotID: slot.ID, AgentID: preferred, Mode: "matched", Reason: fmt.Sprintf("preferred_agent_id among %d matching candidates", len(candidates)), Candidates: candidates})
				continue
			}
			choices = append(choices, TeamSlotChoice{SlotID: slot.ID, Candidates: candidates, Reason: "several agents match; pick one explicitly in bindings"})
			return nil, choices, &TeamBindingRejection{
				Status:     409,
				SlotID:     slot.ID,
				Reason:     fmt.Sprintf("slot %q has %d matching agents (%s); pass one explicitly in bindings or set preferred_agent_id in the team definition", slot.ID, len(candidates), strings.Join(candidates, ", ")),
				Candidates: candidates,
			}
		}
	}
	return bindings, choices, nil
}

// TeamIsLeadWorkers reports whether the protocol runs as a single lead run
// instead of a compiled plan (docs/agent-teams.md §6).
func TeamIsLeadWorkers(kind string) bool {
	return kind == workspace.TeamProtocolLeadWorkers
}

// CompileTeamProgram compiles a team manifest into a PlanSpec executed by the
// kernel's plan machinery (docs/agent-teams.md §6): protocols are not a new
// engine, they are plan/delegate terms. Conventions for protocols without an
// explicit step list: pipeline chains slots in manifest order; fan_out runs
// every slot but the last in parallel and reduces in the last; review_gate
// reviews every slot but the last from the last (a reviewer of the whole
// delivery). dag uses the manifest's explicit steps. The returned string is a
// rejection reason; empty means the spec compiled.
func CompileTeamProgram(manifest workspace.TeamManifest, bindings map[string]TeamSlotBinding, goal string) (PlanSpec, string) {
	spec := PlanSpec{Goal: goal, MaxRework: manifest.Protocol.MaxRework}
	slotByID := make(map[string]workspace.TeamSlot, len(manifest.Slots))
	for _, slot := range manifest.Slots {
		slotByID[slot.ID] = slot
	}
	boundAgent := func(slotID string) (string, string) {
		if binding, ok := bindings[slotID]; ok && binding.AgentID != "" {
			return binding.AgentID, binding.Mode
		}
		if slot, ok := slotByID[slotID]; ok && slot.Binding.Mode == workspace.TeamBindingFixed {
			return slot.Binding.AgentID, "fixed"
		}
		return "", ""
	}
	task := func(id string, slot workspace.TeamSlot) (PlanTask, string) {
		agentID, _ := boundAgent(slot.ID)
		if agentID == "" {
			return PlanTask{}, fmt.Sprintf("slot %q is not bound to an agent", slot.ID)
		}
		return PlanTask{
			ID:       id,
			AgentID:  agentID,
			Prompt:   teamSlotPrompt(manifest, goal, slot),
			MaxTurns: min(manifest.Defaults.MaxTurns, MaxTurnsCeiling),
		}, ""
	}
	switch manifest.Protocol.Kind {
	case workspace.TeamProtocolPipeline:
		for index, slot := range manifest.Slots {
			t, reason := task(slot.ID, slot)
			if reason != "" {
				return spec, reason
			}
			if index > 0 {
				t.DependsOn = []string{manifest.Slots[index-1].ID}
			}
			spec.Tasks = append(spec.Tasks, t)
		}
	case workspace.TeamProtocolFanOut:
		for index, slot := range manifest.Slots {
			t, reason := task(slot.ID, slot)
			if reason != "" {
				return spec, reason
			}
			if index == len(manifest.Slots)-1 {
				// Reducer: consumes every worker's contract.
				for _, worker := range manifest.Slots[:index] {
					t.DependsOn = append(t.DependsOn, worker.ID)
				}
			}
			spec.Tasks = append(spec.Tasks, t)
		}
	case workspace.TeamProtocolReviewGate:
		reviewed := make([]string, 0, len(manifest.Slots)-1)
		for index, slot := range manifest.Slots {
			t, reason := task(slot.ID, slot)
			if reason != "" {
				return spec, reason
			}
			if index == len(manifest.Slots)-1 {
				// Reviewer: acceptance gate over every executor slot.
				t.ReviewOf = reviewed
			} else {
				reviewed = append(reviewed, slot.ID)
			}
			spec.Tasks = append(spec.Tasks, t)
		}
	case workspace.TeamProtocolDAG:
		for _, step := range manifest.Protocol.Steps {
			slot, ok := slotByID[step.SlotID]
			if !ok {
				return spec, fmt.Sprintf("step %q references unknown slot %q", step.ID, step.SlotID)
			}
			t, reason := task(step.ID, slot)
			if reason != "" {
				return spec, reason
			}
			t.DependsOn = append([]string{}, step.DependsOn...)
			t.ReviewOf = append([]string{}, step.ReviewOf...)
			spec.Tasks = append(spec.Tasks, t)
		}
	default:
		return spec, fmt.Sprintf("protocol %q does not compile to a plan", manifest.Protocol.Kind)
	}
	if len(spec.Tasks) == 0 {
		return spec, "the team program has no tasks"
	}
	return spec, ""
}

// teamSlotPrompt composes a slot's child prompt from the team snapshot:
// goal, the slot's responsibility and its output contract. Context flows
// explicitly (docs/agent-teams.md §4): upstream contracts are appended by the
// plan executor, review contracts by the acceptance-gate machinery.
func teamSlotPrompt(manifest workspace.TeamManifest, goal string, slot workspace.TeamSlot) string {
	var b strings.Builder
	b.WriteString("You work inside an agent team. Deliver only your part.\n\nTeam goal:\n")
	b.WriteString(goal)
	title := slot.Title
	if title == "" {
		title = slot.ID
	}
	fmt.Fprintf(&b, "\n\nYour role: %s (slot %q).", title, slot.ID)
	if slot.Responsibility != "" {
		b.WriteString("\nResponsibility: ")
		b.WriteString(slot.Responsibility)
	}
	if slot.Contract != "" {
		b.WriteString("\nOutput contract (downstream stages rely on exactly this): ")
		b.WriteString(slot.Contract)
	}
	if len(slot.Requirements.Capabilities) > 0 {
		b.WriteString("\nRequired capabilities: ")
		b.WriteString(strings.Join(slot.Requirements.Capabilities, ", "))
	}
	b.WriteString("\n\nFinish with the contract deliverable as your final answer.")
	return b.String()
}

// teamContextPrompt appends the shared team context to a slot prompt: the
// scratch directory every slot of this team can use for intermediate files
// and the contract files of upstream slots (the text rides in the prompt,
// files persist for humans and later stages — docs/agent-teams.md §8).
func teamContextPrompt(rootRunID string, task PlanTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nTeam scratch directory (workspace-relative, shared by every slot of this team): team/%s/ — use it for shared intermediate files.", rootRunID)
	if len(task.DependsOn) > 0 {
		b.WriteString("\nUpstream slot contracts are also persisted as files:")
		for _, dep := range task.DependsOn {
			fmt.Fprintf(&b, "\n- team/%s/contracts/%s.md", rootRunID, dep)
		}
	}
	return b.String()
}

// teamRosterPrompt composes the lead agent's prompt for lead_workers teams:
// the lead decomposes the goal itself with plan/delegate, and the bound slots
// are its fixed roster (docs/agent-teams.md §6).
func teamRosterPrompt(team TeamRunInput, lead workspace.TeamSlot) string {
	var b strings.Builder
	b.WriteString("You are the lead of an agent team. Deliver the team goal yourself or by decomposing it with your plan/delegate tools.\n\nTeam goal:\n")
	b.WriteString(team.Goal)
	title := lead.Title
	if title == "" {
		title = lead.ID
	}
	fmt.Fprintf(&b, "\n\nYour role: %s (lead slot %q).", title, lead.ID)
	if lead.Responsibility != "" {
		b.WriteString("\nResponsibility: ")
		b.WriteString(lead.Responsibility)
	}
	if len(team.Manifest.Slots) > 1 {
		b.WriteString("\n\nTeam roster (role slots and their bound agents — delegate work to exactly these agents):")
		for _, slot := range team.Manifest.Slots {
			if slot.ID == lead.ID {
				continue
			}
			name := slot.Title
			if name == "" {
				name = slot.ID
			}
			agentID := ""
			if binding, ok := team.Bindings[slot.ID]; ok {
				agentID = binding.AgentID
			}
			fmt.Fprintf(&b, "\n- %s (slot %q, agent %q)", name, slot.ID, agentID)
			if slot.Responsibility != "" {
				b.WriteString(": ")
				b.WriteString(slot.Responsibility)
			}
		}
	}
	b.WriteString("\n\nStay within your own permissions; the team does not extend them.")
	return b.String()
}

// runTeamProgram executes a team program run (docs/agent-teams.md §9): the
// root makes no model calls; the compiled protocol drives real child AgentRuns.
// Mechanical protocols (pipeline, fan_out, review_gate, dag) run through the
// same plan executor as a model-issued plan call, so every existing guarantee —
// causality, cancellation down the tree, rework gates, budgets — applies
// unchanged. lead_workers runs one lead child that decomposes the goal itself.
// run.started was already emitted by AgentRun; this path emits the team.*
// lifecycle around the program and settles the run honestly.
func runTeamProgram(ctx workflow.Context, activityCtx workflow.Context, state *eventState, input RunInput, executePlan func(llm.ToolCall, string, string, func() error) (string, *planExecution, error), subtree *subtreeTotals) (RunResult, error) {
	team := input.Team
	result := RunResult{RunID: input.RunID, Status: "running"}
	// One frame covers the whole mechanical program; child runs attach below it.
	state.frame = input.RunID + "/program"
	operationID := state.frame + "/team"
	bindingsData := make(map[string]any, len(team.Bindings))
	for slotID, binding := range team.Bindings {
		bindingsData[slotID] = binding.AgentID
	}
	if err := emit(activityCtx, state, "team.started", map[string]any{"team_id": team.TeamID, "team_name": team.TeamName, "version": team.Version, "goal": team.Goal, "bindings": bindingsData}); err != nil {
		return result, err
	}
	for _, slot := range team.Manifest.Slots {
		binding, ok := team.Bindings[slot.ID]
		if !ok || binding.AgentID == "" {
			// Should not happen: the launch API resolves every slot before start.
			return result, temporal.NewNonRetryableApplicationError(fmt.Sprintf("slot %q is not bound", slot.ID), "TeamSlotUnbound", nil)
		}
		if err := emit(activityCtx, state, "slot.bound", map[string]any{"slot_id": slot.ID, "agent_id": binding.AgentID, "mode": binding.Mode}); err != nil {
			return result, err
		}
	}
	fail := func(reason string, step string, childRunID string) (RunResult, error) {
		data := map[string]any{"team_id": team.TeamID, "version": team.Version, "error": reason, "total_tokens": subtree.tokens, "cost_usd": subtree.costUSD}
		if step != "" {
			data["step"] = step
		}
		if childRunID != "" {
			data["child_run_id"] = childRunID
		}
		if err := emit(activityCtx, state, "team.failed", data); err != nil {
			return result, err
		}
		if err := emit(activityCtx, state, "run.failed", map[string]any{"turn": 0, "error_type": "team_failed", "error": reason}); err != nil {
			return result, err
		}
		result.Status = "failed"
		result.Answer = reason
		result.TotalTokens = subtree.tokens
		result.CostUSD = subtree.costUSD
		return result, nil
	}
	complete := func(answer string, reworkRounds int) (RunResult, error) {
		if err := emit(activityCtx, state, "team.completed", map[string]any{"team_id": team.TeamID, "version": team.Version, "status": "completed", "total_tokens": subtree.tokens, "cost_usd": subtree.costUSD, "rework_rounds": reworkRounds}); err != nil {
			return result, err
		}
		result.Status = "completed"
		result.Answer = answer
		result.TotalTokens = subtree.tokens
		result.CostUSD = subtree.costUSD
		if err := emit(activityCtx, state, "run.completed", runCompletedTotals(0, result.TotalTokens, result.CostUSD)); err != nil {
			return result, err
		}
		summary, _ := BoundedNarrative(answer)
		if err := emit(activityCtx, state, "agent.summary", map[string]any{"kind": "narrative", "answer": summary, "truncated": false, "turns": 0, "derived_from": []string{state.frame}}); err != nil {
			return result, err
		}
		return result, nil
	}
	if TeamIsLeadWorkers(team.Manifest.Protocol.Kind) {
		return runTeamLead(ctx, activityCtx, state, input, subtree, fail, complete)
	}
	spec, reason := CompileTeamProgram(team.Manifest, team.Bindings, team.Goal)
	if reason != "" {
		// The launch API validates compilability; reaching here means the
		// snapshot got corrupted in transit — fail honestly, nothing started.
		return fail("team program is invalid: "+reason, "", "")
	}
	call := llm.ToolCall{ID: "team", Name: "team", Args: planSpecToArgs(spec)}
	argumentsHash := operationArgumentsHash(call.Args)
	startTool := func() error {
		return emit(activityCtx, state, "tool.started", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "team", "tool_call_id": call.ID, "arguments": compactJSON(call.Args, 500)})
	}
	content, execution, err := executePlan(call, operationID, argumentsHash, startTool)
	if err != nil {
		if ctx.Err() != nil {
			return result, err // the deferred run.cancelled emit covers cancellation
		}
		return fail("team program execution failed: "+boundedFailureDetail(err), "", "")
	}
	if execution == nil || !execution.completed() {
		step, childRunID := "", ""
		if outcome := execution.firstFailure(); outcome != nil {
			step = outcome.TaskID
			childRunID = outcome.ChildRunID
			if outcome.Error != "" {
				reason = outcome.Error
			} else if outcome.SkipReason != "" {
				reason = "step " + outcome.TaskID + " " + outcome.Status + ": " + outcome.SkipReason
			} else {
				reason = "step " + outcome.TaskID + " " + outcome.Status
			}
		}
		return fail(reason, step, childRunID)
	}
	reworkRounds := 0
	if execution != nil {
		reworkRounds = len(execution.Rejections)
	}
	return complete(content, reworkRounds)
}

// runTeamLead runs the lead_workers protocol: one lead child run that
// decomposes the goal itself with plan/delegate using the bound roster. The
// lead's final answer is the team's answer; no mechanical gates apply.
func runTeamLead(ctx workflow.Context, activityCtx workflow.Context, state *eventState, input RunInput, subtree *subtreeTotals, fail func(string, string, string) (RunResult, error), complete func(string, int) (RunResult, error)) (RunResult, error) {
	team := input.Team
	slots := team.Manifest.Slots
	lead := slots[0]
	binding := team.Bindings[lead.ID]
	prompt := teamRosterPrompt(*team, lead) + fmt.Sprintf("\n\nTeam scratch directory (workspace-relative, shared with your delegates): team/%s/ — use it for shared intermediate files.", input.RunID)
	childRunID := input.RunID + "/lead/01-" + lead.ID
	resolveCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, ScheduleToCloseTimeout: 40 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3}})
	var childInput RunInput
	depth := input.DelegationDepth + 1
	if resolveErr := workflow.ExecuteActivity(resolveCtx, ActivityResolveAgent, ResolveAgentRequest{Project: input.Project, AgentID: binding.AgentID, RunID: childRunID, TaskID: input.TaskID, Prompt: prompt, ActorID: input.ActorID, MaxTurns: team.Manifest.Defaults.MaxTurns, DelegationDepth: depth}).Get(ctx, &childInput); resolveErr != nil {
		return fail("lead agent \""+binding.AgentID+"\" could not be resolved: "+boundedFailureDetail(resolveErr), lead.ID, childRunID)
	}
	if err := emit(activityCtx, state, "delegation.started", map[string]any{"operation_id": state.frame + "/lead", "child_run_id": childRunID, "agent_id": binding.AgentID, "ordinal": 1, "depth": depth}); err != nil {
		return RunResult{RunID: input.RunID}, err
	}
	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: WorkflowID(input.SourceID, input.Project, childRunID)})
	future := workflow.ExecuteChildWorkflow(childCtx, "AgentRun", childInput)
	var childExec workflow.Execution
	if startErr := future.GetChildWorkflowExecution().Get(ctx, &childExec); startErr != nil {
		if ctx.Err() != nil {
			return RunResult{RunID: input.RunID}, startErr
		}
		return fail("lead run failed to start: "+boundedFailureDetail(startErr), lead.ID, childRunID)
	}
	childInput.ParentRunID = input.RunID
	childInput.TeamRootRunID = input.TeamRootRunID
	var child RunResult
	childErr := future.Get(ctx, &child)
	if childErr != nil {
		if ctx.Err() != nil {
			return RunResult{RunID: input.RunID}, childErr
		}
		if err := emit(activityCtx, state, "delegation.failed", map[string]any{"operation_id": state.frame + "/lead", "child_run_id": childRunID, "agent_id": binding.AgentID, "error_type": "delegated_run_failed", "error": boundedFailureDetail(childErr)}); err != nil {
			return RunResult{RunID: input.RunID}, err
		}
		return fail("lead run failed: "+boundedFailureDetail(childErr), lead.ID, childRunID)
	}
	subtree.tokens += child.TotalTokens
	subtree.costUSD += child.CostUSD
	if err := emit(activityCtx, state, "delegation.completed", map[string]any{"operation_id": state.frame + "/lead", "child_run_id": childRunID, "agent_id": binding.AgentID, "child_status": child.Status, "turns": child.Turns}); err != nil {
		return RunResult{RunID: input.RunID}, err
	}
	if child.Status != "completed" {
		return fail("lead run finished with status "+child.Status, lead.ID, childRunID)
	}
	answer, _ := BoundedNarrative(child.Answer)
	return complete("Lead agent final answer:\n\n"+answer, 0)
}
