package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue                 = "temporality-agent-kernel"
	ActivityRecordEvent       = "kernel.record_event"
	ActivityCallModel         = "kernel.call_model"
	ActivityRunTool           = "kernel.run_tool"
	ActivityKnowledgeHints    = "kernel.knowledge_hints"
	ActivityKnowledgeLookup   = "kernel.knowledge_lookup"
	ActivityResolveAgent      = "kernel.resolve_agent"
	ActivityGenerateTitle     = "kernel.generate_run_title"
	ActivityExtractKnowledge  = "kernel.extract_knowledge"
	ActivitySummarizeChildRun = "kernel.summarize_child_run"
	ApprovalSignal            = "kernel.approval"

	// MaxDelegationDepth bounds agent-to-agent delegation chains
	// (docs/agent-delegation.md): depth 0 → 1 → 2 is allowed, a run at depth 2
	// cannot delegate further and must do the work itself.
	MaxDelegationDepth = 2
	// MaxDelegationWidth bounds how many delegated child runs of one parent
	// turn may be in flight at once (docs/agent-delegation.md, parallel
	// delegation). Additional delegate calls in the same response still run,
	// but they wait for a free slot — launched in FIFO order, awaited in FIFO
	// order, so the event stream stays deterministic for replay.
	MaxDelegationWidth = 8

	// DefaultMaxTurns is the turn budget of runs that do not configure one.
	DefaultMaxTurns = 8
	// MaxTurnsCeiling bounds explicitly configured turn budgets. Configured
	// values are honored (clamped here, never silently reset to the default):
	// team manifests already default to 40 turns per member
	// (docs/agent-teams.md), so the old ceiling of 24 truncated real budgets.
	MaxTurnsCeiling = 100
)

type RunInput struct {
	RunID                  string        `json:"run_id"`
	Project                string        `json:"project"`
	TaskID                 string        `json:"task_id"`
	ActorID                string        `json:"actor_id"`
	Prompt                 string        `json:"prompt"`
	Model                  string        `json:"model,omitempty"`
	MaxTurns               int           `json:"max_turns,omitempty"`
	Tools                  []llm.ToolDef `json:"tools,omitempty"`
	ApprovalTools          []string      `json:"approval_tools,omitempty"`
	AutoApproveTools       []string      `json:"auto_approve_tools,omitempty"`
	Role                   string        `json:"role,omitempty"`
	SystemPrompt           string        `json:"system_prompt,omitempty"`
	Skills                 []SkillRef    `json:"skills,omitempty"`
	ParentRunID            string        `json:"parent_run_id,omitempty"`
	ParentFrameID          string        `json:"parent_frame_id,omitempty"`
	ParentEventID          string        `json:"parent_event_id,omitempty"`
	SourceID               string        `json:"source_id,omitempty"`
	ApprovalTimeoutSeconds int           `json:"approval_timeout_seconds,omitempty"`
	WorkspacePath          string        `json:"workspace_path,omitempty"`
	MCPServer              string        `json:"mcp_server,omitempty"`
	// MCPServers selects registry servers for this run; empty keeps the
	// legacy env server (docs: MCP server registry).
	MCPServers []string `json:"mcp_servers,omitempty"`
	// ToolAllowlist restricts the tools offered to (and executed for) this
	// run across kernel and MCP tools; empty allows everything configured.
	ToolAllowlist []string `json:"tool_allowlist,omitempty"`
	// DenyTools removes specific tools from the advertised and executable
	// surface regardless of the allowlist — capability enforcement
	// (docs/evaluable-agent.md, plan §4.1).
	DenyTools     []string `json:"deny_tools,omitempty"`
	NetworkAccess bool     `json:"network_access,omitempty"`
	// ReadOnly mounts the workspace read-only in the command sandbox
	// (modify_files capability off, or the agent's read_only flag).
	ReadOnly bool `json:"read_only,omitempty"`
	// SkipKnowledge disables prior-knowledge hints for this run
	// (knowledge capability off).
	SkipKnowledge bool `json:"skip_knowledge,omitempty"`
	// AgentID/AgentVersion identify the structured agent definition the run
	// was launched with; the version pins the exact definition snapshot for
	// the Evolution view (docs/evaluable-agent.md §14–§15).
	AgentID      string `json:"agent_id,omitempty"`
	AgentVersion int    `json:"agent_version,omitempty"`
	// DelegationDepth counts delegation hops: 0 for a normal run, +1 per child
	// (docs/agent-delegation.md). Runs at MaxDelegationDepth must not delegate.
	DelegationDepth int `json:"delegation_depth,omitempty"`
	// ReviewOf marks a reviewer run inside a plan (docs/agent-delegation.md,
	// acceptance gates): the plan task ids this run must accept or reject via
	// the injected submit_review tool. Empty for normal runs.
	ReviewOf []string `json:"review_of,omitempty"`
	// ExecutionIdentityID names the security context of automated runs
	// (docs/org-structure.md §20): trigger fires re-check its allowed lists and
	// human_targets at every critical step. Empty for manual runs.
	ExecutionIdentityID string `json:"execution_identity_id,omitempty"`
	// Policy-derived constraints (docs/org-structure.md §24): resolved from the
	// org position at run start and enforced here, in the execution model.
	RequireToolApproval bool    `json:"require_tool_approval,omitempty"` // consequential tools need approval
	TokenBudget         int     `json:"token_budget,omitempty"`          // 0 = uncapped
	MaxBudgetUSD        float64 `json:"max_budget_usd,omitempty"`        // 0 = uncapped
	TimeoutSeconds      int     `json:"timeout_seconds,omitempty"`       // 0 = uncapped; checked at turn boundaries
	// Model prices resolved once at run-config time so the workflow can score
	// the budget without carrying the price table. Zero = no price configured;
	// a USD budget then degrades to the token cap.
	ModelPromptPricePer1k float64 `json:"model_prompt_price_per_1k,omitempty"`
	ModelCompPricePer1k   float64 `json:"model_comp_price_per_1k,omitempty"`
	// Team, when set, turns the run into a team program run (docs/agent-teams.md
	// §9): the root makes no model calls; the compiled protocol drives child
	// AgentRuns through the regular plan/delegate machinery instead.
	Team *TeamRunInput `json:"team,omitempty"`
	// DelegationWidth caps concurrent child runs of this run's plans below the
	// installation MaxDelegationWidth (team defaults.parallel_limit). 0 keeps
	// the default.
	DelegationWidth int `json:"delegation_width,omitempty"`
}

// subtreeTotals accumulates the spend of a run's child runs as their results
// land: each child reports its own subtree (own model calls plus its
// children), so the sum is the whole delegation tree of the run.
type subtreeTotals struct {
	tokens  int
	costUSD float64
}

// SkillRef is a skill resolved for a run: identity plus a budgeted digest for
// prompt injection. Execution→version linkage is recorded by the kernel before
// the workflow starts (docs/living-skills.md §13).
type SkillRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Name    string `json:"name"`
	Digest  string `json:"digest,omitempty"`
}

// pendingDelegation is a delegated child run that has been started but not
// yet awaited (docs/agent-delegation.md, parallel delegation). Launching is
// cheap; the parent only blocks when it awaits, which lets sibling delegate
// calls of one model response run concurrently.
type pendingDelegation struct {
	callID        string
	operationID   string
	argumentsHash string
	childRunID    string
	agentID       string
	future        workflow.ChildWorkflowFuture
	// duplicates are identical delegate calls in the same model response
	// (degenerate model output): they share the child's result when it lands.
	duplicates []pendingDuplicate
}

// pendingDuplicate is one duplicate tool-call ID answering a pendingDelegation.
type pendingDuplicate struct {
	callID      string
	operationID string
}

// planPendingTask is one running plan task (docs/agent-delegation.md, plans).
// Same shape as pendingDelegation, kept separate: the plan executor owns its
// own FIFO queue and does not interact with bare delegate calls of the turn.
type planPendingTask struct {
	taskID     string
	agentID    string
	childRunID string
	future     workflow.ChildWorkflowFuture
	exec       workflow.Execution
}

type RunResult struct {
	RunID  string `json:"run_id"`
	Answer string `json:"answer"`
	Turns  int    `json:"turns"`
	Status string `json:"status"`
	// TotalTokens and CostUSD roll up the whole run subtree: the run's own
	// model calls plus every delegated/planned child run. Parents and team
	// roots report honest totals without replaying journal events.
	TotalTokens int     `json:"total_tokens,omitempty"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
	// ReviewVerdict is set when a reviewer run submitted submit_review; the
	// plan executor turns rework verdicts into bounded rework rounds
	// (docs/agent-delegation.md, acceptance gates).
	ReviewVerdict *ReviewVerdict `json:"review_verdict,omitempty"`
}

type ModelRequest struct {
	Model    string        `json:"model"`
	Messages []llm.Message `json:"messages"`
	Tools    []llm.ToolDef `json:"tools"`
	// RunID + Turn enable live token streaming through the ephemeral TokenBus;
	// empty RunID keeps the blocking non-streaming path (tests, helpers).
	RunID string `json:"run_id,omitempty"`
	Turn  int    `json:"turn,omitempty"`
	// PromptCacheKey routes same-key requests to one provider prompt cache
	// entry. Empty disables the hint. See the append-only invariant on the
	// conversation history below.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

type ToolRequest struct {
	RunID         string `json:"run_id"`
	OperationID   string `json:"operation_id"`
	Name          string `json:"name"`
	Role          string `json:"role,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	Project       string `json:"project,omitempty"`
	// ActorID is the run starter; send_file resolves them as the default
	// recipient of agent-produced files.
	ActorID   string         `json:"actor_id,omitempty"`
	Arguments map[string]any `json:"arguments"`
	// AllowedTools mirrors RunInput.ToolAllowlist so the activity also
	// rejects calls to tools that were never advertised to the model.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// DeniedTools mirrors RunInput.DenyTools so the activity also rejects
	// calls to tools removed by capability enforcement.
	DeniedTools []string `json:"denied_tools,omitempty"`
	// ReadOnly mounts the workspace read-only in the command sandbox.
	ReadOnly bool `json:"read_only,omitempty"`
}

type ToolResult struct {
	Content  string                 `json:"content"`
	Evidence []observation.Evidence `json:"evidence,omitempty"`
	ExitCode *int                   `json:"exit_code,omitempty"`
}

type Approval struct {
	OperationID   string `json:"operation_id"`
	ArgumentsHash string `json:"arguments_hash"`
	Approved      bool   `json:"approved"`
	ActorID       string `json:"actor_id"`
	Reason        string `json:"reason,omitempty"`
	// Response carries the human's answer for ask_human requests: a chosen
	// option or free text. Empty for plain approvals.
	Response string `json:"response,omitempty"`
}

func AgentRun(ctx workflow.Context, input RunInput) (RunResult, error) {
	result := RunResult{RunID: input.RunID, Status: "running"}
	if input.RunID == "" || input.Project == "" || input.Prompt == "" {
		return result, temporal.NewNonRetryableApplicationError("run_id, project and prompt are required", "InvalidRunInput", nil)
	}
	if input.ActorID == "" {
		input.ActorID = "agent"
	}
	if input.SourceID == "" {
		input.SourceID = "temporality-agent-kernel"
	}
	input.MaxTurns = normalizeTurnBudget(input.MaxTurns)
	if input.ApprovalTimeoutSeconds <= 0 {
		input.ApprovalTimeoutSeconds = 3600
	}
	if input.ApprovalTimeoutSeconds > 86400 {
		input.ApprovalTimeoutSeconds = 86400
	}
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    4 * time.Minute,
		ScheduleToCloseTimeout: 5 * time.Minute,
		RetryPolicy:            &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3},
	})
	state := &eventState{run: input, sequence: 0, eventScope: eventScope(input.SourceID, input.Project, input.RunID), parentFrame: input.ParentFrameID, previousEventID: input.ParentEventID}
	var frames []string
	defer func() {
		if errors.Is(ctx.Err(), workflow.ErrCanceled) {
			cleanupCtx, _ := workflow.NewDisconnectedContext(ctx)
			cleanupCtx = workflow.WithActivityOptions(cleanupCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 5}})
			_ = emit(cleanupCtx, state, "run.cancelled", nil)
		}
	}()
	started := map[string]any{"task_id": input.TaskID, "role": input.Role, "parent_run_id": input.ParentRunID}
	if input.AgentID != "" {
		started["agent_id"] = input.AgentID
	}
	if input.AgentVersion > 0 {
		started["agent_version"] = input.AgentVersion
	}
	if input.ParentRunID == "" {
		// Root runs get a short human title for the Runs list; delegated child
		// runs are shown inside their parent. Best effort only: on any failure
		// the UI falls back to the run ID, so this must not fail the run.
		titleCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 25 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		var title string
		if err := workflow.ExecuteActivity(titleCtx, ActivityGenerateTitle, TitleRequest{Prompt: input.Prompt}).Get(ctx, &title); err == nil && strings.TrimSpace(title) != "" {
			started["title"] = strings.TrimSpace(title)
		}
	}
	if err := emit(activityCtx, state, "run.started", started); err != nil {
		return result, err
	}
	var priorHints []Hint
	var hintBlock string
	// A team program run has no model turns at the root, so prior-knowledge
	// hints would never be consumed (docs/agent-teams.md §9).
	if input.Team == nil && !input.SkipKnowledge {
		hintsCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		if err := workflow.ExecuteActivity(hintsCtx, ActivityKnowledgeHints, HintRequest{Project: input.Project, RunID: input.RunID, TaskID: input.TaskID, ActorID: input.ActorID, Query: input.Prompt}).Get(ctx, &priorHints); err != nil {
			// Retrieval is best-effort, but the miss is still part of the trajectory.
			if eventErr := emit(activityCtx, state, "memory.read.failed", map[string]any{"error_type": "activation_unavailable"}); eventErr != nil {
				return result, eventErr
			}
			priorHints = nil
		} else {
			// One budgeted block instead of per-hint messages: the model sees a
			// compact cue list with the knowledge_id contract, not a wall of prose.
			rendered := observation.RenderHintBlock(toObservationHints(priorHints), observation.DefaultRenderOptions())
			hintBlock = rendered.Block
			if err := emit(activityCtx, state, "memory.read", map[string]any{"hint_count": len(priorHints), "primed_hints": rendered.Kept, "primed_tokens": rendered.Tokens, "dropped_hints": rendered.Dropped}); err != nil {
				return result, err
			}
		}
	}
	systemPrompt := systemPromptForRole(input.Role)
	if input.SystemPrompt != "" {
		systemPrompt = input.SystemPrompt
	}
	if skillSection := skillsPromptSection(input.Skills); skillSection != "" {
		systemPrompt += "\n\n" + skillSection
	}
	messages := []llm.Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: input.Prompt}}
	if hintBlock != "" {
		messages = append(messages, llm.Message{Role: "system", Content: hintBlock})
	}
	// Hint usage is NOT recorded here: injection is an offer, not a use. The
	// honest used/ignored signal is derived from the run's own output after it
	// finishes (see runKnowledgeExtraction) so reuse accounting and aging track
	// actual influence, not retrieval luck.
	delegations := 0
	// Plan calls in this run, for unique child run ids across re-plans.
	plans := 0
	// Acceptance-gate verdict of this run (docs/agent-delegation.md): set when
	// a reviewer task calls submit_review; the run finishes at the end of that
	// turn. Nil for normal runs — the plan executor treats that as accept.
	var reviewVerdict *ReviewVerdict
	reviewSubmitted := false
	// planAnswers carries bounded answers of completed plan tasks across plans
	// of the same run: a later plan (re-plan of the tail) can reference them via
	// {{task-id.answer}} placeholders without re-running the work.
	planAnswers := map[string]string{}
	// Policy accounting (docs/org-structure.md §24): limits are resolved from
	// the org position at run start; the workflow enforces them at turn
	// boundaries and stops the run with a forced finale when one is exhausted.
	usedTokens := 0
	usedCostUSD := 0.0
	runStartedAt := workflow.Now(ctx)
	forcedFinaleReason := "" // empty = turn budget; policy checks set their own
	// Subtree spend rollup: every awaited child run (delegate or plan task)
	// adds its own totals here, so run.completed and team.completed report
	// honest numbers without replaying the journal.
	var subtree subtreeTotals
	// executePlan validates, resolves and runs a whole plan DAG inside one
	// tool call (docs/agent-delegation.md, plans). Defined before the turn
	// loop: team program runs (docs/agent-teams.md §9) execute it directly,
	// without a model turn at the root. Rejections before the first child
	// starts are inline failures with effect=none; after that a failed task
	// skips only its transitive dependents while independent branches finish.
	// Prior plan answers from the same run are available to task prompts as
	// {{task-id.answer}} placeholders (re-plan support).
	executePlan := func(call llm.ToolCall, operationID, argumentsHash string, startTool func() error) (string, *planExecution, error) {
		if err := startTool(); err != nil {
			return "", nil, err
		}
		reject := func(detail string) (string, *planExecution, error) {
			if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "plan", "error_type": "plan_invalid", "effect": "none", "detail": detail}); err != nil {
				return "", nil, err
			}
			return "Plan rejected before execution, no effect: " + detail + ". Re-issue the plan with corrected arguments.", nil, nil
		}
		spec, parseErr := planSpecFromArgs(call.Args)
		if parseErr != "" {
			return reject(parseErr)
		}
		if input.DelegationDepth >= MaxDelegationDepth {
			if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "plan", "error_type": "delegation_depth_exceeded", "effect": "none", "depth": input.DelegationDepth}); err != nil {
				return "", nil, err
			}
			return "Plan rejected before execution, no effect: the delegation depth limit is reached. Perform the remaining work yourself with your own tools.", nil, nil
		}
		dag, validationErr := validatePlanSpec(spec, planAnswers)
		if validationErr != "" {
			return reject(validationErr)
		}
		plans++
		planOrdinal := plans
		// Child run ids must be unique per plan call and per launch round:
		// workflow ids and event scopes derive from them, and the append-only
		// outbox rejects a reused id carrying new content. A relaunched task
		// (rework round, re-plan) is a new durable attempt, not a replay — so it
		// gets its own id instead of colliding with the recorded events of the
		// previous attempt. First plan, first round keeps the historical format.
		planTaskRunID := func(index int, taskID string, round int) string {
			id := input.RunID + "/plan"
			if planOrdinal > 1 {
				id += strconv.Itoa(planOrdinal)
			}
			id += fmt.Sprintf("/%02d-%s", index+1, taskID)
			if round > 1 {
				id += fmt.Sprintf("-r%d", round)
			}
			return id
		}
		agentIDs := map[string]string{}
		taskIndex := map[string]int{}
		for index := range spec.Tasks {
			agentIDs[spec.Tasks[index].ID] = spec.Tasks[index].AgentID
			taskIndex[spec.Tasks[index].ID] = index
		}
		// Resolve every task up front: an unknown agent rejects the whole plan
		// before any child starts (effect=none) — no partial fan-out on a typo.
		resolveCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, ScheduleToCloseTimeout: 40 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3}})
		resolved := map[string]RunInput{}
		for index := range spec.Tasks {
			task := spec.Tasks[index]
			childRunID := planTaskRunID(index, task.ID, 1)
			var childInput RunInput
			resolveErr := workflow.ExecuteActivity(resolveCtx, ActivityResolveAgent, ResolveAgentRequest{Project: input.Project, AgentID: task.AgentID, RunID: childRunID, TaskID: input.TaskID, Prompt: task.Prompt, ActorID: input.ActorID, MaxTurns: task.MaxTurns, DelegationDepth: input.DelegationDepth + 1}).Get(ctx, &childInput)
			if resolveErr != nil {
				if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "plan", "error_type": "agent_resolution_failed", "effect": "none", "detail": boundedFailureDetail(resolveErr)}); err != nil {
					return "", nil, err
				}
				return "Plan rejected before execution, no effect: agent \"" + task.AgentID + "\" for task \"" + task.ID + "\" could not be resolved (" + boundedFailureDetail(resolveErr) + "). Check the agent_id and re-issue the plan.", nil, nil
			}
			resolved[task.ID] = childInput
		}
		// Acceptance-gate budget (docs/agent-delegation.md): a task may be
		// sent back at most maxRework times, so one plan launches at most
		// tasks × (1+maxRework) child runs before settling honestly.
		maxRework := spec.MaxRework
		if maxRework <= 0 {
			maxRework = DefaultMaxRework
		}
		launchBudget := len(spec.Tasks) * (1 + maxRework)
		totalLaunches := 0
		tasksData := make([]map[string]any, 0, len(spec.Tasks))
		for index := range spec.Tasks {
			task := spec.Tasks[index]
			deps := task.DependsOn
			if deps == nil {
				deps = []string{}
			}
			entry := map[string]any{"id": task.ID, "agent_id": task.AgentID, "depends_on": deps}
			if len(task.ReviewOf) > 0 {
				entry["review_of"] = append([]string{}, task.ReviewOf...)
			}
			tasksData = append(tasksData, entry)
		}
		if err := emit(activityCtx, state, "plan.started", map[string]any{"operation_id": operationID, "goal": spec.Goal, "tasks": tasksData, "max_rework": maxRework}); err != nil {
			return "", nil, err
		}
		summarizeChild := func(runID string) *ChildRunSummary {
			summaryCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, ScheduleToCloseTimeout: 40 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3}})
			var summary ChildRunSummary
			if err := workflow.ExecuteActivity(summaryCtx, ActivitySummarizeChildRun, ChildRunSummaryRequest{Project: input.Project, RunID: runID}).Get(ctx, &summary); err != nil {
				return nil
			}
			return &summary
		}
		statuses := map[string]string{} // "" = pending, running, completed, failed, skipped, invalidated
		// Every task gets an explicit pending entry: activeCount() ranges over
		// the map, so tasks that never launched must still count as active.
		for index := range spec.Tasks {
			statuses[spec.Tasks[index].ID] = ""
		}
		answers := map[string]string{}
		outcomesByTask := map[string]planTaskOutcome{}
		var inflightPlan []*planPendingTask
		// Acceptance-gate state (docs/agent-delegation.md): rework rounds used
		// per task, child launches per task, per-round feedback, and why a
		// running child was cancelled (its upstream reopened or died).
		attempts := map[string]int{}
		launches := map[string]int{}
		lastAnswer := map[string]string{}
		reworkFeedback := map[string]string{}
		staleReason := map[string]string{}
		var rejections []planRejection
		activeCount := func() int {
			count := 0
			for _, s := range statuses {
				if s == "" || s == "running" {
					count++
				}
			}
			return count
		}
		skipBranch := func(failedID string) error {
			queue := append([]string{}, dag.Dependents[failedID]...)
			for len(queue) > 0 {
				id := queue[0]
				queue = queue[1:]
				if statuses[id] != "" {
					continue
				}
				statuses[id] = planStatusSkipped
				outcomesByTask[id] = planTaskOutcome{TaskID: id, AgentID: agentIDs[id], Status: planStatusSkipped, BlockedBy: failedID, SkipReason: "upstream_failed"}
				if err := emit(activityCtx, state, "plan.task.skipped", map[string]any{"operation_id": operationID, "task_id": id, "agent_id": agentIDs[id], "reason": "upstream_failed", "blocked_by": failedID}); err != nil {
					return err
				}
				queue = append(queue, dag.Dependents[id]...)
			}
			return nil
		}
		// cancelRunningChild requests cancellation of a still-inflight child;
		// tasks whose future already resolved are skipped silently.
		cancelRunningChild := func(taskID string) {
			for _, pending := range inflightPlan {
				if pending.taskID == taskID && pending.exec.ID != "" {
					_ = workflow.RequestCancelExternalWorkflow(ctx, pending.exec.ID, pending.exec.RunID)
					return
				}
			}
		}
		// regateDownstream re-opens the consumers of a reopened task: completed
		// dependents go back to pending (their stale answers are dropped), running
		// ones are cancelled and re-gated when their future resolves, pending ones
		// just get their dep counter restored. The graph never gains edges —
		// this is executor state, not a back-edge in the DAG.
		regateDownstream := func(root string) error {
			var walk func(id string) error
			walk = func(id string) error {
				for _, dependent := range dag.Dependents[id] {
					switch statuses[dependent] {
					case "":
						dag.DepsLeft[dependent]++
					case planStatusCompleted:
						dag.DepsLeft[dependent]++
						statuses[dependent] = ""
						delete(answers, dependent)
						delete(planAnswers, dependent)
						delete(outcomesByTask, dependent)
						if err := emit(activityCtx, state, "plan.task.invalidated", map[string]any{"operation_id": operationID, "task_id": dependent, "agent_id": agentIDs[dependent], "reason": "upstream_rework"}); err != nil {
							return err
						}
						if err := walk(dependent); err != nil {
							return err
						}
					case "running":
						dag.DepsLeft[dependent]++
						if _, stale := staleReason[dependent]; !stale {
							staleReason[dependent] = "upstream_rework"
							if err := emit(activityCtx, state, "plan.task.invalidated", map[string]any{"operation_id": operationID, "task_id": dependent, "agent_id": agentIDs[dependent], "reason": "upstream_rework"}); err != nil {
								return err
							}
							cancelRunningChild(dependent)
						}
					}
				}
				return nil
			}
			return walk(root)
		}
		// discardDownstream settles the branch under a task that exhausted its
		// rework rounds: nothing downstream may run on the rejected result.
		// Pending dependents are skipped, completed ones invalidated, running
		// ones cancelled into invalidated.
		discardDownstream := func(root string) error {
			var walk func(id string) error
			walk = func(id string) error {
				for _, dependent := range dag.Dependents[id] {
					switch statuses[dependent] {
					case "":
						statuses[dependent] = planStatusSkipped
						outcomesByTask[dependent] = planTaskOutcome{TaskID: dependent, AgentID: agentIDs[dependent], Status: planStatusSkipped, BlockedBy: id, SkipReason: "upstream_rework_exhausted"}
						if err := emit(activityCtx, state, "plan.task.skipped", map[string]any{"operation_id": operationID, "task_id": dependent, "agent_id": agentIDs[dependent], "reason": "upstream_rework_exhausted", "blocked_by": id}); err != nil {
							return err
						}
						if err := walk(dependent); err != nil {
							return err
						}
					case planStatusCompleted:
						dag.DepsLeft[dependent]++
						statuses[dependent] = planStatusInvalidated
						delete(answers, dependent)
						delete(planAnswers, dependent)
						outcomesByTask[dependent] = planTaskOutcome{TaskID: dependent, AgentID: agentIDs[dependent], Status: planStatusInvalidated, BlockedBy: id, SkipReason: "upstream_rework_exhausted"}
						if err := emit(activityCtx, state, "plan.task.invalidated", map[string]any{"operation_id": operationID, "task_id": dependent, "agent_id": agentIDs[dependent], "reason": "upstream_rework_exhausted"}); err != nil {
							return err
						}
						if err := walk(dependent); err != nil {
							return err
						}
					case "running":
						dag.DepsLeft[dependent]++
						if _, stale := staleReason[dependent]; !stale {
							staleReason[dependent] = "upstream_dead"
							if err := emit(activityCtx, state, "plan.task.invalidated", map[string]any{"operation_id": operationID, "task_id": dependent, "agent_id": agentIDs[dependent], "reason": "upstream_rework_exhausted"}); err != nil {
								return err
							}
							cancelRunningChild(dependent)
						}
					}
				}
				return nil
			}
			return walk(root)
		}
		failTask := func(taskID, childRunID, errorType string, err error, summary *ChildRunSummary) error {
			statuses[taskID] = planStatusFailed
			outcomesByTask[taskID] = planTaskOutcome{TaskID: taskID, AgentID: agentIDs[taskID], ChildRunID: childRunID, Status: planStatusFailed, Error: boundedFailureDetail(err)}
			data := map[string]any{"operation_id": operationID, "task_id": taskID, "child_run_id": childRunID, "agent_id": agentIDs[taskID], "error_type": errorType, "error": boundedFailureDetail(err), "effect": "uncertain"}
			if summary != nil {
				data["child_ops_total"] = summary.Total
				data["child_ops_unresolved"] = summary.Unresolved
				if summary.Unresolved == 0 && summary.Total > 0 {
					data["effect"] = "occurred"
				}
				if summary.Total == 0 {
					data["effect"] = "none"
				}
			}
			if err := emit(activityCtx, state, "plan.task.failed", data); err != nil {
				return err
			}
			return skipBranch(taskID)
		}
		// The loop runs until every task reaches a terminal status (completed,
		// failed, skipped, invalidated); rework rounds move tasks back to pending.
		for activeCount() > 0 {
			// Run-time policy: stop launching new tasks past the limit; running
			// children finish, pending ones are skipped honestly.
			if input.TimeoutSeconds > 0 && workflow.Now(ctx).Sub(runStartedAt) >= time.Duration(input.TimeoutSeconds)*time.Second {
				for index := range spec.Tasks {
					id := spec.Tasks[index].ID
					if statuses[id] != "" {
						continue
					}
					statuses[id] = planStatusSkipped
					outcomesByTask[id] = planTaskOutcome{TaskID: id, AgentID: agentIDs[id], Status: planStatusSkipped, SkipReason: "run_time_limit"}
					if err := emit(activityCtx, state, "plan.task.skipped", map[string]any{"operation_id": operationID, "task_id": id, "agent_id": agentIDs[id], "reason": "run_time_limit"}); err != nil {
						return "", nil, err
					}
				}
				if len(inflightPlan) == 0 {
					break
				}
			}
			// Launch every ready task in spec order while slots are free. Past the
			// launch budget (rework included) ready tasks are skipped honestly.
			budgetExhausted := totalLaunches >= launchBudget
			for index := range spec.Tasks {
				task := spec.Tasks[index]
				if statuses[task.ID] != "" || dag.DepsLeft[task.ID] > 0 {
					continue
				}
				if len(inflightPlan) >= delegationWidthLimit(&input) {
					break
				}
				if budgetExhausted {
					statuses[task.ID] = planStatusSkipped
					outcomesByTask[task.ID] = planTaskOutcome{TaskID: task.ID, AgentID: agentIDs[task.ID], Status: planStatusSkipped, SkipReason: "execution_budget"}
					if err := emit(activityCtx, state, "plan.task.skipped", map[string]any{"operation_id": operationID, "task_id": task.ID, "agent_id": agentIDs[task.ID], "reason": "execution_budget"}); err != nil {
						return "", nil, err
					}
					continue
				}
				childRunID := planTaskRunID(index, task.ID, launches[task.ID]+1)
				child := resolved[task.ID]
				child.RunID = childRunID
				child.Prompt = planTaskPrompt(task, answers, planAnswers)
				if feedback := reworkFeedback[task.ID]; feedback != "" {
					// Rework round: the reviewer rejected the previous result. The
					// task gets the concrete fix instructions plus its last answer —
					// a bounded retry, not a fresh start.
					child.Prompt += "\n\nRework round " + strconv.Itoa(launches[task.ID]+1) + ": the reviewer rejected the previous result.\n\nReviewer feedback\n\n" + feedback
					if previous := lastAnswer[task.ID]; previous != "" {
						child.Prompt += "\n\nYour previous result\n\n" + previous
					}
					delete(reworkFeedback, task.ID)
				}
				if len(task.ReviewOf) > 0 {
					child.Prompt += reviewContractPrompt(task.ReviewOf)
					// The verdict tool is injected and force-allowed: the gate is
					// the plan's contract, not the reviewer agent's own surface.
					child.ReviewOf = task.ReviewOf
					child.Tools = append(slices.Clone(child.Tools), ReviewVerdictTool())
					if len(child.ToolAllowlist) > 0 {
						child.ToolAllowlist = append(slices.Clone(child.ToolAllowlist), "submit_review")
					}
					child.DenyTools = withoutTool(child.DenyTools, "submit_review")
				}
				child.ParentRunID = input.RunID
				child.ParentFrameID = state.frame
				child.ParentEventID = state.previousEventID
				taskDeps := task.DependsOn
				if taskDeps == nil {
					taskDeps = []string{}
				}
				startedData := map[string]any{"operation_id": operationID, "task_id": task.ID, "child_run_id": childRunID, "agent_id": task.AgentID, "depends_on": taskDeps, "round": launches[task.ID] + 1}
				if len(task.ReviewOf) > 0 {
					startedData["review_of"] = append([]string{}, task.ReviewOf...)
				}
				if err := emit(activityCtx, state, "plan.task.started", startedData); err != nil {
					return "", nil, err
				}
				childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: WorkflowID(input.SourceID, input.Project, childRunID)})
				future := workflow.ExecuteChildWorkflow(childCtx, "AgentRun", child)
				var childExec workflow.Execution
				if startErr := future.GetChildWorkflowExecution().Get(ctx, &childExec); startErr != nil {
					if ctx.Err() != nil {
						return "", nil, startErr
					}
					if err := failTask(task.ID, childRunID, "delegated_run_start_failed", startErr, nil); err != nil {
						return "", nil, err
					}
					continue
				}
				statuses[task.ID] = "running"
				launches[task.ID]++
				totalLaunches++
				inflightPlan = append(inflightPlan, &planPendingTask{taskID: task.ID, agentID: task.AgentID, childRunID: childRunID, future: future, exec: childExec})
			}
			if len(inflightPlan) == 0 {
				break
			}
			p := inflightPlan[0]
			inflightPlan = inflightPlan[1:]
			var child RunResult
			childErr := p.future.Get(ctx, &child)
			if childErr == nil {
				// Subtree rollup: the child's totals (its own plus its children)
				// flow into this run's honest spend.
				subtree.tokens += child.TotalTokens
				subtree.costUSD += child.CostUSD
			}
			// Stale settlement first (docs/agent-delegation.md, acceptance
			// gates): this child was cancelled because its upstream reopened or
			// died. Whatever it produced is discarded — no failure event here,
			// the invalidation was emitted when the branch moved.
			if reason, stale := staleReason[p.taskID]; stale {
				delete(staleReason, p.taskID)
				if reason == "upstream_dead" {
					statuses[p.taskID] = planStatusInvalidated
					outcomesByTask[p.taskID] = planTaskOutcome{TaskID: p.taskID, AgentID: p.agentID, ChildRunID: p.childRunID, Status: planStatusInvalidated, SkipReason: "upstream_rework_exhausted"}
					// Its own pending consumers can no longer run: skip the branch.
					if err := skipBranch(p.taskID); err != nil {
						return "", nil, err
					}
				} else {
					statuses[p.taskID] = ""
					for _, dep := range spec.Tasks[taskIndex[p.taskID]].DependsOn {
						if statuses[dep] == planStatusFailed || statuses[dep] == planStatusSkipped || statuses[dep] == planStatusInvalidated {
							statuses[p.taskID] = planStatusSkipped
							outcomesByTask[p.taskID] = planTaskOutcome{TaskID: p.taskID, AgentID: p.agentID, ChildRunID: p.childRunID, Status: planStatusSkipped, BlockedBy: dep, SkipReason: "upstream_failed"}
							if err := emit(activityCtx, state, "plan.task.skipped", map[string]any{"operation_id": operationID, "task_id": p.taskID, "agent_id": p.agentID, "reason": "upstream_failed", "blocked_by": dep}); err != nil {
								return "", nil, err
							}
							break
						}
					}
				}
				continue
			}
			if ctx.Err() != nil {
				return "", nil, ctx.Err()
			}
			if childErr != nil {
				if err := failTask(p.taskID, p.childRunID, "delegated_run_failed", childErr, summarizeChild(p.childRunID)); err != nil {
					return "", nil, err
				}
				continue
			}
			specTask := spec.Tasks[taskIndex[p.taskID]]
			// Reviewer verdict (acceptance gates): rework reopens the rejected
			// tasks and re-gates their consumers; accept (or no verdict at all)
			// completes normally.
			verdict := ""
			if len(specTask.ReviewOf) > 0 && child.ReviewVerdict != nil {
				verdict = child.ReviewVerdict.Verdict
			}
			if verdict == "rework" {
				rejected := make([]string, 0, len(specTask.ReviewOf))
				feedbacks := map[string]string{}
				for _, reviewed := range specTask.ReviewOf {
					if feedback := strings.TrimSpace(child.ReviewVerdict.Feedback[reviewed]); feedback != "" {
						rejected = append(rejected, reviewed)
						feedbacks[reviewed] = feedback
					}
				}
				if len(rejected) == 0 {
					verdict = "accept" // rework without actionable feedback accepts
				} else {
					// The reviewer task itself is done for this round; its verdict —
					// not its prose — is the deliverable, so no answer is recorded.
					if err := emit(activityCtx, state, "plan.task.completed", map[string]any{"operation_id": operationID, "task_id": p.taskID, "child_run_id": p.childRunID, "agent_id": p.agentID, "child_status": child.Status, "turns": child.Turns, "round": launches[p.taskID], "verdict": "rework"}); err != nil {
						return "", nil, err
					}
					for _, reviewed := range rejected {
						attempts[reviewed]++
						feedback := feedbacks[reviewed]
						if err := emit(activityCtx, state, "plan.task.rejected", map[string]any{"operation_id": operationID, "task_id": reviewed, "agent_id": agentIDs[reviewed], "rejected_by": p.taskID, "round": attempts[reviewed], "feedback": feedback}); err != nil {
							return "", nil, err
						}
						rejections = append(rejections, planRejection{TaskID: reviewed, By: p.taskID, Round: attempts[reviewed], Feedback: feedback})
						if attempts[reviewed] > maxRework {
							// Rework exhausted: the task failed its acceptance gate and
							// nothing downstream may build on the rejected result.
							reviewedRunID := planTaskRunID(taskIndex[reviewed], reviewed, launches[reviewed])
							failure := "rework exhausted after " + strconv.Itoa(maxRework) + " round(s); last reviewer feedback: " + feedback
							statuses[reviewed] = planStatusFailed
							outcomesByTask[reviewed] = planTaskOutcome{TaskID: reviewed, AgentID: agentIDs[reviewed], ChildRunID: reviewedRunID, Status: planStatusFailed, Round: attempts[reviewed], Error: failure}
							if err := emit(activityCtx, state, "plan.task.failed", map[string]any{"operation_id": operationID, "task_id": reviewed, "agent_id": agentIDs[reviewed], "child_run_id": reviewedRunID, "error_type": "rework_exhausted", "round": attempts[reviewed], "max_rework": maxRework, "rejected_by": p.taskID, "error": failure, "effect": "uncertain"}); err != nil {
								return "", nil, err
							}
							if err := discardDownstream(reviewed); err != nil {
								return "", nil, err
							}
						} else {
							// Reopen: the task runs again with the reviewer's feedback; its
							// stale answer is dropped everywhere so no consumer template
							// resolves to the rejected result.
							lastAnswer[reviewed] = answers[reviewed]
							statuses[reviewed] = ""
							delete(answers, reviewed)
							delete(planAnswers, reviewed)
							delete(outcomesByTask, reviewed)
							reworkFeedback[reviewed] = feedback
							if err := emit(activityCtx, state, "plan.task.reopened", map[string]any{"operation_id": operationID, "task_id": reviewed, "agent_id": agentIDs[reviewed], "round": attempts[reviewed], "rejected_by": p.taskID}); err != nil {
								return "", nil, err
							}
							if err := regateDownstream(reviewed); err != nil {
								return "", nil, err
							}
						}
					}
					// Settle the reviewer: regate marked it stale (a reviewed task
					// reopened), so it re-runs and judges the fixed work; if the branch
					// died instead, the reviewer is invalidated with it.
					if reason, stale := staleReason[p.taskID]; stale {
						delete(staleReason, p.taskID)
						if reason == "upstream_dead" {
							statuses[p.taskID] = planStatusInvalidated
							outcomesByTask[p.taskID] = planTaskOutcome{TaskID: p.taskID, AgentID: p.agentID, ChildRunID: p.childRunID, Status: planStatusInvalidated, SkipReason: "upstream_rework_exhausted"}
							if err := skipBranch(p.taskID); err != nil {
								return "", nil, err
							}
						} else {
							statuses[p.taskID] = ""
						}
					} else {
						statuses[p.taskID] = planStatusCompleted
						outcomesByTask[p.taskID] = planTaskOutcome{TaskID: p.taskID, AgentID: p.agentID, ChildRunID: p.childRunID, Status: planStatusCompleted, ChildStatus: child.Status, Turns: child.Turns, Round: launches[p.taskID], Verdict: "rework", Answer: strings.TrimSpace(child.ReviewVerdict.Summary)}
						for _, dependent := range dag.Dependents[p.taskID] {
							dag.DepsLeft[dependent]--
						}
					}
					continue
				}
			}
			answer, _ := BoundedNarrative(child.Answer)
			answers[p.taskID] = answer
			planAnswers[p.taskID] = answer
			statuses[p.taskID] = planStatusCompleted
			outcome := planTaskOutcome{TaskID: p.taskID, AgentID: p.agentID, ChildRunID: p.childRunID, Status: planStatusCompleted, ChildStatus: child.Status, Turns: child.Turns, Round: launches[p.taskID], Answer: answer}
			completedData := map[string]any{"operation_id": operationID, "task_id": p.taskID, "child_run_id": p.childRunID, "agent_id": p.agentID, "child_status": child.Status, "turns": child.Turns, "round": launches[p.taskID]}
			if verdict != "" {
				outcome.Verdict = verdict
				completedData["verdict"] = verdict
			}
			outcomesByTask[p.taskID] = outcome
			if err := emit(activityCtx, state, "plan.task.completed", completedData); err != nil {
				return "", nil, err
			}
			for _, dependent := range dag.Dependents[p.taskID] {
				dag.DepsLeft[dependent]--
			}
		}
		// Assemble outcomes in spec order for a readable summary.
		outcomes := make([]planTaskOutcome, 0, len(spec.Tasks))
		for index := range spec.Tasks {
			if outcome, ok := outcomesByTask[spec.Tasks[index].ID]; ok {
				outcomes = append(outcomes, outcome)
			}
		}
		statusData := map[string]any{}
		for id, s := range statuses {
			statusData[id] = s
		}
		if err := emit(activityCtx, state, "plan.completed", map[string]any{"operation_id": operationID, "goal": spec.Goal, "statuses": statusData}); err != nil {
			return "", nil, err
		}
		content := planSummary(spec, outcomes, rejections)
		if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "plan", "output": compactJSON(content, 1000)}); err != nil {
			return "", nil, err
		}
		return content, &planExecution{Outcomes: outcomes, Rejections: rejections}, nil
	}
	if input.Team != nil {
		return runTeamProgram(ctx, activityCtx, state, input, executePlan, &subtree)
	}
	for turn := 1; turn <= input.MaxTurns; turn++ {
		result.Turns = turn
		if input.TimeoutSeconds > 0 && workflow.Now(ctx).Sub(runStartedAt) >= time.Duration(input.TimeoutSeconds)*time.Second {
			forcedFinaleReason = policyStopTime
			if err := emit(activityCtx, state, "policy.limit", policyLimitData("time_exhausted", turn, input, usedTokens, usedCostUSD)); err != nil {
				return result, err
			}
			break
		}
		frame := fmt.Sprintf("%s/turn/%02d", input.RunID, turn)
		if state.frame != "" {
			state.parentFrame = state.frame
		}
		state.frame = frame
		frames = append(frames, frame)
		if err := emit(activityCtx, state, "turn.started", map[string]any{"turn": turn}); err != nil {
			return result, err
		}
		turnMessages := messages
		if turn == input.MaxTurns {
			// Budget awareness: without a reminder the model reliably spends the
			// last tool turn on more calls and the run ends with an empty handoff.
			turnMessages = append(slices.Clone(messages), llm.Message{Role: "system", Content: "This is the final turn with tools. Complete only the remaining essential checks; your next message must be the final answer to the request."})
		}
		// Conversation history invariant: `messages` is strictly append-only —
		// turns only append the assistant response and tool results, never
		// rewrite or drop earlier entries. Provider prompt caching (keyed by
		// PromptCacheKey) relies on the prefix being byte-stable across turns;
		// mutating history would silently invalidate the cache and reset billing.
		modelReq := ModelRequest{Model: input.Model, Messages: turnMessages, Tools: advertisedTools(&input), RunID: input.RunID, Turn: turn, PromptCacheKey: input.TaskID}
		if err := emit(activityCtx, state, "model.started", map[string]any{"turn": turn, "model": input.Model}); err != nil {
			return result, err
		}
		var completion llm.Completion
		// Model endpoints fail transiently (hangs, EOFs, empty bodies). Retries
		// with backoff live inside the llm client; the activity therefore runs
		// exactly once and its timeout must cover the client's worst case: a
		// streaming attempt bounded only by chunk silence (StreamIdleTimeout) plus
		// the blocking fallback (3 attempts × request timeout + backoff ≈ 9m15s).
		// Long generations are expected, hence the configurable ceiling — see
		// ModelCallActivityTimeout. Non-retryable model errors (bad request, auth)
		// fail fast on the first client attempt.
		modelCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: ModelCallActivityTimeout, ScheduleToCloseTimeout: ModelCallActivityTimeout + time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		if err := workflow.ExecuteActivity(modelCtx, ActivityCallModel, modelReq).Get(ctx, &completion); err != nil {
			failureDetail := boundedFailureDetail(err)
			if eventErr := emit(activityCtx, state, "model.failed", map[string]any{"turn": turn, "error_type": "activity_failed", "error": failureDetail}); eventErr != nil {
				return result, eventErr
			}
			if eventErr := emit(activityCtx, state, "run.failed", map[string]any{"turn": turn, "error_type": "model_call_failed", "error": failureDetail}); eventErr != nil {
				return result, eventErr
			}
			result.Status = "failed"
			return result, err
		}
		completedData := map[string]any{"turn": turn, "tool_call_count": len(completion.ToolCalls), "finish_reason": completion.Finish, "total_tokens": completion.Usage.TotalTokens}
		maps.Copy(completedData, modelObservability(completion))
		if err := emit(activityCtx, state, "model.completed", completedData); err != nil {
			return result, err
		}
		// Emit the response text so the frontend can show tokens as they arrive.
		// Truncate to 4KB to keep events manageable.
		if completion.Content != "" {
			truncated := completion.Content
			if len(truncated) > 4096 {
				truncated = truncated[:4096]
			}
			if err := emit(activityCtx, state, "model.text_delta", map[string]any{"turn": turn, "text": truncated}); err != nil {
				return result, err
			}
		}
		// Emit reasoning content if the model provided it.
		if completion.Reasoning != "" {
			truncated := completion.Reasoning
			if len(truncated) > 4096 {
				truncated = truncated[:4096]
			}
			if err := emit(activityCtx, state, "model.reasoning", map[string]any{"turn": turn, "text": truncated}); err != nil {
				return result, err
			}
		}
		if len(completion.ToolCalls) == 0 {
			result.Answer = completion.Content
			result.Status = "completed"
			result.TotalTokens = usedTokens + completion.Usage.TotalTokens + subtree.tokens
			result.CostUSD = usedCostUSD + modelCallCostUSD(input, completion.Usage) + subtree.costUSD
			if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": turn}); err != nil {
				return result, err
			}
			if err := emit(activityCtx, state, "run.completed", runCompletedTotals(turn, result.TotalTokens, result.CostUSD)); err != nil {
				return result, err
			}
			if err := emitAgentSummary(activityCtx, state, result.Answer, frames, turn); err != nil {
				return result, err
			}
			startKnowledgeExtraction(ctx, activityCtx, state, input)
			return result, nil
		}
		// Policy budgets are checked after a model call that still wants tools: a
		// completion that already carries the final answer completes normally.
		usedTokens += completion.Usage.TotalTokens
		usedCostUSD += modelCallCostUSD(input, completion.Usage)
		if reason := policyStopReason(input, usedTokens, usedCostUSD); reason != "" {
			forcedFinaleReason = reason
			if err := emit(activityCtx, state, "policy.limit", policyLimitData(strings.TrimPrefix(reason, "policy."), turn, input, usedTokens, usedCostUSD)); err != nil {
				return result, err
			}
			break
		}
		messages = append(messages, llm.Message{Role: "assistant", Content: completion.Content, ToolCalls: completion.ToolCalls})
		// Degenerate model responses emit the same call many times in one
		// completion; live runs showed 10+ identical failing verifications per
		// turn. Identical calls within one response are executed exactly once —
		// side effects must not multiply — and duplicates answer with the
		// original execution result, linked by operation id.
		type executedCall struct {
			operationID string
			result      ToolResult
			pending     *pendingDelegation
		}
		executed := map[string]executedCall{}
		// Parallel delegation (docs/agent-delegation.md): delegate calls of one
		// response start their child runs immediately and are awaited later —
		// at a width-limit slot or, at the latest, before the turn closes. FIFO
		// awaiting keeps the event order deterministic for replay.
		var inflight []*pendingDelegation
		awaitDelegation := func(p *pendingDelegation) error {
			var child RunResult
			childErr := p.future.Get(ctx, &child)
			if childErr == nil {
				subtree.tokens += child.TotalTokens
				subtree.costUSD += child.CostUSD
			}
			content := ""
			if childErr != nil {
				if ctx.Err() != nil {
					return childErr
				}
				// The child is terminal now, so its whole event stream is already in
				// the journal. Classifying it turns a generic "may have side effects"
				// into a precise statement (docs/failure-reconciliation.md).
				summaryCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, ScheduleToCloseTimeout: 40 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3}})
				var summary ChildRunSummary
				summaryErr := workflow.ExecuteActivity(summaryCtx, ActivitySummarizeChildRun, ChildRunSummaryRequest{Project: input.Project, RunID: p.childRunID}).Get(ctx, &summary)
				if summaryErr != nil && ctx.Err() != nil {
					return summaryErr
				}
				effect := "uncertain"
				content = "Delegated run " + p.childRunID + " failed; its effects may be uncertain. Verify the current state before retrying or continue the work yourself."
				if summaryErr != nil {
					content = "Delegated run " + p.childRunID + " failed; its effects may be uncertain (child operations could not be summarized: " + boundedFailureDetail(summaryErr) + "). Verify the current state before retrying or continue the work yourself."
				} else {
					switch {
					case summary.Unresolved > 0:
						effect = "uncertain"
						content = "Delegated run " + p.childRunID + " failed with " + strconv.Itoa(summary.Unresolved) + " of its " + strconv.Itoa(summary.Total) + " tool operation(s) unresolved. Inspect run " + p.childRunID + " before retrying or continue the work yourself."
					case summary.Total > 0:
						effect = "occurred"
						content = "Delegated run " + p.childRunID + " failed after " + strconv.Itoa(summary.Total) + " fully recorded tool operation(s); every child effect is settled in the journal, nothing external is pending. Child run trajectory: " + p.childRunID + "."
					default:
						effect = "none"
						content = "Delegated run " + p.childRunID + " failed before performing any tool operations; no effects. It is safe to retry the delegation or do the work yourself."
					}
				}
				delegationFailed := map[string]any{"operation_id": p.operationID, "child_run_id": p.childRunID, "agent_id": p.agentID, "error_type": "delegated_run_failed", "error": boundedFailureDetail(childErr)}
				toolFailedData := map[string]any{"operation_id": p.operationID, "arguments_hash": p.argumentsHash, "tool": "delegate", "error_type": "delegated_run_failed", "effect": effect, "child_run_id": p.childRunID}
				if summaryErr == nil {
					delegationFailed["child_ops_total"] = summary.Total
					delegationFailed["child_ops_unresolved"] = summary.Unresolved
					toolFailedData["child_ops_total"] = summary.Total
					toolFailedData["child_ops_unresolved"] = summary.Unresolved
				}
				if err := emit(activityCtx, state, "delegation.failed", delegationFailed); err != nil {
					return err
				}
				if err := emit(activityCtx, state, "tool.failed", toolFailedData); err != nil {
					return err
				}
			} else {
				if err := emit(activityCtx, state, "delegation.completed", map[string]any{"operation_id": p.operationID, "child_run_id": p.childRunID, "agent_id": p.agentID, "child_status": child.Status, "turns": child.Turns}); err != nil {
					return err
				}
				answer, _ := BoundedNarrative(child.Answer)
				content = "Delegated agent \"" + p.agentID + "\" (run " + p.childRunID + ") finished with status " + child.Status + " after " + strconv.Itoa(child.Turns) + " turns. Final answer:\n\n" + answer
				if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": p.operationID, "arguments_hash": p.argumentsHash, "tool": "delegate", "output": compactJSON(content, 1000), "child_run_id": p.childRunID}); err != nil {
					return err
				}
			}
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: p.callID, Content: content})
			for _, duplicate := range p.duplicates {
				if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": duplicate.operationID, "arguments_hash": p.argumentsHash, "tool": "delegate", "duplicate_of": p.operationID}); err != nil {
					return err
				}
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: duplicate.callID, Content: content})
			}
			return nil
		}
		// launchDelegation resolves and starts one delegated child run without
		// blocking on it. Failures that happen before the child starts are
		// reported inline (returned as content, deferred=false); a started child
		// is parked in inflight and deferred=true tells the caller to skip the
		// per-call tail — the tool message lands when the child is awaited.
		// startTool is the caller's idempotent tool.started emitter.
		launchDelegation := func(call llm.ToolCall, callKey, operationID, argumentsHash string, startTool func() error) (string, bool, error) {
			if err := startTool(); err != nil {
				return "", false, err
			}
			delegateAgentID, _ := call.Args["agent_id"].(string)
			delegatePrompt, _ := call.Args["prompt"].(string)
			delegateMaxTurns := 0
			if v, ok := call.Args["max_turns"].(float64); ok {
				delegateMaxTurns = int(v)
			}
			if strings.TrimSpace(delegateAgentID) == "" || strings.TrimSpace(delegatePrompt) == "" {
				if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "delegate", "error_type": "malformed_arguments", "effect": "none", "detail": "agent_id and prompt are required"}); err != nil {
					return "", false, err
				}
				return "Delegate call rejected before execution, no effect: agent_id and prompt are required. Re-issue the call with both arguments.", false, nil
			}
			if input.DelegationDepth >= MaxDelegationDepth {
				if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "delegate", "error_type": "delegation_depth_exceeded", "effect": "none", "depth": input.DelegationDepth}); err != nil {
					return "", false, err
				}
				return "Delegation depth limit reached; delegating further is not allowed. Perform the remaining work yourself with your own tools.", false, nil
			}
			delegations++
			childRunID := fmt.Sprintf("%s/delegate/%02d", input.RunID, delegations)
			resolveCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, ScheduleToCloseTimeout: 40 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3}})
			var childInput RunInput
			resolveErr := workflow.ExecuteActivity(resolveCtx, ActivityResolveAgent, ResolveAgentRequest{Project: input.Project, AgentID: delegateAgentID, RunID: childRunID, TaskID: input.TaskID, Prompt: delegatePrompt, ActorID: input.ActorID, MaxTurns: delegateMaxTurns, DelegationDepth: input.DelegationDepth + 1}).Get(ctx, &childInput)
			if resolveErr != nil {
				if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "delegate", "error_type": "agent_resolution_failed", "effect": "none", "detail": boundedFailureDetail(resolveErr)}); err != nil {
					return "", false, err
				}
				return "Delegation rejected before execution, no effect: agent \"" + delegateAgentID + "\" could not be resolved (" + boundedFailureDetail(resolveErr) + "). Check the agent_id and re-issue, or do the work yourself.", false, nil
			}
			// Width gate: wait for a free slot BEFORE initiating the next child so at
			// most MaxDelegationWidth children of this turn are actually in flight.
			// The awaited child's completion events land first — a slot visibly opens
			// before the next delegation starts.
			if len(inflight) >= MaxDelegationWidth {
				if err := awaitDelegation(inflight[0]); err != nil {
					return "", false, err
				}
				inflight = inflight[1:]
			}
			if err := emit(activityCtx, state, "delegation.started", map[string]any{"operation_id": operationID, "child_run_id": childRunID, "agent_id": delegateAgentID, "ordinal": delegations, "depth": input.DelegationDepth + 1}); err != nil {
				return "", false, err
			}
			childInput.ParentRunID = input.RunID
			childInput.ParentFrameID = state.frame
			childInput.ParentEventID = state.previousEventID
			childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: WorkflowID(input.SourceID, input.Project, childRunID)})
			childFuture := workflow.ExecuteChildWorkflow(childCtx, "AgentRun", childInput)
			// Awaiting the child execution separately splits failures into
			// two honest classes: if the execution could not even be started,
			// nothing ran and the effect is provably "none"; only a child
			// that started and then failed can leave effects uncertain.
			var childExec workflow.Execution
			if startErr := childFuture.GetChildWorkflowExecution().Get(ctx, &childExec); startErr != nil {
				if ctx.Err() != nil {
					return "", false, startErr
				}
				if err := emit(activityCtx, state, "delegation.failed", map[string]any{"operation_id": operationID, "child_run_id": childRunID, "agent_id": delegateAgentID, "error_type": "delegated_run_start_failed", "error": boundedFailureDetail(startErr)}); err != nil {
					return "", false, err
				}
				if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "delegate", "error_type": "delegated_run_start_failed", "effect": "none", "child_run_id": childRunID}); err != nil {
					return "", false, err
				}
				return "Delegated run " + childRunID + " was rejected before execution, no effect (" + boundedFailureDetail(startErr) + "). The child agent never started, so nothing it could do has happened; it is safe to retry the delegation or do the work yourself.", false, nil
			}
			pending := &pendingDelegation{callID: call.ID, operationID: operationID, argumentsHash: argumentsHash, childRunID: childRunID, agentID: delegateAgentID, future: childFuture}
			inflight = append(inflight, pending)
			executed[callKey] = executedCall{operationID: operationID, pending: pending}
			return "", true, nil
		}
		for _, call := range completion.ToolCalls {
			operationID := fmt.Sprintf("%s/%s", frame, call.ID)
			argumentsHash := operationArgumentsHash(call.Args)
			if call.ArgsError != "" {
				// The arguments never parsed, so the call is rejected before any
				// effect could land (effect=none); neighboring calls of this
				// response survive instead of failing the whole completion.
				argumentsHash = rawArgumentsHash(call.ArgsRaw)
				if err := emit(activityCtx, state, "tool.started", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "tool_call_id": call.ID, "arguments": compactJSON(call.Args, 500)}); err != nil {
					return result, err
				}
				if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "error_type": "malformed_arguments", "effect": "none", "detail": call.ArgsError}); err != nil {
					return result, err
				}
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: "Tool call arguments were malformed JSON; the call was not executed. Re-issue it with valid JSON arguments."})
				continue
			}
			callKey := dedupSignature(call.Name, call.Args) // legacy key format: call.Name + "\x00" + argumentsHash
			if original, duplicate := executed[callKey]; duplicate {
				if err := emit(activityCtx, state, "tool.started", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "tool_call_id": call.ID, "arguments": compactJSON(call.Args, 500), "duplicate_of": original.operationID}); err != nil {
					return result, err
				}
				if original.pending != nil {
					// The identical delegate call already launched its child run in
					// this response: the duplicate shares that child's result when it
					// lands instead of multiplying the delegation.
					original.pending.duplicates = append(original.pending.duplicates, pendingDuplicate{callID: call.ID, operationID: operationID})
					continue
				}
				if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "duplicate_of": original.operationID}); err != nil {
					return result, err
				}
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: original.result.Content})
				continue
			}
			toolCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 4 * time.Minute, ScheduleToCloseTimeout: 5 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
			isMCP := strings.HasPrefix(call.Name, "mcp__")
			recentlyApproved := false // set true after request_approval succeeds
			approvalRequired := call.Name == "request_approval" || contains(input.ApprovalTools, call.Name) ||
				(input.RequireToolApproval && !policySafeTool(call.Name))
			autoApproved := sandboxAutoApprovedTools[call.Name] && contains(input.AutoApproveTools, call.Name)
			toolStarted := false
			// deferred marks a delegate call whose child run is in flight: its
			// tool message and executed entry are handled at await time.
			deferred := false
			// runCancelled stops the whole run after this turn — set by the
			// cancel timeout policy of ask_human (§33).
			runCancelled := false
			startTool := func() error {
				if toolStarted {
					return nil
				}
				toolStarted = true
				return emit(activityCtx, state, "tool.started", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "tool_call_id": call.ID, "arguments": compactJSON(call.Args, 500)})
			}
			startMCP := func() error {
				if !isMCP {
					return nil
				}
				return emit(activityCtx, state, "mcp.call.started", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "server": mcpServerForTool(call.Name, input.MCPServer), "approval_required": approvalRequired})
			}
			var toolResult ToolResult
			toolFailed := false
			toolBlocked := false
			if call.Name == "ask_human" {
				// Escalation to a human (docs/triggers-and-escalations.md §5-13,
				// docs/org-structure.md §25-33): a self-contained question with
				// optional structured options. The run pauses on the approval signal
				// channel; the answer rides the same Approval payload with Response set.
				question, _ := call.Args["question"].(string)
				humanContext, _ := call.Args["context"].(string)
				options := stringList(call.Args["options"])
				recipient, _ := call.Args["recipient"].(string)
				timeoutSeconds := input.ApprovalTimeoutSeconds
				if requested, ok := numericArg(call.Args["timeout_sec"]); ok && requested > 0 {
					timeoutSeconds = int(requested)
				}
				if timeoutSeconds > 86400 {
					timeoutSeconds = 86400
				}
				// Timeout policy (§33): what happens when nobody answers. The policy
				// lives here, in the execution model — never in the transport.
				timeoutPolicy, _ := call.Args["timeout_policy"].(string)
				if !validTimeoutPolicy(timeoutPolicy) {
					timeoutPolicy = "fallback"
				}
				description, _ := approvalOperation(operationID, call.Name, call.Args, false)
				operation := description["operation"].(map[string]any)
				argumentsHash = operation["arguments_hash"].(string)
				if err := startTool(); err != nil {
					return result, err
				}
				if err := emit(activityCtx, state, "human.requested", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "recipient": recipient, "question": question, "context": humanContext, "options": options, "timeout_seconds": timeoutSeconds, "timeout_policy": timeoutPolicy}); err != nil {
					return result, err
				}
				// Route the question to the recipient's preferred channel (§6-7,
				// §26-27): logical recipient → concrete user → channel. Delivery
				// problems are recorded, never fatal — the run keeps waiting.
				notifyCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, ScheduleToCloseTimeout: 45 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 2}})
				var delivery NotificationDelivery
				notifyReq := NotifyChannelRequest{Project: input.Project, RunID: input.RunID, OperationID: operationID, Recipient: recipient, ActorID: input.ActorID, AgentID: input.AgentID, Question: question, Context: humanContext, Options: options, TimeoutSeconds: timeoutSeconds, TimeoutPolicy: timeoutPolicy, ExecutionIdentityID: input.ExecutionIdentityID}
				if err := workflow.ExecuteActivity(notifyCtx, ActivityNotifyChannel, notifyReq).Get(ctx, &delivery); err != nil {
					delivery = NotificationDelivery{Recipient: recipient, Channel: "web", Status: "failed", Detail: boundedFailureDetail(err)}
				}
				if err := emit(activityCtx, state, "notification.sent", map[string]any{"operation_id": operationID, "recipient": delivery.Recipient, "channel": delivery.Channel, "status": delivery.Status, "detail": delivery.Detail}); err != nil {
					return result, err
				}
				// §30: the execution identity may not address this user. The tool
				// call fails without pausing — the agent must re-ask an allowed
				// recipient (the delivery detail names the constraint).
				if delivery.Status == "rejected" {
					if err := emit(activityCtx, state, "human.rejected", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "recipient": recipient, "reason": delivery.Detail}); err != nil {
						return result, err
					}
					toolResult.Content = "The question was rejected before delivery: " + delivery.Detail + ". Re-issue the ask with a recipient this run is allowed to address."
					toolBlocked = true
					if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name}); err != nil {
						return result, err
					}
					messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: toolResult.Content})
					executed[callKey] = executedCall{operationID: operationID, result: toolResult}
					continue
				}
				// Waiting rounds: the initial wait plus one extra round for the
				// retry/escalate policies (§33). Escalation re-targets the fallback
				// recipient — project_owner — for its second round.
				waitRounds := 1
				if timeoutPolicy == "retry" || timeoutPolicy == "escalate" {
					waitRounds = 2
				}
				approved, answer, timedOut, waitErr := false, Approval{}, true, error(nil)
				for round := 0; round < waitRounds; round++ {
					if round == 1 {
						roundRecipient := recipient
						if timeoutPolicy == "escalate" {
							roundRecipient = "project_owner"
						}
						if err := emit(activityCtx, state, "human.reasked", map[string]any{"operation_id": operationID, "recipient": roundRecipient, "policy": timeoutPolicy, "round": round + 1}); err != nil {
							return result, err
						}
						reask := notifyReq
						reask.Recipient = roundRecipient
						var redelivery NotificationDelivery
						if err := workflow.ExecuteActivity(notifyCtx, ActivityNotifyChannel, reask).Get(ctx, &redelivery); err != nil {
							redelivery = NotificationDelivery{Recipient: roundRecipient, Channel: "web", Status: "failed", Detail: boundedFailureDetail(err)}
						}
						if err := emit(activityCtx, state, "notification.sent", map[string]any{"operation_id": operationID, "recipient": redelivery.Recipient, "channel": redelivery.Channel, "status": redelivery.Status, "detail": redelivery.Detail, "round": round + 1}); err != nil {
							return result, err
						}
					}
					approved, answer, timedOut, waitErr = awaitApproval(ctx, operationID, time.Duration(timeoutSeconds)*time.Second)
					if waitErr != nil || !timedOut {
						break
					}
				}
				if waitErr != nil {
					return result, waitErr
				}
				// The run resumed (or the wait ended): close the entity row so the
				// inbox stops listing it. Best-effort — the lazy sweep in the list
				// endpoint catches anything this activity missed.
				closeCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 15 * time.Second, ScheduleToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 2}})
				switch {
				case timedOut:
					_ = workflow.ExecuteActivity(closeCtx, ActivityCloseHumanRequest, CloseHumanRequestInput{OperationID: operationID, Status: "expired"}).Get(ctx, nil)
					if err := emit(activityCtx, state, "human.timed_out", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "timeout_seconds": timeoutSeconds, "timeout_policy": timeoutPolicy}); err != nil {
						return result, err
					}
					switch timeoutPolicy {
					case "fail":
						toolResult.Content = "No human response within the timeout (policy: fail). The run cannot proceed without this answer."
						toolFailed = true
					case "cancel":
						toolResult.Content = "No human response within the timeout (policy: cancel). The run is being cancelled."
						toolBlocked = true
						runCancelled = true
					default: // fallback
						toolResult.Content = "No human response within the timeout. Act on the safest option that does not require the missing information, or report what blocked you."
						toolBlocked = true
					}
				case approved:
					_ = workflow.ExecuteActivity(closeCtx, ActivityCloseHumanRequest, CloseHumanRequestInput{OperationID: operationID, Status: "answered", Response: approvalText(answer.Response), ActorID: answer.ActorID}).Get(ctx, nil)
					response := approvalText(answer.Response)
					if response == "" {
						response = approvalText(answer.Reason)
					}
					if err := emit(activityCtx, state, "human.answered", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "actor": answer.ActorID, "response": response}); err != nil {
						return result, err
					}
					toolResult.Content = "Human response: " + response
				default:
					_ = workflow.ExecuteActivity(closeCtx, ActivityCloseHumanRequest, CloseHumanRequestInput{OperationID: operationID, Status: "cancelled", ActorID: answer.ActorID}).Get(ctx, nil)
					if err := emit(activityCtx, state, "human.cancelled", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "actor": answer.ActorID, "reason": approvalText(answer.Reason)}); err != nil {
						return result, err
					}
					toolResult.Content = "The human cancelled this question" + approvalSuffix(answer.Reason)
					toolBlocked = true
				}
				if runCancelled {
					// Policy cancel (§33): stop the run honestly instead of letting
					// the model wander on without the missing answer.
					result.Status = "cancelled"
					result.Answer = toolResult.Content
					return result, nil
				}
				if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name}); err != nil {
					return result, err
				}
			} else if call.Name == "submit_review" && len(input.ReviewOf) > 0 {
				// Acceptance-gate verdict (docs/agent-delegation.md): the plan
				// executor injected this tool into the reviewer run. The verdict is
				// the deliverable — the run finishes at the end of this turn.
				verdict, _ := call.Args["verdict"].(string)
				feedback := stringMapField(call.Args["feedback"])
				summary, _ := call.Args["summary"].(string)
				valid := verdict == "accept" || verdict == "rework"
				if verdict == "rework" {
					usable := 0
					for _, reviewed := range input.ReviewOf {
						if strings.TrimSpace(feedback[reviewed]) != "" {
							usable++
						}
					}
					if usable == 0 {
						valid = false
					}
				}
				if !valid {
					if err := startTool(); err != nil {
						return result, err
					}
					if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "submit_review", "error_type": "invalid_verdict", "effect": "none", "detail": "verdict must be \"accept\", or \"rework\" with feedback for at least one of: " + strings.Join(input.ReviewOf, ", ")}); err != nil {
						return result, err
					}
					messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: "Invalid review verdict: verdict must be \"accept\", or \"rework\" with concrete feedback for at least one reviewed task (" + strings.Join(input.ReviewOf, ", ") + "). Re-issue submit_review correctly."})
					continue
				}
				if err := startTool(); err != nil {
					return result, err
				}
				if reviewSubmitted {
					// One verdict per run; later calls are answered, not executed.
					toolResult.Content = "Review verdict already recorded; the run is finishing with it."
					if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "submit_review", "duplicate": true}); err != nil {
						return result, err
					}
				} else {
					filtered := map[string]string{}
					for _, reviewed := range input.ReviewOf {
						if text := strings.TrimSpace(feedback[reviewed]); text != "" {
							filtered[reviewed] = text
						}
					}
					reviewVerdict = &ReviewVerdict{Verdict: verdict, Feedback: filtered, Summary: strings.TrimSpace(summary)}
					reviewSubmitted = true
					if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "submit_review", "verdict": verdict, "review_of": input.ReviewOf}); err != nil {
						return result, err
					}
					toolResult.Content = "Review verdict recorded (verdict: " + verdict + "). The run finishes with this verdict; no further work is needed."
				}
			} else if approvalRequired && !recentlyApproved {
				action, reason := call.Name, ""
				if call.Name == "request_approval" {
					action, _ = call.Args["action"].(string)
					reason, _ = call.Args["reason"].(string)
				}
				approvalDescription, details := approvalOperation(operationID, call.Name, call.Args, input.Role == "reviewer" || input.Role == "qa")
				operation := approvalDescription["operation"].(map[string]any)
				argumentsHash = operation["arguments_hash"].(string)
				approvalData := map[string]any{"operation_id": operationID, "action": approvalText(action), "reason": approvalText(reason), "operation": operation, "details": details, "risk": approvalDescription["risk"], "redaction": approvalDescription["redaction"], "arguments_hash": argumentsHash}
				if err := emit(activityCtx, state, "approval.requested", approvalData); err != nil {
					return result, err
				}
				approved, approval, timedOut, waitErr := awaitApproval(ctx, operationID, time.Duration(input.ApprovalTimeoutSeconds)*time.Second)
				if waitErr != nil {
					return result, waitErr
				}
				if timedOut {
					if err := emit(activityCtx, state, "approval.timed_out", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "timeout_seconds": input.ApprovalTimeoutSeconds}); err != nil {
						return result, err
					}
					toolResult.Content = "Approval timed out; the action was not run."
					toolBlocked = true
					if err := emit(activityCtx, state, "tool.blocked", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "reason": "approval_timeout"}); err != nil {
						return result, err
					}
					if isMCP {
						if err := emit(activityCtx, state, "mcp.call.blocked", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "server": mcpServerForTool(call.Name, input.MCPServer), "reason": "approval_timeout"}); err != nil {
							return result, err
						}
					}
				} else if approved && (call.Name == "request_approval" || approval.ArgumentsHash == argumentsHash) {
					if err := emit(activityCtx, state, "approval.granted", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "approver": approval.ActorID, "reason": approvalText(approval.Reason)}); err != nil {
						return result, err
					}
					if call.Name == "request_approval" {
						if err := startTool(); err != nil {
							return result, err
						}
						toolResult.Content = "Approval granted. Continue with the requested action, but perform it only through an available tool."
						recentlyApproved = true
					} else if call.Name == "delegate" {
						// The approval covers the delegate call itself: launch the child
						// run here — ActivityRunTool does not know "delegate" and would
						// fail the call with "tool not registered".
						content, deferredLaunch, launchErr := launchDelegation(call, callKey, operationID, argumentsHash, startTool)
						if launchErr != nil {
							return result, launchErr
						}
						if deferredLaunch {
							deferred = true
						} else {
							toolResult.Content = content
						}
					} else if call.Name == "plan" {
						// Approved plan: execute the DAG directly for the same reason as
						// delegate — the tool activity does not know "plan".
						content, _, planErr := executePlan(call, operationID, argumentsHash, startTool)
						if planErr != nil {
							return result, planErr
						}
						toolResult.Content = content
					} else if err := startTool(); err != nil {
						return result, err
					} else if err := startMCP(); err != nil {
						return result, err
					} else {
						// Wall latency from the workflow clock covers scheduling, retries
						// and execution — the honest end-to-end cost of the operation.
						toolStarted := workflow.Now(ctx)
						if err := workflow.ExecuteActivity(toolCtx, ActivityRunTool, ToolRequest{RunID: input.RunID, OperationID: operationID, Name: call.Name, Role: input.Role, WorkspacePath: input.WorkspacePath, Project: input.Project, ActorID: input.ActorID, Arguments: call.Args, AllowedTools: input.ToolAllowlist, DeniedTools: input.DenyTools, ReadOnly: input.ReadOnly}).Get(ctx, &toolResult); err != nil {
							toolFailed = true
							failure := toolFailureData(operationID, argumentsHash, call.Name, err)
							failure["latency_ms"] = workflow.Now(ctx).Sub(toolStarted).Milliseconds()
							if eventErr := emit(activityCtx, state, "tool.failed", failure); eventErr != nil {
								return result, eventErr
							}
							toolResult.Content = toolFailureMessage(call.Name, err)
						} else if eventErr := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "output": compactJSON(toolResult.Content, 1000), "latency_ms": workflow.Now(ctx).Sub(toolStarted).Milliseconds()}); eventErr != nil {
							return result, eventErr
						}
					}
				} else {
					rejectionReason := approvalText(approval.Reason)
					if approved {
						rejectionReason = "approved operation arguments did not match the pending operation"
					}
					if err := emit(activityCtx, state, "approval.rejected", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "approver": approval.ActorID, "reason": rejectionReason}); err != nil {
						return result, err
					}
					toolResult.Content = "Approval rejected: " + rejectionReason
					toolBlocked = true
					blockReason := "approval_rejected"
					if approved {
						blockReason = "approval_arguments_mismatch"
					}
					if err := emit(activityCtx, state, "tool.blocked", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "reason": blockReason}); err != nil {
						return result, err
					}
					if isMCP {
						if err := emit(activityCtx, state, "mcp.call.blocked", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "server": mcpServerForTool(call.Name, input.MCPServer), "reason": "approval_rejected"}); err != nil {
							return result, err
						}
					}
				}
				if call.Name == "request_approval" {
					if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name}); err != nil {
						return result, err
					}
				}
			} else if call.Name == "remember" {
				if err := startTool(); err != nil {
					return result, err
				}
				proposition, _ := call.Args["proposition"].(string)
				knowledgeID := fmt.Sprintf("%s/knowledge/%02d", input.RunID, state.sequence+1)
				if proposition == "" {
					toolResult.Content = "proposition is required"
				} else {
					if err := emitKnowledge(activityCtx, state, knowledgeID, proposition, operationID, call.Args); err != nil {
						return result, err
					}
					toolResult.Content = "Knowledge recorded with id " + knowledgeID
				}
				if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name}); err != nil {
					return result, err
				}
			} else if call.Name == "delegate" {
				// Delegation runs a durable child AgentRun with the child agent's own
				// enforced configuration (docs/agent-delegation.md). The child starts
				// without blocking the parent; the result arrives as the tool outcome
				// when the child is awaited (see launchDelegation/awaitDelegation).
				content, deferredLaunch, launchErr := launchDelegation(call, callKey, operationID, argumentsHash, startTool)
				if launchErr != nil {
					return result, launchErr
				}
				if deferredLaunch {
					deferred = true
				} else {
					toolResult.Content = content
				}
			} else if call.Name == "plan" {
				// A plan runs its whole DAG inside this call (docs/agent-delegation.md,
				// plans); the summary text is the tool outcome.
				content, _, planErr := executePlan(call, operationID, argumentsHash, startTool)
				if planErr != nil {
					return result, planErr
				}
				toolResult.Content = content
			} else {
				if autoApproved {
					description, _ := approvalOperation(operationID, call.Name, call.Args, input.Role == "reviewer" || input.Role == "qa")
					operation := description["operation"].(map[string]any)
					if err := emit(activityCtx, state, "approval.auto_granted", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "operation": operation, "risk": description["risk"], "redaction": description["redaction"], "policy_id": "sandbox.workspace.v1", "approver": "kernel-policy", "workspace_path": input.WorkspacePath}); err != nil {
						return result, err
					}
				}
				if err := startTool(); err != nil {
					return result, err
				}
				if err := startMCP(); err != nil {
					return result, err
				}
				// Same wall-clock semantics as the approval path: scheduling,
				// retries and execution in one number.
				toolStarted := workflow.Now(ctx)
				if err := workflow.ExecuteActivity(toolCtx, ActivityRunTool, ToolRequest{RunID: input.RunID, OperationID: operationID, Name: call.Name, Role: input.Role, WorkspacePath: input.WorkspacePath, Project: input.Project, Arguments: call.Args, AllowedTools: input.ToolAllowlist, DeniedTools: input.DenyTools, ReadOnly: input.ReadOnly}).Get(ctx, &toolResult); err != nil {
					toolFailed = true
					failure := toolFailureData(operationID, argumentsHash, call.Name, err)
					failure["latency_ms"] = workflow.Now(ctx).Sub(toolStarted).Milliseconds()
					if eventErr := emit(activityCtx, state, "tool.failed", failure); eventErr != nil {
						return result, eventErr
					}
					toolResult.Content = toolFailureMessage(call.Name, err)
				} else {
					completed := map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "latency_ms": workflow.Now(ctx).Sub(toolStarted).Milliseconds()}
					// Exit code makes tool completion a first-class trajectory fact:
					// a command exiting non-zero is a failed attempt even though the
					// tool activity itself succeeded.
					if toolResult.ExitCode != nil {
						completed["exit_code"] = *toolResult.ExitCode
					}
					// Truncated output for trace visibility.
					if toolResult.Content != "" {
						completed["output"] = compactJSON(toolResult.Content, 1000)
					}
					if eventErr := emit(activityCtx, state, "tool.completed", completed); eventErr != nil {
						return result, eventErr
					}
					if proposal := executionObservationProposal(input, call, toolResult); proposal != nil {
						if err := emitExecutionObservation(activityCtx, state, proposal, operationID, argumentsHash); err != nil {
							return result, err
						}
					}
				}
			}
			if !deferred {
				// Deferred delegate calls skip the per-call tail: their tool message,
				// executed entry and completion events land at await time instead.
				if isMCP && !toolBlocked {
					eventType := "mcp.call.completed"
					data := map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": call.Name, "server": mcpServerForTool(call.Name, input.MCPServer)}
					if toolFailed {
						eventType = "mcp.call.failed"
						data["error_type"] = "activity_failed"
						data["outcome"] = "uncertain"
					} else {
						// Result is content-addressed, never stored raw: the ref plus byte
						// count let operators correlate the exact MCP payload without
						// copying tool output into the event stream.
						data["result_ref"] = resultContentRef(toolResult.Content)
						data["result_bytes"] = len(toolResult.Content)
					}
					if err := emit(activityCtx, state, eventType, data); err != nil {
						return result, err
					}
				}
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: toolResult.Content})
				for _, evidence := range toolResult.Evidence {
					if err := emit(activityCtx, state, "evidence.observed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool_call_id": call.ID, "ref": evidence.Ref, "type": evidence.Type}); err != nil {
						return result, err
					}
				}
				executed[callKey] = executedCall{operationID: operationID, result: toolResult}
			}
		}
		// Parallel delegation: await every child still in flight before the turn
		// closes so each delegate outcome lands in the turn that issued it.
		for _, pending := range inflight {
			if err := awaitDelegation(pending); err != nil {
				return result, err
			}
		}
		if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": turn, "tool_calls": len(completion.ToolCalls)}); err != nil {
			return result, err
		}
		// Acceptance gate (docs/agent-delegation.md): a reviewer run ends with
		// its verdict — submit_review was the deliverable, not a final message,
		// so the normal loop must not continue into another turn.
		if reviewSubmitted {
			result.Status = "completed"
			result.Answer = strings.TrimSpace(reviewVerdict.Summary)
			if result.Answer == "" {
				if reviewVerdict.Verdict == "rework" {
					rejected := make([]string, 0, len(reviewVerdict.Feedback))
					for _, reviewed := range input.ReviewOf {
						if _, ok := reviewVerdict.Feedback[reviewed]; ok {
							rejected = append(rejected, reviewed)
						}
					}
					result.Answer = "Rework requested for: " + strings.Join(rejected, ", ")
				} else {
					result.Answer = "Accepted: the reviewed tasks passed the review."
				}
			}
			result.ReviewVerdict = reviewVerdict
			// The verdict turn's usage is already in usedTokens (added before tool
			// execution); add the subtree so reviewer runs report honest totals.
			result.TotalTokens = usedTokens + subtree.tokens
			result.CostUSD = usedCostUSD + subtree.costUSD
			completedData := runCompletedTotals(turn, result.TotalTokens, result.CostUSD)
			completedData["review_verdict"] = reviewVerdict.Verdict
			if err := emit(activityCtx, state, "run.completed", completedData); err != nil {
				return result, err
			}
			if err := emitAgentSummary(activityCtx, state, result.Answer, frames, turn); err != nil {
				return result, err
			}
			startKnowledgeExtraction(ctx, activityCtx, state, input)
			return result, nil
		}
	}
	// Deterministic finale: the model spent every turn on tool calls, or a policy
	// limit (time/tokens/budget) stopped the loop. One more model call without
	// tools must turn the accumulated conversation into a final answer, so a
	// limited run still produces a handoff instead of silence. The status stays
	// honest (turn_limit / time_limit / token_limit / budget_limit); events
	// record that the answer came from a forced finale and why.
	if forcedFinaleReason == "" {
		forcedFinaleReason = "turn_limit"
	}
	finaleStatus := map[string]string{
		policyStopTime:   "time_limit",
		policyStopTokens: "token_limit",
		policyStopBudget: "budget_limit",
	}[forcedFinaleReason]
	if finaleStatus == "" {
		finaleStatus = "turn_limit"
	}
	result.Status = finaleStatus
	finaleTurn := input.MaxTurns + 1
	result.Turns = finaleTurn
	if state.frame != "" {
		state.parentFrame = state.frame
	}
	state.frame = fmt.Sprintf("%s/turn/%02d", input.RunID, finaleTurn)
	frames = append(frames, state.frame)
	if err := emit(activityCtx, state, "turn.started", map[string]any{"turn": finaleTurn, "forced_finale": true, "reason": finaleStatus}); err != nil {
		return result, err
	}
	finaleInstruction := map[string]string{
		"time_limit":   "The run's time budget is exhausted and no tools are available. Produce the final answer to the request now, based strictly on the conversation and tool results so far.",
		"token_limit":  "The run's token budget is exhausted and no tools are available. Produce the final answer to the request now, based strictly on the conversation and tool results so far.",
		"budget_limit": "The run's cost budget is exhausted and no tools are available. Produce the final answer to the request now, based strictly on the conversation and tool results so far.",
	}[finaleStatus]
	if finaleInstruction == "" {
		finaleInstruction = "The turn budget is exhausted and no tools are available. Produce the final answer to the request now, based strictly on the conversation and tool results so far."
	}
	finaleMessages := append(slices.Clone(messages), llm.Message{Role: "system", Content: finaleInstruction})
	if err := emit(activityCtx, state, "model.started", map[string]any{"turn": finaleTurn, "forced_finale": true, "reason": finaleStatus, "model": input.Model}); err != nil {
		return result, err
	}
	var finale llm.Completion
	// Same ceiling as the per-turn model call: the forced finale can be as
	// long a generation as any other turn.
	modelCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: ModelCallActivityTimeout, ScheduleToCloseTimeout: ModelCallActivityTimeout + time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	if err := workflow.ExecuteActivity(modelCtx, ActivityCallModel, ModelRequest{Model: input.Model, Messages: finaleMessages, RunID: input.RunID, Turn: finaleTurn, PromptCacheKey: input.TaskID}).Get(ctx, &finale); err != nil {
		failureDetail := boundedFailureDetail(err)
		if eventErr := emit(activityCtx, state, "model.failed", map[string]any{"turn": finaleTurn, "forced_finale": true, "error_type": "activity_failed", "error": failureDetail}); eventErr != nil {
			return result, eventErr
		}
		if eventErr := emit(activityCtx, state, "run.failed", map[string]any{"turn": finaleTurn, "error_type": "finale_model_call_failed", "error": failureDetail}); eventErr != nil {
			return result, eventErr
		}
		result.Status = "failed"
		return result, err
	}
	finaleData := map[string]any{"turn": finaleTurn, "forced_finale": true, "tool_call_count": len(finale.ToolCalls), "finish_reason": finale.Finish, "total_tokens": finale.Usage.TotalTokens}
	maps.Copy(finaleData, modelObservability(finale))
	if err := emit(activityCtx, state, "model.completed", finaleData); err != nil {
		return result, err
	}
	if finale.Content != "" {
		result.Answer = finale.Content
	}
	if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": finaleTurn, "forced_finale": true, "tool_calls": len(finale.ToolCalls)}); err != nil {
		return result, err
	}
	result.TotalTokens = usedTokens + finale.Usage.TotalTokens + subtree.tokens
	result.CostUSD = usedCostUSD + modelCallCostUSD(input, finale.Usage) + subtree.costUSD
	runData := map[string]any{"status": finaleStatus, "turns": finaleTurn, "forced_finale": true, "reason": finaleStatus,
		"total_tokens": result.TotalTokens, "cost_usd": result.CostUSD}
	if result.Answer != "" {
		runData["final_answer"] = true
	}
	if err := emit(activityCtx, state, "run.completed", runData); err != nil {
		return result, err
	}
	if result.Answer != "" {
		if err := emitAgentSummary(activityCtx, state, result.Answer, frames, finaleTurn); err != nil {
			return result, err
		}
	}
	startKnowledgeExtraction(ctx, activityCtx, state, input)
	return result, nil
}

// delegationWidthLimit is the concurrency cap for one run's plan children:
// the team defaults.parallel_limit clamped to the installation maximum.
func delegationWidthLimit(input *RunInput) int {
	if input.DelegationWidth > 0 && input.DelegationWidth < MaxDelegationWidth {
		return input.DelegationWidth
	}
	return MaxDelegationWidth
}

// normalizeTurnBudget honors an explicitly configured turn budget instead of
// resetting it: unset/negative falls back to the default, oversized budgets
// are clamped to the ceiling. The previous behavior (>24 → 8) silently
// discarded every configured budget above 24 (e.g. team defaults of 40).
func normalizeTurnBudget(n int) int {
	if n <= 0 {
		return DefaultMaxTurns
	}
	if n > MaxTurnsCeiling {
		return MaxTurnsCeiling
	}
	return n
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// withoutTool returns the list without the named tool — used to force-allow the
// injected submit_review verdict tool regardless of the agent's deny list.
func withoutTool(values []string, name string) []string {
	filtered := make([]string, 0, len(values))
	for _, item := range values {
		if item != name {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func eventScope(sourceID, project, runID string) string {
	digest := sha256.Sum256([]byte(sourceID + "\x00" + project + "\x00" + runID))
	return hex.EncodeToString(digest[:16])
}

// EventScope returns a stable source/project/run namespace for event IDs and
// workflow identities used by optional integrations.
func EventScope(sourceID, project, runID string) string { return eventScope(sourceID, project, runID) }

// WorkflowID returns the durable Temporal workflow identity for a run —
// top-level and delegated alike (docs/agent-delegation.md §5).
func WorkflowID(sourceID, project, runID string) string {
	digest := sha256.Sum256([]byte(sourceID + "\x00" + project + "\x00" + runID))
	return fmt.Sprintf("agent-run/%x", digest[:16])
}

func toolFailureMessage(name string, err error) string {
	// Argument-validation failures are rejected before execution: no effect,
	// deterministic, and fixable — tell the model exactly what to correct so a
	// schema slip does not burn the run budget as repeated "uncertain" failures.
	var application *temporal.ApplicationError
	if errors.As(err, &application) && application.Type() == "InvalidToolArguments" {
		return "Tool call rejected before execution, no effect: " + application.Message() + ". Re-issue the call with corrected arguments (run_command expects command as an array of strings; file tools expect workspace-relative paths)."
	}
	if strings.HasPrefix(name, "mcp__") || name == "run_command" || name == "write_file" || name == "edit_file" {
		return "Tool call failed; its effect may be uncertain. Do not repeat a consequential action without checking its status."
	}
	return "Tool failed: " + err.Error()
}

const (
	narrativeMaxBytes  = 4096
	failureDetailBytes = 512
)

// BoundedNarrative produces a bounded display representation of an
// agent-authored narrative: credentials are redacted, inline whitespace
// is collapsed (line structure is preserved — narratives are markdown)
// and the text is truncated to narrativeMaxBytes. The second return
// value reports whether truncation happened.
func BoundedNarrative(value string) (string, bool) {
	clean := redactProse(value)
	if len(clean) > narrativeMaxBytes {
		return clean[:narrativeMaxBytes] + "…", true
	}
	return clean, false
}

// boundedFailureDetail reduces an activity error to a bounded, redacted
// description suitable for inclusion in failure events.
func boundedFailureDetail(err error) string {
	detail := redactProse(err.Error())
	if len(detail) > failureDetailBytes {
		return detail[:failureDetailBytes] + "…"
	}
	return detail
}

// modelObservability projects client-side model-call observability onto the
// model.completed event: provider identity, wall-clock latency, token usage
// split, truncation flag, attempt count and content-addressed references for
// the exact request/response payloads. References are hashes only — payload
// contents never enter the event stream.
func modelObservability(completion llm.Completion) map[string]any {
	data := map[string]any{
		"provider":          completion.Provider,
		"latency_ms":        completion.LatencyMs,
		"prompt_tokens":     completion.Usage.PromptTokens,
		"completion_tokens": completion.Usage.CompletionTokens,
		"input_ref":         completion.RequestRef,
		"output_ref":        completion.ResponseRef,
		"truncated":         completion.Truncated(),
		"attempts":          completion.Attempts,
	}
	if completion.Usage.CachedTokens > 0 {
		data["cached_tokens"] = completion.Usage.CachedTokens
	}
	if completion.Transport != "" {
		data["transport"] = completion.Transport
	}
	// A dead stream salvaged by the blocking fallback means the provider
	// generated the answer twice; the marker makes the hidden regeneration
	// visible next to the (inflated) latency.
	if completion.StreamFailed {
		data["stream_failed"] = true
	}
	return data
}

// resultContentRef derives the sha256 content reference for an MCP tool
// result so events stay correlatable without embedding tool output.
func resultContentRef(content string) string {
	digest := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// toolFailureData builds the tool.failed payload. The effect field is the
// reconciliation contract: "none" means the call was rejected before any
// execution (fixable, safe to re-issue), "uncertain" means the failure
// happened at or after the execution boundary, where the operator — not the
// model — must decide whether the side effect landed.

// compactJSON serializes v as JSON, truncating to maxLen characters.
// Returns empty string if marshaling fails.
func compactJSON(v any, maxLen int) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if len(b) > maxLen {
		return string(b[:maxLen]) + "…"
	}
	return string(b)
}

// advertisedTools merges kernel and configured MCP tools, then applies the
// run's tool allowlist when one is set.
func advertisedTools(input *RunInput) []llm.ToolDef {
	tools := append(KernelTools(), input.Tools...)
	if len(input.ToolAllowlist) == 0 && len(input.DenyTools) == 0 {
		return tools
	}
	allowed := make(map[string]bool, len(input.ToolAllowlist))
	for _, name := range input.ToolAllowlist {
		allowed[name] = true
	}
	denied := make(map[string]bool, len(input.DenyTools))
	for _, name := range input.DenyTools {
		denied[name] = true
	}
	var filtered []llm.ToolDef
	for _, tool := range tools {
		if denied[tool.Name] {
			continue
		}
		if len(input.ToolAllowlist) > 0 && !allowed[tool.Name] {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

// mcpServerForTool attributes an mcp__ tool to its server for the event
// stream: namespaced tools carry the server id, legacy mcp__<tool> names fall
// back to the run's single-server field.
func mcpServerForTool(name, legacy string) string {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return legacy
	}
	if server, _, found := strings.Cut(rest, "__"); found {
		return server
	}
	return legacy
}

const prefix = "mcp__"

// readOnlyTools are tools that never produce side effects — they only
// observe state. If they fail, there's nothing to reconcile.
var readOnlyTools = map[string]bool{
	"read_file": true, "mcp__read_file": true,
	"list_directory": true, "mcp__list_directory": true,
	"search": true, "mcp__search": true,
	"glob": true, "mcp__glob": true,
	"grep": true, "mcp__grep": true,
}

// isReadOnlyTool classifies a tool as side-effect free. Namespaced MCP tools
// (mcp__<server>__<tool>) classify by their bare tool name.
func isReadOnlyTool(tool string) bool {
	if readOnlyTools[tool] {
		return true
	}
	rest, ok := strings.CutPrefix(tool, prefix)
	if !ok {
		return false
	}
	if _, bare, found := strings.Cut(rest, "__"); found {
		return readOnlyTools[prefix+bare]
	}
	return false
}

// Policy stop reasons (docs/org-structure.md §24): resolved from the org
// position at run start and enforced at turn boundaries.
const (
	policyStopTime   = "policy.time_exhausted"
	policyStopTokens = "policy.tokens_exhausted"
	policyStopBudget = "policy.budget_exhausted"
)

// policyLimitData shapes the policy.limit event: what was exhausted, the
// limits in force and what the run had consumed when it stopped.
func policyLimitData(reason string, turn int, input RunInput, usedTokens int, usedCostUSD float64) map[string]any {
	return map[string]any{
		"reason": reason, "turn": turn,
		"used_tokens": usedTokens, "token_budget": input.TokenBudget,
		"used_cost_usd": usedCostUSD, "max_budget_usd": input.MaxBudgetUSD,
		"timeout_seconds": input.TimeoutSeconds,
	}
}

// policyStopReason reports the first exhausted budget after a model call.
func policyStopReason(input RunInput, usedTokens int, usedCostUSD float64) string {
	if input.TokenBudget > 0 && usedTokens >= input.TokenBudget {
		return policyStopTokens
	}
	if input.MaxBudgetUSD > 0 && usedCostUSD >= input.MaxBudgetUSD {
		return policyStopBudget
	}
	return ""
}

// modelCallCostUSD prices one model call with the run-config-resolved prices.
// Zero prices mean no price is configured: the USD budget degrades to the
// token cap and this returns 0. Cached input tokens are billed at half the
// prompt price — a conservative default covering the common 10%..50% discounts
// without extending the run config with a per-model cached price.
func modelCallCostUSD(input RunInput, usage llm.Usage) float64 {
	cached := min(usage.CachedTokens, usage.PromptTokens)
	fresh := usage.PromptTokens - cached
	return (float64(fresh)/1000)*input.ModelPromptPricePer1k +
		(float64(cached)/1000)*input.ModelPromptPricePer1k*cachedPriceFactor +
		(float64(usage.CompletionTokens)/1000)*input.ModelCompPricePer1k
}

// cachedPriceFactor is the billing factor applied to cached prompt tokens.
const cachedPriceFactor = 0.5

// runCompletedTotals shapes the normal-completion run.completed payload: turn
// count plus the run's token and cost totals (all model calls, finale
// included) so UIs can show what the run spent without replaying events.
func runCompletedTotals(turns, totalTokens int, costUSD float64) map[string]any {
	return map[string]any{"turns": turns, "total_tokens": totalTokens, "cost_usd": costUSD}
}

// sandboxAutoApprovedTools are consequential workspace tools covered by the
// kernel's sandbox.workspace.v1 policy (the list is prepared by PrepareRun):
// the project workspace is an isolated sandbox root, so running commands and
// writing files there needs no human round-trip. An explicit approval policy
// (approval_mode: tools) still forces confirmation — approvalRequired takes
// precedence over auto-approval.
var sandboxAutoApprovedTools = map[string]bool{
	"run_command": true, "write_file": true, "edit_file": true,
}

// policySafeTools are exempt from policy-mandated tool approval
// (approval_mode: tools): observation, knowledge-recording and escalation
// tools the policy regime itself relies on. Consequential tools —
// run_command, write_file/edit_file, delegate, trigger writes, skill_propose,
// mcp__ tools — still need a human decision. Read-only MCP tools are exempt
// via isReadOnlyTool.
var policySafeTools = map[string]bool{
	"echo": true, "remember": true, "request_approval": true, "ask_human": true,
	"human_contacts": true, "list_triggers": true,
	"skill_search": true, "skill_inspect": true, "skill_validate": true,
	"skill_history": true, "skill_executions": true, "skill_memory": true,
	"skill_diff": true, "skill_evaluate": true,
}

// policySafeTool reports whether a tool may run without approval when the
// effective policy demands approval for consequential tools.
func policySafeTool(name string) bool {
	return policySafeTools[name] || isReadOnlyTool(name)
}

func toolFailureData(operationID, argumentsHash, tool string, err error) map[string]any {
	data := map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": tool, "error_type": "activity_failed"}
	if isReadOnlyTool(tool) {
		// Read-only tools have no side effect regardless of where they fail.
		data["effect"] = "none"
	} else {
		var application *temporal.ApplicationError
		if errors.As(err, &application) && application.NonRetryable() {
			data["effect"] = "none"
		} else {
			data["effect"] = "uncertain"
		}
	}
	return data
}

// emitAgentSummary records the agent-authored final answer as derived data:
// it never replaces the execution record and always carries the frames it
// is derived from.
func emitAgentSummary(ctx workflow.Context, state *eventState, answer string, frames []string, turns int) error {
	summary, truncated := BoundedNarrative(answer)
	return emit(ctx, state, "agent.summary", map[string]any{"kind": "narrative", "answer": summary, "truncated": truncated, "turns": turns, "derived_from": frames})
}

func systemPromptForRole(role string) string {
	base := "You are a careful project agent. Use tools when useful and converge to a final answer before the turn budget runs out. Every command sandbox has a writable /scratch directory (HOME and TMPDIR point there); use it for build and package caches, and never write caches into the workspace, which may be read-only for your role. Successful verification and build command outcomes are recorded as knowledge automatically; do not restate them with remember. Call remember only when you are highly confident in a durable conclusion that goes beyond the recorded execution results, and include evidence refs. Treat retrieved memory as fallible and respect cautions."
	if role == "" {
		return base
	}
	return base + " Your assigned team role is " + role + "; stay within that responsibility and ground handoffs in observed evidence."
}

// skillsPromptSection renders the agent's skills as a compact canonical
// section of the system prompt. Skills describe HOW the work should be done;
// per-run experience arrives separately as knowledge hints, never here
// (docs/living-skills.md §2, §15).
func skillsPromptSection(refs []SkillRef) string {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Skills\nVersioned capability packages bound to you: each has a SKILL.md procedure and a manifest contract stored in the skill registry (not files in this workspace). When a task matches a skill, follow its procedure; skill_inspect shows the full text. Skill knowledge is contextual: record discoveries with remember (include skill_id and capability). If a task reveals a new repeatable capability, propose it with skill_propose — never hand-write skill files in the workspace.")
	for _, ref := range refs {
		b.WriteString("\n\n")
		b.WriteString(ref.Digest)
	}
	return b.String()
}

func awaitApproval(ctx workflow.Context, operationID string, timeout time.Duration) (bool, Approval, bool, error) {
	channel := workflow.GetSignalChannel(ctx, ApprovalSignal)
	var approval Approval
	matched := false
	ok, err := workflow.AwaitWithTimeout(ctx, timeout, func() bool {
		for channel.ReceiveAsync(&approval) {
			if approval.OperationID == operationID {
				matched = true
				return true
			}
		}
		return false
	})
	if err != nil {
		return false, approval, false, err
	}
	if !ok {
		return false, approval, true, nil
	}
	if !matched {
		return false, approval, false, errors.New("approval wait completed without matching operation")
	}
	return approval.Approved, approval, false, nil
}

type eventState struct {
	run             RunInput
	frame           string
	parentFrame     string
	sequence        int
	eventScope      string
	previousEventID string
}

func emit(ctx workflow.Context, state *eventState, kind string, data map[string]any) error {
	state.sequence++
	if data == nil {
		data = map[string]any{}
	}
	data["frame_id"] = state.frame
	data["parent_frame_id"] = state.parentFrame
	if state.run.ParentRunID != "" {
		data["parent_run_id"] = state.run.ParentRunID
	}
	data["sequence"] = state.sequence
	eventID := fmt.Sprintf("%s/event/%06d", state.eventScope, state.sequence)
	if state.previousEventID != "" {
		data["caused_by"] = []string{state.previousEventID}
	}
	item := observation.Event{
		Schema: observation.Schema, EventID: eventID, OccurredAt: workflow.Now(ctx).UTC(),
		Source:  observation.Source{ID: state.run.SourceID, Integration: "temporality-agent-kernel", Version: "0.1"},
		Context: observation.Context{Project: state.run.Project, Run: state.run.RunID, Task: state.run.TaskID, Actor: observation.Actor{ID: state.run.ActorID, Type: "agent"}, ParentEventID: state.previousEventID},
		Type:    kind, Data: data,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityRecordEvent, item).Get(ctx, nil); err != nil {
		return err
	}
	state.previousEventID = eventID
	return nil
}

func emitKnowledge(ctx workflow.Context, state *eventState, id, proposition, operationID string, args map[string]any) error {
	data := map[string]any{"knowledge_id": id, "proposition": proposition, "kind": "claim", "operation_id": operationID, "arguments_hash": operationArgumentsHash(args)}
	// Skill linkage: knowledge born from applying a skill points at the skill
	// (and optionally the capability) so memory stays navigable per skill
	// without ever modifying the skill itself (docs/living-skills.md §14-16).
	if skillID, _ := args["skill_id"].(string); skillID != "" {
		data["skill_id"] = skillID
	}
	if capability, _ := args["capability"].(string); capability != "" {
		data["capability"] = capability
	}
	var evidence []observation.Evidence
	if raw, ok := args["evidence"].([]any); ok {
		for _, value := range raw {
			if ref, ok := value.(string); ok && ref != "" {
				evidence = append(evidence, observation.Evidence{Ref: ref, Type: "artifact"})
			}
		}
	}
	return emitKnowledgeEvent(ctx, state, "knowledge.proposed", data, evidence)
}

// emitExecutionObservation records a heuristic knowledge event for a
// successful verification or build command. The observation identity is
// project-scoped and stable, so the authoritative projection decides the
// semantics: an unknown command outcome is proposed once, a repeated success
// of a proposed observation confirms it, and later successes record reuse.
// Terminal states (corrected, superseded, invalidated) emit nothing: the
// runtime tool.completed event already carries the fact.
func emitExecutionObservation(ctx workflow.Context, state *eventState, proposal *executionObservation, operationID, argumentsHash string) error {
	lookupCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2}})
	var lookup KnowledgeLookupResult
	if err := workflow.ExecuteActivity(lookupCtx, ActivityKnowledgeLookup, KnowledgeLookupQuery{Project: state.run.Project, KnowledgeID: proposal.KnowledgeID}).Get(ctx, &lookup); err != nil {
		// Without an authoritative lookup the kernel cannot know whether this
		// observation already exists; proposing blindly could duplicate a node.
		// The execution fact remains recorded as a runtime event.
		return nil
	}
	evidence := []observation.Evidence{{Ref: operationID, Type: "execution"}}
	switch {
	case !lookup.Exists:
		data := map[string]any{"knowledge_id": proposal.KnowledgeID, "proposition": proposal.Proposition, "kind": "observation", "policy_id": proposal.Policy, "command": proposal.Command, "command_class": proposal.Class, "exit_code": 0, "operation_id": operationID, "arguments_hash": argumentsHash}
		return emitKnowledgeEvent(ctx, state, "knowledge.proposed", data, evidence)
	case lookup.State == "proposed" || lookup.State == "challenged":
		data := map[string]any{"knowledge_id": proposal.KnowledgeID, "rule": ReverificationRule, "operation_id": operationID, "arguments_hash": argumentsHash, "command": proposal.Command}
		return emitKnowledgeEvent(ctx, state, "knowledge.confirmed", data, evidence)
	case lookup.State == "confirmed":
		data := map[string]any{"knowledge_id": proposal.KnowledgeID, "rule": ReuseRule, "operation_id": operationID, "arguments_hash": argumentsHash, "command": proposal.Command}
		return emitKnowledgeEvent(ctx, state, "knowledge.used", data, evidence)
	}
	return nil
}

// KnowledgeExtractionWorkflowName is the detached extraction workflow a
// finished run hands off to (see startKnowledgeExtraction).
const KnowledgeExtractionWorkflowName = "KnowledgeExtraction"

// KnowledgeExtractionInput carries the finished run's identity plus the
// continuation point of its event chain: the child appends to the same journal
// scope, so it must know the last sequence number and event id the parent used
// instead of restarting from event/000001.
type KnowledgeExtractionInput struct {
	Run             RunInput
	Scope           string
	ParentFrame     string
	Sequence        int
	PreviousEventID string
}

// startKnowledgeExtraction hands the finished run over to a detached
// KnowledgeExtraction child workflow. The run's terminal status must not wait
// for the extraction model call (measured at 43-84s in live runs): everyone —
// the run status API, the SSE stream, the chat UI — learns the run is done the
// moment this workflow completes, so extraction runs asynchronously and
// continues the run's event chain on its own. Only the child's start is
// awaited (a scheduling guarantee); its completion is best-effort, exactly as
// extraction failures were best-effort before. If the child cannot even be
// started, a knowledge.extraction.failed event records the miss so the journal
// never shows a silently skipped extraction.
func startKnowledgeExtraction(ctx workflow.Context, activityCtx workflow.Context, state *eventState, input RunInput) {
	if input.SkipKnowledge {
		return
	}
	childInput := KnowledgeExtractionInput{
		Run:             input,
		Scope:           state.eventScope,
		ParentFrame:     state.frame,
		Sequence:        state.sequence,
		PreviousEventID: state.previousEventID,
	}
	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:            extractionWorkflowID(input.SourceID, input.Project, input.RunID),
		ParentClosePolicy:     enums.PARENT_CLOSE_POLICY_ABANDON,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	})
	future := workflow.ExecuteChildWorkflow(childCtx, KnowledgeExtractionWorkflowName, childInput)
	var exec workflow.Execution
	if startErr := future.GetChildWorkflowExecution().Get(ctx, &exec); startErr != nil {
		_ = emit(activityCtx, state, extractionFailedEvent, map[string]any{
			"extraction_id":     extractionIdentity(input.RunID),
			"extractor_version": ExtractorVersion,
			"error_type":        "start_failed",
			"error":             boundedFailureDetail(startErr),
		})
	}
}

// KnowledgeExtractionWorkflow replays a finished run through the knowledge
// extractor. It is fully detached from the run workflow: the run completed
// before this started and its result is already with the user. The workflow
// only produces best-effort knowledge events; every failure is contained and
// recorded as knowledge.extraction.failed.
func KnowledgeExtractionWorkflow(ctx workflow.Context, input KnowledgeExtractionInput) error {
	if input.Run.SkipKnowledge {
		return nil
	}
	// Same default as AgentRun: the handoff always carries the parent's source
	// id, but a defensive default keeps standalone executions valid.
	if input.Run.SourceID == "" {
		input.Run.SourceID = "temporality-agent-kernel"
	}
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    4 * time.Minute,
		ScheduleToCloseTimeout: 5 * time.Minute,
		RetryPolicy:            &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3},
	})
	state := &eventState{
		run:             input.Run,
		frame:           input.Run.RunID + "/extraction",
		parentFrame:     input.ParentFrame,
		sequence:        input.Sequence,
		eventScope:      input.Scope,
		previousEventID: input.PreviousEventID,
	}
	runKnowledgeExtraction(ctx, activityCtx, state, input.Run)
	return nil
}

// extractionWorkflowID is deterministic per run and extractor version: a
// duplicate start (parent workflow replay) is rejected as already exists,
// while bumping ExtractorVersion deliberately opens a new id so every
// finished run becomes eligible for re-extraction under the new version.
func extractionWorkflowID(sourceID, project, runID string) string {
	digest := sha256.Sum256([]byte(sourceID + "\x00" + project + "\x00" + runID + "\x00" + ExtractorVersion))
	return fmt.Sprintf("knowledge-extraction/%x", digest[:16])
}

// runKnowledgeExtraction replays the finished run through the knowledge
// extractor (docs/knowledge-extraction.md): one model call over the compact
// trajectory produces 0..N candidates that are emitted as knowledge.proposed
// with evidence, or strengthen already-projected nodes. Extraction is strictly
// best-effort — any failure is recorded as knowledge.extraction.failed and the
// run's own result stays untouched.
func runKnowledgeExtraction(ctx workflow.Context, activityCtx workflow.Context, state *eventState, input RunInput) {
	if input.SkipKnowledge {
		return
	}
	extractionID := extractionIdentity(input.RunID)
	if err := emit(activityCtx, state, extractionStartedEvent, map[string]any{"extraction_id": extractionID, "extractor_version": ExtractorVersion}); err != nil {
		return
	}
	extractCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 3 * time.Minute, ScheduleToCloseTimeout: 4 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	var extraction KnowledgeExtractResult
	if err := workflow.ExecuteActivity(extractCtx, ActivityExtractKnowledge, KnowledgeExtractRequest{Project: input.Project, RunID: input.RunID, TaskID: input.TaskID, ActorID: input.ActorID, Prompt: input.Prompt, ExtractionID: extractionID, Model: input.Model}).Get(ctx, &extraction); err != nil {
		_ = emit(activityCtx, state, extractionFailedEvent, map[string]any{"extraction_id": extractionID, "extractor_version": ExtractorVersion, "error": boundedFailureDetail(err)})
		return
	}
	if extraction.Skipped {
		_ = emit(activityCtx, state, extractionCompletedEvent, map[string]any{"extraction_id": extractionID, "extractor_version": ExtractorVersion, "skipped": "already_extracted"})
		return
	}
	// Hint feedback closes the retrieval loop: every hint offered to this run is
	// classified as used or ignored from the run's own output, so reuse counts
	// and aging reflect actual influence (docs/knowledge-evolution.md §2 Hint).
	for _, hint := range extraction.HintFeedback {
		eventType := "hint.ignored"
		data := map[string]any{"hint_id": hint.HintID, "knowledge_id": hint.KnowledgeID, "proposition": hint.Proposition, "matcher": "hint-usage-lexical.v1"}
		if hint.Used {
			eventType = "hint.used"
			data["matched_by"] = hint.MatchedBy
		}
		if err := emit(activityCtx, state, eventType, data); err != nil {
			return
		}
	}
	lookupCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2}})
	proposed, strengthened, challenged := 0, 0, 0
	for _, candidate := range extraction.Candidates {
		if emitExtractedKnowledge(ctx, activityCtx, lookupCtx, state, input, extractionID, candidate) {
			if candidate.Existing {
				strengthened++
			} else {
				proposed++
			}
		}
		if candidate.Contradicts != "" && emitContradictionChallenge(ctx, activityCtx, lookupCtx, state, input, extractionID, candidate) {
			challenged++
		}
	}
	aged := 0
	for _, item := range extraction.Aging {
		if emitAgingChallenge(ctx, activityCtx, lookupCtx, state, input, extractionID, item) {
			aged++
		}
	}
	_ = emit(activityCtx, state, extractionCompletedEvent, map[string]any{
		"extraction_id":      extractionID,
		"extractor_version":  ExtractorVersion,
		"candidates_count":   len(extraction.Candidates),
		"proposed":           proposed,
		"strengthened":       strengthened,
		"challenged":         challenged,
		"aged":               aged,
		"duplicates_skipped": extraction.Duplicates,
		"invalid_skipped":    extraction.Invalid,
		"duration_ms":        extraction.DurationMs,
		"model":              extraction.Model,
	})
}

// emitExtractedKnowledge records one candidate. New ids are proposed with
// their evidence; ids that already exist are strengthened — a fresh lookup
// decides between confirming a proposal and recording reuse of confirmed
// knowledge, mirroring the execution-observation semantics. Terminal states
// emit nothing: the runtime events already carry the fact.
func emitExtractedKnowledge(ctx workflow.Context, activityCtx workflow.Context, lookupCtx workflow.Context, state *eventState, input RunInput, extractionID string, candidate KnowledgeCandidate) bool {
	evidence := make([]observation.Evidence, 0, len(candidate.Evidence))
	for _, ref := range candidate.Evidence {
		evidence = append(evidence, observation.Evidence{Ref: ref, Type: "event"})
	}
	if !candidate.Existing {
		data := map[string]any{"knowledge_id": candidate.KnowledgeID, "proposition": candidate.Proposition, "kind": candidate.Kind, "producer": "knowledge-extractor", "extractor_version": ExtractorVersion, "extraction_id": extractionID}
		if candidate.Confidence > 0 {
			data["confidence"] = candidate.Confidence
		}
		return emitKnowledgeEvent(activityCtx, state, "knowledge.proposed", data, evidence) == nil
	}
	var lookup KnowledgeLookupResult
	if err := workflow.ExecuteActivity(lookupCtx, ActivityKnowledgeLookup, KnowledgeLookupQuery{Project: input.Project, KnowledgeID: candidate.KnowledgeID}).Get(ctx, &lookup); err != nil {
		// Without an authoritative lookup the kernel cannot know the node's
		// current state; strengthening blindly could break its lifecycle.
		return false
	}
	switch lookup.State {
	case "proposed", "challenged":
		data := map[string]any{"knowledge_id": candidate.KnowledgeID, "proposition": candidate.Proposition, "rule": ExtractionReverificationRule, "extractor_version": ExtractorVersion, "extraction_id": extractionID}
		return emitKnowledgeEvent(activityCtx, state, "knowledge.confirmed", data, evidence) == nil
	case "confirmed":
		data := map[string]any{"knowledge_id": candidate.KnowledgeID, "proposition": candidate.Proposition, "rule": ExtractionReuseRule, "extractor_version": ExtractorVersion, "extraction_id": extractionID}
		return emitKnowledgeEvent(activityCtx, state, "knowledge.used", data, evidence) == nil
	}
	return false
}

// emitContradictionChallenge marks knowledge the run directly disproved
// (docs/knowledge-evolution.md, scenario C). A fresh lookup guards the
// transition — a terminal or unknown node is skipped instead of poisoning the
// projection. The corrected fact itself was proposed by the caller separately;
// the challenge keeps the stale item alive as "challenged" (offered with
// caution) until a human or a later re-derivation settles it.
func emitContradictionChallenge(ctx workflow.Context, activityCtx workflow.Context, lookupCtx workflow.Context, state *eventState, input RunInput, extractionID string, candidate KnowledgeCandidate) bool {
	var lookup KnowledgeLookupResult
	if err := workflow.ExecuteActivity(lookupCtx, ActivityKnowledgeLookup, KnowledgeLookupQuery{Project: input.Project, KnowledgeID: candidate.Contradicts}).Get(ctx, &lookup); err != nil {
		return false
	}
	if !lookup.Exists || terminalKnowledgeState(lookup.State) {
		return false
	}
	data := map[string]any{
		"knowledge_id":      candidate.Contradicts,
		"proposition":       lookup.Proposition,
		"rule":              ExtractionContradictionRule,
		"contradicted_by":   candidate.KnowledgeID,
		"reason":            "contradicted by extraction: " + candidate.Proposition,
		"extractor_version": ExtractorVersion,
		"extraction_id":     extractionID,
	}
	evidence := make([]observation.Evidence, 0, len(candidate.Evidence))
	for _, ref := range candidate.Evidence {
		evidence = append(evidence, observation.Evidence{Ref: ref, Type: "event"})
	}
	return emitKnowledgeEvent(activityCtx, state, "knowledge.challenged", data, evidence) == nil
}

// emitAgingChallenge retires a stale proposal (rule aging.v1): knowledge that
// stayed "proposed" past AgingThreshold without a single use. Only still-proposed
// items are challenged — anything confirmed, already challenged or terminal
// between the sweep and now must not be touched. Challenged items keep being
// offered with caution and re-confirm on the next re-derivation, so aging is
// reversible, not a death sentence.
func emitAgingChallenge(ctx workflow.Context, activityCtx workflow.Context, lookupCtx workflow.Context, state *eventState, input RunInput, extractionID string, aging AgingCandidate) bool {
	var lookup KnowledgeLookupResult
	if err := workflow.ExecuteActivity(lookupCtx, ActivityKnowledgeLookup, KnowledgeLookupQuery{Project: input.Project, KnowledgeID: aging.KnowledgeID}).Get(ctx, &lookup); err != nil {
		return false
	}
	if !lookup.Exists || lookup.State != "proposed" {
		return false
	}
	data := map[string]any{
		"knowledge_id":      aging.KnowledgeID,
		"proposition":       lookup.Proposition,
		"rule":              AgingRule,
		"reason":            fmt.Sprintf("unconfirmed for %.0f days without any use", aging.AgeDays),
		"extractor_version": ExtractorVersion,
		"extraction_id":     extractionID,
	}
	return emitKnowledgeEvent(activityCtx, state, "knowledge.challenged", data, nil) == nil
}

func emitKnowledgeEvent(ctx workflow.Context, state *eventState, eventType string, data map[string]any, evidence []observation.Evidence) error {
	state.sequence++
	// Kernel proposals are born project-scoped (docs/knowledge-evolution.md
	// §5: locality first); wider scopes happen only via knowledge.promoted.
	if eventType == "knowledge.proposed" && data["scope_kind"] == nil {
		data["scope_kind"] = "project"
	}
	data["frame_id"] = state.frame
	data["parent_frame_id"] = state.parentFrame
	if state.run.ParentRunID != "" {
		data["parent_run_id"] = state.run.ParentRunID
	}
	data["sequence"] = state.sequence
	eventID := fmt.Sprintf("%s/event/%06d", state.eventScope, state.sequence)
	if state.previousEventID != "" {
		data["caused_by"] = []string{state.previousEventID}
	}
	item := observation.Event{Schema: observation.Schema, EventID: eventID, OccurredAt: workflow.Now(ctx).UTC(), Source: observation.Source{ID: state.run.SourceID, Integration: "temporality-agent-kernel", Version: "0.1"}, Context: observation.Context{Project: state.run.Project, Run: state.run.RunID, Task: state.run.TaskID, Actor: observation.Actor{ID: state.run.ActorID, Type: "agent"}, ParentEventID: state.previousEventID}, Type: eventType, Data: data, Evidence: evidence}
	if err := workflow.ExecuteActivity(ctx, ActivityRecordEvent, item).Get(ctx, nil); err != nil {
		return err
	}
	state.previousEventID = eventID
	return nil
}

func KernelTools() []llm.ToolDef {
	return []llm.ToolDef{
		{Name: "echo", Description: "Return a short text value for debugging the harness tool path", Parameters: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}},
		{Name: "remember", Description: "Record a high-confidence durable conclusion with optional evidence refs. Verification and build outcomes are recorded automatically; use this only for conclusions you are confident in and can ground in evidence. If the conclusion came from applying a skill, include skill_id and capability", Parameters: map[string]any{"type": "object", "properties": map[string]any{"proposition": map[string]any{"type": "string"}, "evidence": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "skill_id": map[string]any{"type": "string", "description": "Skill this knowledge came from, e.g. deploy-service"}, "capability": map[string]any{"type": "string", "description": "Specific capability of the skill, e.g. verify"}}, "required": []string{"proposition"}}},
		{Name: "request_approval", Description: "Pause this run and request a human decision before a consequential action", Parameters: map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}, "required": []string{"action"}}},
		{Name: "ask_human", Description: "Ask a human a question when you cannot proceed without additional context or a decision. The question must be self-contained: what task you are running, what you have established, what exactly is missing and which options exist. Prefer short structured options over open-ended questions. The run pauses until an answer arrives or the timeout expires.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"question": map[string]any{"type": "string", "description": "The exact question for the human, self-contained"}, "recipient": map[string]any{"type": "string", "description": "Logical recipient: a workspace user id/name (user:ruslan), role:admin, org:<unit-id>, or project_owner. Defaults to the operator who started this run"}, "context": map[string]any{"type": "string", "description": "Brief human-facing background: what you are doing and what you already established"}, "options": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Short answer variants to choose from"}, "timeout_sec": map[string]any{"type": "integer", "description": "How long to wait for the answer"}, "timeout_policy": map[string]any{"type": "string", "enum": []string{"fallback", "fail", "retry", "escalate", "cancel"}, "description": "What happens on timeout: fallback (default) proceeds with the safest option, fail aborts the tool call, retry asks once more, escalate re-asks the project owner, cancel stops the run"}}, "required": []string{"question"}}},
		{Name: "human_contacts", Description: "Find who to ask before ask_human: search workspace users by name, role or org unit, or resolve an escalation target with escalate_for (the nearest org level above that user with active people, falling back to installation admins). Returns contact cards with user_id, role, org unit and enabled channel types — channel addresses are never exposed, delivery is automatic", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "Optional substring matched against user name or id"}, "role": map[string]any{"type": "string", "description": "Filter by workspace role: admin, operator, writer, reader"}, "org_unit_id": map[string]any{"type": "string", "description": "Filter by org unit id (the exact unit, not its subtree)"}, "escalate_for": map[string]any{"type": "string", "description": "User id or name: resolve who to escalate to when that user cannot answer"}}}},
		{Name: "send_file", Description: "Deliver a file you produced in the workspace to a human: it is uploaded to their enabled messaging channels (Matrix, Telegram) and appears as a download in the product UI. Use it to hand over reports, archives, generated documents — not for files the user can already find in the repo", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "Workspace path of the file, e.g. /workspace/report.md"}, "note": map[string]any{"type": "string", "description": "One-line human-facing caption for the file"}, "recipient": map[string]any{"type": "string", "description": "Logical recipient like ask_human: user id/name, role:admin, org:<unit>, project_owner. Defaults to the operator who started this run"}}, "required": []string{"path"}}},
		{Name: "list_triggers", Description: "List all configured triggers (schedules, webhooks, event listeners) for this project", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "create_trigger", Description: "Create a new trigger to automatically launch agent runs. Types: schedule (cron-based), webhook (HTTP endpoint), event (reacts to journal events).", Parameters: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "type": map[string]any{"type": "string", "enum": []string{"schedule", "webhook", "event"}}, "cron": map[string]any{"type": "string", "description": "Cron expression for schedule triggers, e.g. '0 9 * * 1-5'"}, "prompt": map[string]any{"type": "string", "description": "The prompt sent to the agent when the trigger fires"}, "path": map[string]any{"type": "string", "description": "URL path for webhook triggers"}, "event_type": map[string]any{"type": "string", "description": "Event type to react to for event triggers, e.g. 'tool.failed'"}, "agent_id": map[string]any{"type": "string", "description": "Agent to use (optional, uses default if empty)"}}, "required": []string{"name", "type", "prompt"}}},
		{Name: "update_trigger", Description: "Update an existing trigger's configuration (enable/disable, change cron, update prompt, etc.)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"trigger_id": map[string]any{"type": "string"}, "enabled": map[string]any{"type": "boolean"}, "cron": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}}, "required": []string{"trigger_id"}}},
		{Name: "delete_trigger", Description: "Delete a trigger by ID", Parameters: map[string]any{"type": "object", "properties": map[string]any{"trigger_id": map[string]any{"type": "string"}}, "required": []string{"trigger_id"}}},
		{Name: "skill_search", Description: "List living skills in this project: versioned capability packages (SKILL.md instructions + manifest contract) stored in the skill registry, not files in the repo. Returns id, name, version, capabilities, tools", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "Optional space-separated keywords; a skill matches when any keyword occurs in id, name, capability or tool (case-insensitive); results are ranked by keyword coverage"}}}},
		{Name: "skill_inspect", Description: "Show a skill's full SKILL.md and manifest contract (capabilities, tools, runtime, preconditions, postconditions, evidence)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_validate", Description: "Validate a skill's manifest and return issues", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_history", Description: "List a skill's versions", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_executions", Description: "List recent executions of a skill (runs with this skill attached)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_memory", Description: "List knowledge recorded from a skill's executions (memory stays a separate temporal layer; it never modifies the skill)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_propose", Description: "Propose a new living skill from a natural-language description of a repeatable capability. The skill builder extracts the contract (procedure, capabilities, tools, runtime, constraints) referencing only tools that actually exist, and saves a 0.x draft pending human review in the Skills UI. If the skill already exists, the draft becomes its next version. Never hand-write skill files in the repo — the registry is the only source of truth", Parameters: map[string]any{"type": "object", "properties": map[string]any{"description": map[string]any{"type": "string", "description": "What the skill should do, when to use it, and any constraints — the same way you would describe it to a human"}}, "required": []string{"description"}}},
		{Name: "skill_evaluate", Description: "Run the stored evaluation suite of a skill against its current revision or a specific version (e.g. a pending draft). Each case is a grounded model call checked against expected answer patterns; results are recorded as an evaluation run. Use it to validate a proposal before asking a human to apply it", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}, "version": map[string]any{"type": "string", "description": "Optional specific version to test (a draft); defaults to the current revision"}}, "required": []string{"skill_id"}}},
		{Name: "skill_diff", Description: "Show what changed between two skill versions (manifest contract + SKILL.md). Defaults to current revision vs the newest draft proposal — use it to review an evolution proposal before recommending apply", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}, "from_version": map[string]any{"type": "string", "description": "Optional base version; defaults to the current revision"}, "to_version": map[string]any{"type": "string", "description": "Optional target version; defaults to the newest draft"}}, "required": []string{"skill_id"}}},
		{Name: "delegate", Description: "Delegate a self-contained subtask to another agent and wait for its result. The delegated agent runs with its own model, system prompt and capabilities — you cannot grant it anything beyond what it already has. Give a complete, self-contained prompt: everything the agent needs to know must be in it", Parameters: map[string]any{"type": "object", "properties": map[string]any{"agent_id": map[string]any{"type": "string", "description": "Agent to delegate to"}, "prompt": map[string]any{"type": "string", "description": "Self-contained subtask description"}, "max_turns": map[string]any{"type": "integer", "description": "Turn budget for the delegated run, 1–100 (default 8)"}}, "required": []string{"agent_id", "prompt"}}},
		{Name: "plan", Description: "Execute a plan of dependent subtasks as a DAG across agents, in one call. Each task names an agent and a self-contained prompt; depends_on lists task ids that must complete successfully first — their final answers are appended to the dependent task's prompt automatically, or place them inline with {{task-id.answer}} placeholders. Tasks without shared dependencies run in parallel. A failed task skips only its transitive dependents; independent branches still finish. A task with review_of is an acceptance gate: its run receives the submit_review tool, and a rework verdict sends the rejected tasks back with concrete feedback and re-runs their consumers (bounded by max_rework). Use this instead of several delegate calls whenever the work has ordering dependencies, can fan out, or needs review gates.", Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal":       map[string]any{"type": "string", "description": "Short human-readable goal of the plan"},
				"max_rework": map[string]any{"type": "integer", "description": "How many times a reviewer may send a task back for rework, 0-4 (default 2)"},
				"tasks": map[string]any{
					"type":        "array",
					"description": "Plan tasks; ids must be unique, depends_on references task ids",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":         map[string]any{"type": "string", "description": "Task id, 1-64 chars [a-zA-Z0-9._-], referenced by depends_on"},
							"agent_id":   map[string]any{"type": "string", "description": "Agent to run this task"},
							"prompt":     map[string]any{"type": "string", "description": "Self-contained instruction; {{dep-id.answer}} inlines a dependency's result"},
							"depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Task ids that must complete before this one"},
							"review_of":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Acceptance gate: this task reviews these task ids (auto-added to depends_on). Its run receives the submit_review tool; a rework verdict reopens the rejected tasks with the feedback"},
							"max_turns":  map[string]any{"type": "integer", "description": "Turn budget for this task's run, 1–100 (default 8)"},
						},
						"required": []string{"id", "agent_id", "prompt"},
					},
					"required": []string{"tasks"},
				},
			},
			"required": []string{"tasks"},
		}},
	}
}
