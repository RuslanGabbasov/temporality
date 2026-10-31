package agent

import (
	"context"
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
	"github.com/temporality-project/temporality/kernel/sandbox"
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
	ActivityWriteTeamContract = "kernel.team_contract_write"
	ApprovalSignal            = "kernel.approval"
	NudgeSignal               = "kernel.nudge"
	UserNoteSignal            = "kernel.user_note"

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
	// DelegatedTurnsFloor keeps a delegating model's budget guess from
	// crippling a subtask: children dying at 9–15 turns mid-work were marked
	// incomplete and the lead burned its own budget on rework rounds. The
	// card (when configured) still caps what the child may use.
	DelegatedTurnsFloor = 24
)

type RunInput struct {
	RunID   string `json:"run_id"`
	Project string `json:"project"`
	TaskID  string `json:"task_id"`
	ActorID string `json:"actor_id"`
	Prompt  string `json:"prompt"`
	// PriorTail carries the journal tail of the interrupted previous attempt
	// of the same task (failed/cancelled): rendered as a system block so a
	// «давай продолжим» run resumes instead of starting blind.
	PriorTail string `json:"prior_tail,omitempty"`
	// Title is the owning task's display title. When present it becomes the
	// run title directly — no model call, so a slow provider never leaves the
	// Runs list with bare agent names (runtitle fallback ladder).
	Title                  string        `json:"title,omitempty"`
	Model                  string        `json:"model,omitempty"`
	// ProviderID pins the agent's explicitly chosen provider: routing must go
	// to THAT provider (verifying it serves the model), not to the first
	// provider found declaring the same model name. Empty = search by model.
	ProviderID             string        `json:"provider_id,omitempty"`
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
	// Secrets are the workspace secret names granted to this run's agent
	// (docs/secrets-management.md §8): resolved to SECRET_<NAME> sandbox env
	// vars at the execution boundary. Names only — values never enter the
	// workflow or the model context.
	Secrets []string `json:"secrets,omitempty"`
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
	RequireToolApproval bool `json:"require_tool_approval,omitempty"` // consequential tools need approval
	// BehaviorMonitor turns on the oversight judge (docs/agent-behavior-
	// monitoring.md, stage 3): every completed turn of this run is reviewed
	// for escape/exfil/tamper/deception patterns and flagged to the supervisor.
	BehaviorMonitor bool    `json:"behavior_monitor,omitempty"`
	TokenBudget     int     `json:"token_budget,omitempty"`    // 0 = uncapped
	MaxBudgetUSD    float64 `json:"max_budget_usd,omitempty"`  // 0 = uncapped
	TimeoutSeconds  int     `json:"timeout_seconds,omitempty"` // 0 = uncapped; checked at turn boundaries
	// Model prices resolved once at run-config time so the workflow can score
	// the budget without carrying the price table. Zero = no price configured;
	// a USD budget then degrades to the token cap.
	ModelPromptPricePer1k float64 `json:"model_prompt_price_per_1k,omitempty"`
	ModelCompPricePer1k   float64 `json:"model_comp_price_per_1k,omitempty"`
	// Team, when set, turns the run into a team program run (docs/agent-teams.md
	// §9): the root makes no model calls; the compiled protocol drives child
	// AgentRuns through the regular plan/delegate machinery instead.
	Team *TeamRunInput `json:"team,omitempty"`
	// Sandbox is the resolved sandbox profile (docs/sandbox-profiles.md §3.2):
	// image, resource overrides and cache env from the agent's assigned
	// profile. Nil = the installation default image and the legacy Go cache
	// env, i.e. exactly the behavior before profiles existed.
	Sandbox *SandboxSpec `json:"sandbox,omitempty"`
	// DelegationWidth caps concurrent child runs of this run's plans below the
	// installation MaxDelegationWidth (team defaults.parallel_limit). 0 keeps
	// the default.
	DelegationWidth int `json:"delegation_width,omitempty"`
	// TeamRootRunID names the root run of the team program this run belongs to
	// (docs/agent-teams.md §8): every slot and delegate carries it so shared
	// context — the team scratch directory, run-scoped knowledge hints — reaches
	// the whole tree. Empty for runs outside a team.
	TeamRootRunID string `json:"team_root_run_id,omitempty"`
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
	// ProviderID pins the agent's explicitly chosen provider (docs/agents.md
	// §routing): when set, routing must serve from THAT provider — no silent
	// first-match search across other providers declaring the same model.
	ProviderID string      `json:"provider_id,omitempty"`
	Messages []llm.Message `json:"messages"`
	Tools    []llm.ToolDef `json:"tools"`
	// RunID + Turn enable live token streaming through the ephemeral TokenBus;
	// empty RunID keeps the blocking non-streaming path (tests, helpers).
	RunID string `json:"run_id,omitempty"`
	Turn  int    `json:"turn,omitempty"`
	// Project scopes provider resolution: a model name may be served by a
	// provider bound to an org unit, and only the project's visible providers
	// are candidate routes.
	Project string `json:"project,omitempty"`
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
	// TaskID is the owning task of the run (docs/sagas.md): saga tools bind
	// explicit sagas at the task level, so follow-up runs and delegated
	// children inherit the binding automatically.
	TaskID string `json:"task_id,omitempty"`
	// ActorID is the run starter; send_file resolves them as the default
	// recipient of agent-produced files.
	ActorID string `json:"actor_id,omitempty"`
	// AgentID identifies the structured agent definition the run executes;
	// sandbox containers carry it as a label so observability can show which
	// agent occupies the host.
	AgentID   string         `json:"agent_id,omitempty"`
	Arguments map[string]any `json:"arguments"`
	// AllowedTools mirrors RunInput.ToolAllowlist so the activity also
	// rejects calls to tools that were never advertised to the model.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// Secrets mirrors RunInput.Secrets: the names resolvable into sandbox env
	// for this run (docs/secrets-management.md §8).
	Secrets []string `json:"secrets,omitempty"`
	// DeniedTools mirrors RunInput.DenyTools so the activity also rejects
	// calls to tools removed by capability enforcement.
	DeniedTools []string `json:"denied_tools,omitempty"`
	// ReadOnly mounts the workspace read-only in the command sandbox.
	ReadOnly bool `json:"read_only,omitempty"`
	// Sandbox carries the run's resolved sandbox profile into the execution
	// boundary (docs/sandbox-profiles.md §3.3); nil keeps the runner defaults.
	Sandbox *SandboxSpec `json:"sandbox,omitempty"`
}

// SandboxSpec is the resolved sandbox profile of a run: the digest-pinned
// image plus resource and cache-env overrides (docs/sandbox-profiles.md).
// It rides unchanged from run-config resolution through the workflow to the
// run_command activity — resolution never happens mid-run.
type SandboxSpec struct {
	Profile     string   `json:"profile,omitempty"` // profile id, for events and trace
	Image       string   `json:"image,omitempty"`   // digest-pinned image reference
	// Disabled marks the uniform switch (the trigger pattern): the profile
	// exists but was turned off. run_command fails with an explicit error
	// instead of falling back to another toolchain.
	Disabled    bool     `json:"disabled,omitempty"`
	Memory      string   `json:"memory,omitempty"`
	CPUs        string   `json:"cpus,omitempty"`
	ScratchSize string   `json:"scratch_size,omitempty"`
	TmpSize     string   `json:"tmp_size,omitempty"`
	CacheEnv    []string `json:"cache_env,omitempty"`
	// DockerSocket passes the host daemon socket into the sandbox — the
	// profile's explicit operator opt-in (docs/sandbox-profiles.md §5.3),
	// carried here only because the run_command activity needs it at the
	// execution boundary.
	DockerSocket bool `json:"docker_socket,omitempty"`
}

type ToolResult struct {
	Content  string                 `json:"content"`
	Evidence []observation.Evidence `json:"evidence,omitempty"`
	ExitCode *int                   `json:"exit_code,omitempty"`
	// Images carries vision blocks produced by image tools (view_image,
	// fetch_image). OpenAI-compatible APIs accept image parts only in user
	// messages, so the workflow re-injects them as one follow-up user message
	// after the turn's tool replies; the tool content stays metadata-only.
	Images []llm.ImageBlock `json:"images,omitempty"`
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
	// Values carries the structured form answer (docs/interactive-HITL.md §7):
	// the submission UI validates it against the request's schema; the approval
	// endpoint folds the canonical JSON into Response before the signal lands,
	// so the workflow contract stays a string. Empty for plain questions.
	Values map[string]any `json:"values,omitempty"`
}

// Nudge is the manual "unstick this model call" signal: it cancels the
// in-flight call_model activity and retries the turn with a fresh attempt.
// Model calls are pure (no side effects, cached prompt prefix), so a retry is
// always safe. Turn 0 matches whatever turn is currently in flight — the UI
// normally pins the exact turn to avoid acting on a stale request.
type Nudge struct {
	Turn    int    `json:"turn"`
	ActorID string `json:"actor_id,omitempty"`
}

// UserNote carries a chat message the user sent while the run was already in
// progress: the workflow appends it to the conversation at the nearest turn
// boundary instead of letting the client start a parallel run.
type UserNote struct {
	Text    string `json:"text"`
	ActorID string `json:"actor_id,omitempty"`
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
	// Replay compatibility gates (docs/failure-model.md): in-flight runs carry
	// histories recorded by older binaries. Every behavioral change to the
	// wait paths must be gated by GetVersion, so a replay takes the code path
	// that matches the recorded history instead of failing the workflow task
	// on determinism checks. Called unconditionally, in this fixed order.
	noteSteering := workflow.GetVersion(ctx, "delegation-note-steering", workflow.DefaultVersion, 1)
	modelSchedStart := workflow.GetVersion(ctx, "model-schedule-to-start", workflow.DefaultVersion, 1)
	titleParallel := workflow.GetVersion(ctx, "run-title-parallel", workflow.DefaultVersion, 1)
	// Journal transport gate: version 1 records every emit event through a
	// local activity (RecordEventLocal) instead of a queued activity task.
	recordLocal := workflow.GetVersion(ctx, recordEventLocalChangeID, workflow.DefaultVersion, recordEventLocalVersion)
	// The team root seeds the shared context handle: every child run of the
	// program (slots, reviewers, delegates below them) inherits it so scratch
	// paths and run-scoped hints stay addressable across the tree.
	if input.Team != nil && input.TeamRootRunID == "" {
		input.TeamRootRunID = input.RunID
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
	state := &eventState{run: input, sequence: 0, eventScope: eventScope(input.SourceID, input.Project, input.RunID), parentFrame: input.ParentFrameID, previousEventID: input.ParentEventID, recordLocal: recordLocal}
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
	// The initiating request rides the start event: the run overview renders
	// it, and without it the journal has no readable trace of what the run
	// was asked to do (the task row holds the full prompt, but child runs of
	// delegate/plan have no task row at all). Bounded — prompts reach tens of
	// kilobytes and the event stream is not the place for the full body.
	if prompt := strings.TrimSpace(input.Prompt); prompt != "" {
		started["request"] = truncate(prompt, 2000)
	}
	if input.AgentVersion > 0 {
		started["agent_version"] = input.AgentVersion
	}
	// Title and prior-knowledge hints, with the run.started entry between their
	// result handling. The wait order is replay-gated, see runTitleAndHints.
	hintBlock, err := runTitleAndHints(ctx, activityCtx, state, input, started, titleParallel)
	if err != nil {
		return result, err
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
	if input.PriorTail != "" {
		messages = append(messages, llm.Message{Role: "system", Content: input.PriorTail})
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
	executePlan := func(call llm.ToolCall, operationID, argumentsHash string, startTool func() error, turn int) (string, *planExecution, error) {
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
		// The result is validation-only: every launch re-resolves, because a plan
		// can run for hours (gated tasks, rework rounds) and agent config changed
		// in between must reach the launches that still run.
		resolveCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, ScheduleToCloseTimeout: 40 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3}})
		for index := range spec.Tasks {
			task := spec.Tasks[index]
			childRunID := planTaskRunID(index, task.ID, 1)
			var childInput RunInput
			resolveErr := workflow.ExecuteActivity(resolveCtx, ActivityResolveAgent, ResolveAgentRequest{Project: input.Project, AgentID: task.AgentID, RunID: childRunID, TaskID: input.TaskID, Prompt: task.Prompt, ActorID: input.ActorID, MaxTurns: task.MaxTurns, DelegationDepth: input.DelegationDepth + 1}).Get(ctx, &childInput)
			if resolveErr != nil {
				if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "plan", "error_type": "agent_resolution_failed", "effect": "none", "detail": boundedFailureDetail(resolveErr)}); err != nil {
					return "", nil, err
				}
				return "Plan rejected before execution, no effect: agent \"" + task.AgentID + "\" for task \"" + task.ID + "\" could not be resolved (" + boundedFailureDetail(resolveErr) + "). List valid ids with the agents tool and re-issue the plan with exact agent_id values.", nil, nil
			}
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
		startedData := map[string]any{"operation_id": operationID, "goal": spec.Goal, "tasks": tasksData, "max_rework": maxRework, "on_failure": planOnFailurePolicy(spec.OnFailure)}
		if spec.Mode == PlanModeWave {
			startedData["mode"] = PlanModeWave
		}
		if err := emit(activityCtx, state, "plan.started", startedData); err != nil {
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
		// Wave mode (mode="wave"): after the first in-flight batch settles, the
		// call returns instead of driving the whole DAG — newly-ready tasks are
		// left pending for the parent's re-issue (see the launch phase gate).
		drainedOnce := false
		stagedRemaining := []string{}
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
		// skipBranch settles the pending dependents of a dead task. The reason
		// tells the UI and the summary whether the blocker failed or merely never
		// finished (an incomplete upstream leaves its consumers nothing to build on).
		skipBranch := func(blockedFrom, reason string) error {
			queue := append([]string{}, dag.Dependents[blockedFrom]...)
			for len(queue) > 0 {
				id := queue[0]
				queue = queue[1:]
				if statuses[id] != "" {
					continue
				}
				statuses[id] = planStatusSkipped
				outcomesByTask[id] = planTaskOutcome{TaskID: id, AgentID: agentIDs[id], Status: planStatusSkipped, BlockedBy: blockedFrom, SkipReason: reason}
				if err := emit(activityCtx, state, "plan.task.skipped", map[string]any{"operation_id": operationID, "task_id": id, "agent_id": agentIDs[id], "reason": reason, "blocked_by": blockedFrom}); err != nil {
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
		// Failure policies (on_failure): after the first failed or incomplete
		// task, "abort" cancels running children and returns the summary
		// immediately; "stop" keeps running branches and only stops launching
		// new work. Both settle pending tasks synchronously in spec order —
		// deterministic executor state — and make the summary lead with the
		// cause, so the parent can diagnose and re-issue instead of waiting out
		// a doomed DAG.
		onFailureAbort := spec.OnFailure == PlanOnFailureAbort
		onFailureStop := spec.OnFailure == PlanOnFailureStop
		abortedBy := ""
		stoppedBy := ""
		settlePending := func(reason, blockedBy string) error {
			for index := range spec.Tasks {
				id := spec.Tasks[index].ID
				if statuses[id] != "" {
					continue
				}
				statuses[id] = planStatusSkipped
				outcomesByTask[id] = planTaskOutcome{TaskID: id, AgentID: agentIDs[id], Status: planStatusSkipped, SkipReason: reason, BlockedBy: blockedBy}
				if err := emit(activityCtx, state, "plan.task.skipped", map[string]any{"operation_id": operationID, "task_id": id, "agent_id": agentIDs[id], "reason": reason, "blocked_by": blockedBy}); err != nil {
					return err
				}
			}
			return nil
		}
		requestAbort := func(taskID string) error {
			if !onFailureAbort || abortedBy != "" {
				return nil
			}
			abortedBy = taskID
			if err := emit(activityCtx, state, "plan.aborted", map[string]any{"operation_id": operationID, "task_id": taskID, "agent_id": agentIDs[taskID]}); err != nil {
				return err
			}
			if err := settlePending("plan_aborted", taskID); err != nil {
				return err
			}
			for _, pending := range inflightPlan {
				cancelRunningChild(pending.taskID)
			}
			return nil
		}
		requestStop := func(taskID string) error {
			if !onFailureStop || stoppedBy != "" || abortedBy != "" {
				return nil
			}
			stoppedBy = taskID
			if err := emit(activityCtx, state, "plan.stopped", map[string]any{"operation_id": operationID, "task_id": taskID, "agent_id": agentIDs[taskID]}); err != nil {
				return err
			}
			return settlePending("plan_stopped", taskID)
		}
		// A policy already fired: tasks reopened later (a rework verdict after a
		// stop) must settle instead of staying pending forever.
		haltFired := func() bool { return abortedBy != "" || stoppedBy != "" }
		requestFailurePolicy := func(taskID string) error {
			if err := requestAbort(taskID); err != nil {
				return err
			}
			return requestStop(taskID)
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
			if err := requestFailurePolicy(taskID); err != nil {
				return err
			}
			return skipBranch(taskID, "upstream_failed")
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
			// Wave mode gates the second and later launch phases: only tasks that
			// already ran (rework reopenings) may launch — never-run tasks stay
			// pending for the parent's re-issue, and the call returns after the
			// current in-flight batch settles.
			budgetExhausted := totalLaunches >= launchBudget
			stagedRemaining = stagedRemaining[:0]
			for index := range spec.Tasks {
				task := spec.Tasks[index]
				if statuses[task.ID] != "" || dag.DepsLeft[task.ID] > 0 {
					continue
				}
				if haltFired() {
					// A failure policy fired after this task was reopened: settle it
					// instead of launching into a stopped/aborted plan.
					reason := "plan_stopped"
					blockedBy := stoppedBy
					if abortedBy != "" {
						reason, blockedBy = "plan_aborted", abortedBy
					}
					statuses[task.ID] = planStatusSkipped
					outcomesByTask[task.ID] = planTaskOutcome{TaskID: task.ID, AgentID: agentIDs[task.ID], Status: planStatusSkipped, SkipReason: reason, BlockedBy: blockedBy}
					if err := emit(activityCtx, state, "plan.task.skipped", map[string]any{"operation_id": operationID, "task_id": task.ID, "agent_id": agentIDs[task.ID], "reason": reason, "blocked_by": blockedBy}); err != nil {
						return "", nil, err
					}
					continue
				}
				if spec.Mode == PlanModeWave && drainedOnce && launches[task.ID] == 0 {
					// A newly-ready task from a later stage: leave pending for the
					// parent's re-issue.
					stagedRemaining = append(stagedRemaining, task.ID)
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
				// Resolve at launch time, not from the plan-start snapshot: gated
				// tasks and rework rounds can launch hours after validation, and a
				// config change in between (a raised turn budget, a different model,
				// rotated policies) must reach them. Existence was validated up
				// front; a failure here fails only this task — nothing started for
				// it, so the effect is none — and dependents are skipped honestly.
				var child RunInput
				if resolveErr := workflow.ExecuteActivity(resolveCtx, ActivityResolveAgent, ResolveAgentRequest{Project: input.Project, AgentID: task.AgentID, RunID: childRunID, TaskID: input.TaskID, Prompt: task.Prompt, ActorID: input.ActorID, MaxTurns: task.MaxTurns, DelegationDepth: input.DelegationDepth + 1}).Get(ctx, &child); resolveErr != nil {
					statuses[task.ID] = planStatusFailed
					outcomesByTask[task.ID] = planTaskOutcome{TaskID: task.ID, AgentID: task.AgentID, ChildRunID: childRunID, Status: planStatusFailed, Error: boundedFailureDetail(resolveErr)}
					if err := emit(activityCtx, state, "plan.task.failed", map[string]any{"operation_id": operationID, "task_id": task.ID, "child_run_id": childRunID, "agent_id": task.AgentID, "error_type": "agent_resolution_failed", "error": boundedFailureDetail(resolveErr), "effect": "none"}); err != nil {
						return "", nil, err
					}
					if err := requestFailurePolicy(task.ID); err != nil {
						return "", nil, err
					}
					if err := skipBranch(task.ID, "upstream_failed"); err != nil {
						return "", nil, err
					}
					continue
				}
				child.RunID = childRunID
				child.Prompt = planTaskPrompt(task, answers, planAnswers)
				if input.Team != nil {
					// Team context (docs/agent-teams.md §8): the scratch dir every slot
					// shares plus the contract files of upstream slots — text rides in
					// the prompt, files persist for humans and later stages.
					child.Prompt += teamContextPrompt(input.TeamRootRunID, task)
				}
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
				child.TeamRootRunID = input.TeamRootRunID
				taskDeps := task.DependsOn
				if taskDeps == nil {
					taskDeps = []string{}
				}
				startedData := map[string]any{"operation_id": operationID, "task_id": task.ID, "child_run_id": childRunID, "agent_id": task.AgentID, "depends_on": taskDeps, "round": launches[task.ID] + 1, "max_turns": child.MaxTurns, "prompt": truncate(task.Prompt, 2000)}
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
			// Mid-delegation steering (docs/agent-delegation.md §steering):
			// wait on the child AND the note signal together — a chat addendum
			// wakes the lead immediately and is signalled into every still-
			// running child's mailbox, instead of sitting queued until the
			// whole delegation completes. Gated by version: pre-gate histories
			// replay the plain blocking wait.
			var child RunResult
			var childErr error
			if noteSteering == 1 {
				childDone := false
				noteChannel := workflow.GetSignalChannel(ctx, UserNoteSignal)
				selector := workflow.NewSelector(ctx)
				selector.AddFuture(p.future, func(f workflow.Future) {
					childDone = true
					childErr = f.Get(ctx, &child)
				})
				selector.AddReceive(noteChannel, func(c workflow.ReceiveChannel, more bool) {
					if notes := pendingUserNotes(ctx); len(notes) > 0 {
						running := runningPlanChildren(inflightPlan, p)
						// Best-effort: a journaling failure here must not fail the
						// child (the callback's only error channel is childErr, and
						// the child is alive) — the note is already delivered.
						_ = forwardUserNotesToChildren(ctx, activityCtx, state, turn, notes, running)
						appendForwardedNotes(&messages, notes)
					}
				})
				for !childDone {
					selector.Select(ctx)
				}
			} else {
				childErr = p.future.Get(ctx, &child)
			}
			if childErr == nil {
				// Subtree rollup: the child's totals (its own plus its children)
				// flow into this run's honest spend.
				subtree.tokens += child.TotalTokens
				subtree.costUSD += child.CostUSD
			}
			drainedOnce = true
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
					if err := skipBranch(p.taskID, "upstream_failed"); err != nil {
						return "", nil, err
					}
				} else {
					statuses[p.taskID] = ""
					for _, dep := range spec.Tasks[taskIndex[p.taskID]].DependsOn {
						depStatus := statuses[dep]
						blocked := depStatus == planStatusFailed || depStatus == planStatusSkipped || depStatus == planStatusInvalidated || depStatus == planStatusIncomplete
						if blocked {
							reason := "upstream_failed"
							if depStatus == planStatusIncomplete {
								reason = "upstream_incomplete"
							}
							statuses[p.taskID] = planStatusSkipped
							outcomesByTask[p.taskID] = planTaskOutcome{TaskID: p.taskID, AgentID: p.agentID, ChildRunID: p.childRunID, Status: planStatusSkipped, BlockedBy: dep, SkipReason: reason}
							if err := emit(activityCtx, state, "plan.task.skipped", map[string]any{"operation_id": operationID, "task_id": p.taskID, "agent_id": p.agentID, "reason": reason, "blocked_by": dep}); err != nil {
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
				// A cancelled child after an abort is the policy working, not a
				// failure: settle it honestly as skipped instead of stacking a
				// bogus plan.task.failed on top of the abort.
				if abortedBy != "" {
					statuses[p.taskID] = planStatusSkipped
					outcomesByTask[p.taskID] = planTaskOutcome{TaskID: p.taskID, AgentID: p.agentID, ChildRunID: p.childRunID, Status: planStatusSkipped, SkipReason: "plan_aborted", BlockedBy: abortedBy}
					if err := emit(activityCtx, state, "plan.task.skipped", map[string]any{"operation_id": operationID, "task_id": p.taskID, "agent_id": p.agentID, "reason": "plan_aborted", "blocked_by": abortedBy}); err != nil {
						return "", nil, err
					}
					continue
				}
				if err := failTask(p.taskID, p.childRunID, "delegated_run_failed", childErr, summarizeChild(p.childRunID)); err != nil {
					return "", nil, err
				}
				continue
			}
			// Honest settlement: a child ended by a policy limit (forced finale)
			// finished its workflow without an error, but its report is a handoff,
			// not the deliverable. The task is incomplete and dependents must not
			// build on unfinished work. Anything else that is not "completed"
			// (cancelled, unknown) settles defensively as a failure: no answer was
			// produced to trust.
			switch child.Status {
			case "completed":
				// Normal settlement below (verdict gate, then completion).
			case "turn_limit", "time_limit", "token_limit", "budget_limit":
				answer, _ := BoundedNarrative(child.Answer)
				statuses[p.taskID] = planStatusIncomplete
				outcomesByTask[p.taskID] = planTaskOutcome{TaskID: p.taskID, AgentID: p.agentID, ChildRunID: p.childRunID, Status: planStatusIncomplete, ChildStatus: child.Status, Turns: child.Turns, Round: launches[p.taskID], Answer: answer, Error: "executor finished by " + child.Status + ", not by its own final answer"}
				if err := emit(activityCtx, state, "plan.task.incomplete", map[string]any{"operation_id": operationID, "task_id": p.taskID, "child_run_id": p.childRunID, "agent_id": p.agentID, "child_status": child.Status, "turns": child.Turns, "round": launches[p.taskID]}); err != nil {
					return "", nil, err
				}
				if err := skipBranch(p.taskID, "upstream_incomplete"); err != nil {
					return "", nil, err
				}
				if err := requestFailurePolicy(p.taskID); err != nil {
					return "", nil, err
				}
				continue
			default:
				status := child.Status
				if status == "" {
					status = "unknown"
				}
				if err := failTask(p.taskID, p.childRunID, "delegated_run_"+status, fmt.Errorf("child run ended with status %q without a final answer", status), summarizeChild(p.childRunID)); err != nil {
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
							if err := requestFailurePolicy(reviewed); err != nil {
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
							if err := skipBranch(p.taskID, "upstream_failed"); err != nil {
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
			if input.Team != nil && input.TeamRootRunID != "" && len(specTask.ReviewOf) == 0 {
				// Persist the slot contract into the team scratch (docs/agent-teams.md
				// §8): dependents already get the text in their prompt, the file serves
				// humans and any stage that wants the full deliverable. Best effort —
				// a failed write never fails the team. Rework rounds overwrite it.
				contractCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 15 * time.Second, ScheduleToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3}})
				var contractPath string
				_ = workflow.ExecuteActivity(contractCtx, ActivityWriteTeamContract, TeamContractRequest{Project: input.Project, RunID: input.TeamRootRunID, TaskID: p.taskID, Answer: answer}).Get(contractCtx, &contractPath)
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
		completedData := map[string]any{"operation_id": operationID, "goal": spec.Goal, "statuses": statusData}
		halt := planHalt{}
		if abortedBy != "" {
			completedData["aborted_by"] = abortedBy
			halt.kind, halt.by = "aborted", abortedBy
		} else if stoppedBy != "" {
			completedData["stopped_by"] = stoppedBy
			halt.kind, halt.by = "stopped", stoppedBy
		}
		if spec.Mode == PlanModeWave {
			completedData["mode"] = PlanModeWave
			for index := range spec.Tasks {
				if statuses[spec.Tasks[index].ID] == "" {
					halt.remaining = append(halt.remaining, spec.Tasks[index].ID)
				}
			}
			if len(halt.remaining) > 0 {
				completedData["remaining"] = append([]string{}, halt.remaining...)
			}
		}
		if err := emit(activityCtx, state, "plan.completed", completedData); err != nil {
			return "", nil, err
		}
		content := planSummary(spec, outcomes, rejections, halt)
		if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": "plan", "output": compactJSON(content, 1000)}); err != nil {
			return "", nil, err
		}
		return content, &planExecution{Outcomes: outcomes, Rejections: rejections}, nil
	}
	if input.Team != nil {
		return runTeamProgram(ctx, activityCtx, state, input, executePlan, &subtree)
	}
	// advertisedTools is a pure function of the prepared run input: KernelTools()
	// builds a fresh literal, and input.Tools/ToolAllowlist/DenyTools/Project are
	// constants after input preparation — the only writes to input happen at the
	// top of AgentRun, and nothing inside the turn loop takes &input except the
	// read-only delegationWidthLimit. The result is identical on every turn, so
	// compute it once; a local computation issues no workflow commands and needs
	// no GetVersion gate.
	runTools := advertisedTools(&input)
	// Background operations (docs/agent-async-operations.md): run_command calls
	// with background=true start their RunTool activity here and the turn gets
	// a synthetic «started» result immediately; the futures resolve later and
	// each completion wakes the model with a batched notification. The map is
	// workflow-local and rebuilt deterministically during replay (the
	// ExecuteActivity calls are history records).
	background := map[string]*backgroundOp{}
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
		// User notes (chat messages sent mid-run) are drained at turn boundaries:
		// the history stays append-only, so the prompt-cache prefix keeps its
		// byte-stability, and the addendum reaches the model at the earliest turn
		// that has not been built yet.
		if _, noteErr := appendUserNotes(activityCtx, state, turn, &messages, pendingUserNotes(ctx)); noteErr != nil {
			return result, noteErr
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
		modelReq := ModelRequest{Model: input.Model, ProviderID: input.ProviderID, Messages: turnMessages, Tools: runTools, RunID: input.RunID, Turn: turn, Project: input.Project, PromptCacheKey: input.TaskID}
		if err := emit(activityCtx, state, "model.started", map[string]any{"turn": turn, "model": input.Model}); err != nil {
			return result, err
		}
		var completion llm.Completion
		// Model endpoints fail transiently (hangs, EOFs, empty bodies); retries
		// with backoff live inside the llm client, so one activity attempt must
		// cover the client's worst case: a streaming attempt bounded only by
		// chunk silence (StreamIdleTimeout) plus the blocking fallback (3 attempts
		// × request timeout + backoff ≈ 9m15s). Long generations are expected,
		// hence the configurable ceiling — see ModelCallActivityTimeout.
		// Non-retryable model errors (bad request, auth) fail fast on the first
		// client attempt.
		//
		// The one failure the in-client retries cannot absorb is a frozen host:
		// when the machine sleeps mid-stream, the worker and the Temporal server
		// freeze together, and on wake the StartToClose deadline has already
		// passed — the attempt dies having done no work. Model calls are pure
		// (no side effects, prompt prefix cached via PromptCacheKey), so Temporal
		// retries the whole activity: a fresh attempt after wake recovers the
		// run instead of killing it. ScheduleToClose bounds the total patience.
		// ScheduleToStartTimeout surfaces queue starvation honestly: without it a
		// call whose worker died (restart, deploy) sits silent for up to the whole
		// ScheduleToClose (65m) while the UI shows «ждём первый токен» — the
		// attempt fails after the timeout and the retry ladder takes over.
		// Gated by version: the extra timeout field is part of the activity
		// schedule command, so pre-gate histories must replay without it.
		// HeartbeatTimeout turns «kernel was rebuilt mid-call» from a 20-minute
		// freeze into a 90-second retry: a dead worker stops heartbeating, and
		// Temporal re-runs the call on the live worker. A live worker keeps
		// heartbeating through long streams, so a healthy slow call is safe.
		modelOpts := workflow.ActivityOptions{StartToCloseTimeout: ModelCallActivityTimeout, HeartbeatTimeout: 90 * time.Second, ScheduleToCloseTimeout: ModelCallActivityTimeout*3 + 5*time.Minute, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second, MaximumAttempts: 3}}
		if modelSchedStart == 1 {
			modelOpts.ScheduleToStartTimeout = 5 * time.Minute
		}
		if err := executeModelWithNudge(ctx, activityCtx, state, modelOpts, modelReq, turn, &completion); err != nil {
			failureDetail := boundedFailureDetail(err)
			if eventErr := emit(activityCtx, state, "model.failed", map[string]any{"turn": turn, "model": input.Model, "error_type": "activity_failed", "error": failureDetail}); eventErr != nil {
				return result, eventErr
			}
			if eventErr := emit(activityCtx, state, "run.failed", map[string]any{"turn": turn, "error_type": "model_call_failed", "error": failureDetail}); eventErr != nil {
				return result, eventErr
			}
			result.Status = "failed"
			return result, err
		}
		completedData := map[string]any{"turn": turn, "model": input.Model, "tool_call_count": len(completion.ToolCalls), "finish_reason": completion.Finish, "total_tokens": completion.Usage.TotalTokens}
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
			// A note that arrived while this answer was being generated reopens the
			// task: the answer is recorded in history, the addendum is delivered
			// after it, and one more turn is spent on the request. History order
			// matters — the draft was produced WITHOUT seeing the note, so it must
			// land before the note, not after. At the turn-budget edge the loop
			// simply falls through to the deterministic finale.
			if notes := pendingUserNotes(ctx); len(notes) > 0 {
				usedTokens += completion.Usage.TotalTokens
				usedCostUSD += modelCallCostUSD(input, completion.Usage)
				messages = append(messages, llm.Message{Role: "assistant", Content: completion.Content})
				if _, noteErr := appendUserNotes(activityCtx, state, turn, &messages, notes); noteErr != nil {
					return result, noteErr
				}
				if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": turn, "reopened_by_note": true}); err != nil {
					return result, err
				}
				continue
			}
			// Background operations (docs/agent-async-operations.md): the model
			// is done, but detached commands still run — the run must NOT
			// complete while they do. Wait for the first resolution (or a
			// note), deliver the batched outcome report, and give the model
			// another turn to act on it. The loop exits naturally once every
			// operation has resolved and the model produces a final answer.
			if len(background) > 0 {
				usedTokens += completion.Usage.TotalTokens
				usedCostUSD += modelCallCostUSD(input, completion.Usage)
				messages = append(messages, llm.Message{Role: "assistant", Content: completion.Content})
				if _, waitErr := awaitBackgroundOps(ctx, activityCtx, state, &messages, background); waitErr != nil {
					return result, waitErr
				}
				if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": turn, "reopened_by_background": true}); err != nil {
					return result, err
				}
				continue
			}
			// A reasoning model can end the turn with reasoning only and an empty
			// content (xiaomi mimo does). Completing here would publish an empty
			// answer after a full run's worth of work. Nudge the conversation
			// instead: the model gets a system reminder that its reply was empty
			// and must produce the final text; the turn loop continues, and the
			// turn budget/forced finale still bound the recovery.
			if strings.TrimSpace(completion.Content) == "" {
				usedTokens += completion.Usage.TotalTokens
				usedCostUSD += modelCallCostUSD(input, completion.Usage)
				messages = append(messages,
					llm.Message{Role: "assistant", Content: ""},
					llm.Message{Role: "system", Content: "Your previous reply contained no answer text (reasoning only). Produce the final answer to the user now as plain text — do not end the reply without it."})
				if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": turn, "empty_answer_retry": true}); err != nil {
					return result, err
				}
				continue
			}
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
		// Malformed tool calls (empty function name — seen once from a real
		// provider, harness_bug case) MUST NOT enter the history: the next
		// model call replays them and the provider rejects the whole request
		// with a 400. Filtered here, before both execution and the history
		// append; each dropped call answers the model with an actionable tool
		// message so it can re-issue the call correctly.
		validCalls := make([]llm.ToolCall, 0, len(completion.ToolCalls))
		for _, call := range completion.ToolCalls {
			if strings.TrimSpace(call.Name) == "" {
				if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": fmt.Sprintf("%s/%s", frame, call.ID), "tool_call_id": call.ID, "tool": "", "error_type": "empty_tool_name", "effect": "none", "detail": "model emitted a tool call without a function name; it was dropped"}); err != nil {
					return result, err
				}
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: "Tool call was rejected: it had no function name. Re-issue it with the correct tool."})
				continue
			}
			validCalls = append(validCalls, call)
		}
		completion.ToolCalls = validCalls
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
			// Mid-delegation steering (docs/agent-delegation.md §steering):
			// same as the plan loop — a chat addendum during the wait is
			// signalled straight into the running children's note mailboxes.
			// Gated by version: pre-gate histories replay the plain blocking
			// wait, which is what their events were recorded against.
			var childErr error
			if noteSteering == 1 {
				childDone := false
				noteChannel := workflow.GetSignalChannel(ctx, UserNoteSignal)
				selector := workflow.NewSelector(ctx)
				selector.AddFuture(p.future, func(f workflow.Future) {
					childDone = true
					childErr = f.Get(ctx, &child)
				})
				selector.AddReceive(noteChannel, func(c workflow.ReceiveChannel, _ bool) {
					if notes := pendingUserNotes(ctx); len(notes) > 0 {
						// p is still in inflight here (the width gate awaits it before
						// removing it), so inflight alone covers every running child.
						running := make([]string, 0, len(inflight))
						for _, d := range inflight {
							running = append(running, d.childRunID)
						}
						// Best-effort journaling: the child is alive, a lead-side
						// journaling failure must not turn into childErr.
						_ = forwardUserNotesToChildren(ctx, activityCtx, state, turn, notes, running)
						appendForwardedNotes(&messages, notes)
					}
				})
				for !childDone {
					selector.Select(ctx)
				}
			} else {
				childErr = p.future.Get(ctx, &child)
			}
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
				return "Delegation rejected before execution, no effect: agent \"" + delegateAgentID + "\" could not be resolved (" + boundedFailureDetail(resolveErr) + "). List valid ids with the agents tool and re-issue with the exact agent_id, or do the work yourself.", false, nil
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
			if err := emit(activityCtx, state, "delegation.started", map[string]any{"operation_id": operationID, "child_run_id": childRunID, "agent_id": delegateAgentID, "ordinal": delegations, "depth": input.DelegationDepth + 1, "prompt": truncate(delegatePrompt, 2000), "max_turns": delegateMaxTurns}); err != nil {
				return "", false, err
			}
			childInput.ParentRunID = input.RunID
			childInput.ParentFrameID = state.frame
			childInput.ParentEventID = state.previousEventID
			childInput.TeamRootRunID = input.TeamRootRunID
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
		// Vision blocks (view_image / fetch_image) produced by this turn's tool
		// calls. OpenAI-compatible APIs accept image parts only in user messages,
		// never in tool results, so after every tool reply of the turn has landed
		// they are re-injected as one follow-up user message (append-only history:
		// prompt-cache compatible, dedup replays already carry the blocks).
		var turnImages []llm.ImageBlock
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
			// Must outlive the sandbox command ceiling (10 min) — see
			// ToolActivityTimeout. MaximumAttempts stays 1: tools carry side
			// effects, a retry could duplicate them.
			toolCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: ToolActivityTimeout, ScheduleToCloseTimeout: ToolActivityTimeout + time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
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
				// optional structured options or a form schema (docs/interactive-HITL.md
				// §7). The run pauses on the approval signal channel; the answer rides
				// the same Approval payload — Response (option/free text) or the
				// canonical JSON of the validated form values.
				question, _ := call.Args["question"].(string)
				humanContext, _ := call.Args["context"].(string)
				options := stringList(call.Args["options"])
				recipient, _ := call.Args["recipient"].(string)
				formSchema := boundedFormSchema(call.Args["schema"])
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
				if err := emit(activityCtx, state, "human.requested", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "recipient": recipient, "question": question, "context": humanContext, "options": options, "schema": formSchema, "timeout_seconds": timeoutSeconds, "timeout_policy": timeoutPolicy}); err != nil {
					return result, err
				}
				// Route the question to the recipient's preferred channel (§6-7,
				// §26-27): logical recipient → concrete user → channel. Delivery
				// problems are recorded, never fatal — the run keeps waiting.
				notifyCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, ScheduleToCloseTimeout: 45 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 2}})
				var delivery NotificationDelivery
				notifyReq := NotifyChannelRequest{Project: input.Project, RunID: input.RunID, OperationID: operationID, Recipient: recipient, ActorID: input.ActorID, AgentID: input.AgentID, Question: question, Context: humanContext, Options: options, TimeoutSeconds: timeoutSeconds, TimeoutPolicy: timeoutPolicy, Schema: formSchema, ExecutionIdentityID: input.ExecutionIdentityID}
				if err := workflow.ExecuteActivity(notifyCtx, ActivityNotifyChannel, notifyReq).Get(ctx, &delivery); err != nil {
					delivery = NotificationDelivery{Recipient: recipient, Channel: "web", Status: "failed", Detail: boundedFailureDetail(err)}
				}
				notificationSent := map[string]any{"operation_id": operationID, "recipient": delivery.Recipient, "channel": delivery.Channel, "status": delivery.Status, "detail": delivery.Detail}
				if len(delivery.DeliveredTo) > 0 {
					notificationSent["delivered_to"] = delivery.DeliveredTo
				}
				if err := emit(activityCtx, state, "notification.sent", notificationSent); err != nil {
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
						reaskSent := map[string]any{"operation_id": operationID, "recipient": redelivery.Recipient, "channel": redelivery.Channel, "status": redelivery.Status, "detail": redelivery.Detail, "round": round + 1}
						if len(redelivery.DeliveredTo) > 0 {
							reaskSent["delivered_to"] = redelivery.DeliveredTo
						}
						if err := emit(activityCtx, state, "notification.sent", reaskSent); err != nil {
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
					// The answer payload: form values as canonical JSON (the endpoint
					// validated them against the schema before signaling), or the plain
					// option/free-text answer. Empty payload must not reach the model —
					// it reads as "the transport lost the answer" and triggers retries.
					answerText := formAnswerText(answer)
					_ = workflow.ExecuteActivity(closeCtx, ActivityCloseHumanRequest, CloseHumanRequestInput{OperationID: operationID, Status: "answered", Response: answerText, ActorID: answer.ActorID}).Get(ctx, nil)
					if err := emit(activityCtx, state, "human.answered", map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "actor": answer.ActorID, "response": answerText}); err != nil {
						return result, err
					}
					toolResult.Content = "Human response: " + answerText
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
						content, _, planErr := executePlan(call, operationID, argumentsHash, startTool, turn)
						if planErr != nil {
							return result, planErr
						}
						toolResult.Content = content
					} else if bgFlag, _ := call.Args["background"].(bool); call.Name == "run_command" && bgFlag {
						// Approved background command (docs/agent-async-operations.md):
						// the same detach as the normal path, after the grant.
						timeoutSec := 0
						if requested, ok := numericArg(call.Args["timeout_sec"]); ok && requested > 0 {
							timeoutSec = int(requested)
						}
						if timeoutSec <= 0 {
							timeoutSec = sandbox.DefaultTimeout
						}
						bgCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
							StartToCloseTimeout:    time.Duration(timeoutSec)*time.Second + 10*time.Minute,
							ScheduleToCloseTimeout: time.Duration(timeoutSec)*time.Second + 15*time.Minute,
							HeartbeatTimeout:       90 * time.Second,
							RetryPolicy:            &temporal.RetryPolicy{MaximumAttempts: 1},
						})
						future := workflow.ExecuteActivity(bgCtx, ActivityRunTool, ToolRequest{RunID: input.RunID, OperationID: operationID, Name: call.Name, Role: input.Role, WorkspacePath: input.WorkspacePath, Project: input.Project, TaskID: input.TaskID, ActorID: input.ActorID, AgentID: input.AgentID, Arguments: call.Args, AllowedTools: input.ToolAllowlist, DeniedTools: input.DenyTools, ReadOnly: input.ReadOnly, Secrets: input.Secrets, Sandbox: input.Sandbox})
						if err := startTool(); err != nil {
							return result, err
						}
						background[operationID] = &backgroundOp{future: future, command: compactJSON(call.Args["command"], 120), started: workflow.Now(ctx), timeoutSec: timeoutSec, turn: turn}
						toolResult.Content = fmt.Sprintf("Started in background as operation %s. The result will arrive automatically as a follow-up message; use kill_operation to cancel it.", operationID)
					} else if err := startTool(); err != nil {
						return result, err
					} else if err := startMCP(); err != nil {
						return result, err
					} else {
						// Wall latency from the workflow clock covers scheduling, retries
						// and execution — the honest end-to-end cost of the operation.
						toolStarted := workflow.Now(ctx)
						if err := workflow.ExecuteActivity(toolCtx, ActivityRunTool, ToolRequest{RunID: input.RunID, OperationID: operationID, Name: call.Name, Role: input.Role, WorkspacePath: input.WorkspacePath, Project: input.Project, TaskID: input.TaskID, ActorID: input.ActorID, AgentID: input.AgentID, Arguments: call.Args, AllowedTools: input.ToolAllowlist, DeniedTools: input.DenyTools, ReadOnly: input.ReadOnly, Secrets: input.Secrets, Sandbox: input.Sandbox}).Get(ctx, &toolResult); err != nil {
							toolFailed = true
							failure := toolFailureData(operationID, argumentsHash, call.Name, call.Args, err)
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
			} else if call.Name == "remember_many" {
				// Bulk knowledge import (the test-map / document learning case): the
				// agent decomposes a document or map into atomic candidates and
				// submits them in one call. Each candidate becomes a normal
				// knowledge.proposed (same chain as remember) — the projection,
				// dedup and hint matcher treat them identically. The summary tells
				// the agent how many landed, so it can answer the user honestly.
				if err := startTool(); err != nil {
					return result, err
				}
				rawItems, _ := call.Args["items"].([]any)
				if len(rawItems) == 0 {
					toolResult.Content = "items is required: an array of {proposition, evidence?} — one atomic fact per item"
				} else if len(rawItems) > 50 {
					toolResult.Content = fmt.Sprintf("too many items (%d): submit at most 50 per call; split the batch", len(rawItems))
				} else {
					recorded, skipped := 0, 0
					for _, item := range rawItems {
						obj, _ := item.(map[string]any)
						proposition, _ := obj["proposition"].(string)
						proposition = strings.TrimSpace(proposition)
						if proposition == "" {
							skipped++
							continue
						}
						args := map[string]any{}
						if evidence, ok := obj["evidence"].([]any); ok {
							args["evidence"] = evidence
						}
						knowledgeID := fmt.Sprintf("%s/knowledge/%02d", input.RunID, state.sequence+1)
						if err := emitKnowledge(activityCtx, state, knowledgeID, proposition, operationID, args); err != nil {
							return result, err
						}
						recorded++
					}
					toolResult.Content = fmt.Sprintf("Recorded %d knowledge items (%d skipped: empty). Proposed items surface in hints as unconfirmed until confirmed.", recorded, skipped)
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
				content, _, planErr := executePlan(call, operationID, argumentsHash, startTool, turn)
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
				startedBackground := false
				if bgFlag, _ := call.Args["background"].(bool); call.Name == "run_command" && bgFlag {
					// Async operation (docs/agent-async-operations.md): start the
					// RunTool activity and return immediately — the synthetic
					// result tells the model the operation id; the future stays
					// in the registry until its completion notification. The
					// tool.started row stays open in the trace (no completion
					// yet) with the live console attached.
					timeoutSec := 0
					if requested, ok := numericArg(call.Args["timeout_sec"]); ok && requested > 0 {
						timeoutSec = int(requested)
					}
					if timeoutSec <= 0 {
						timeoutSec = sandbox.DefaultTimeout
					}
					bgCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
						StartToCloseTimeout: time.Duration(timeoutSec)*time.Second + 10*time.Minute,
						ScheduleToCloseTimeout: time.Duration(timeoutSec)*time.Second + 15*time.Minute,
						HeartbeatTimeout: 90 * time.Second,
						// Never re-execute a detached install/build on retry: the
						// attach path in RunTool handles a worker death instead.
						RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
					})
					future := workflow.ExecuteActivity(bgCtx, ActivityRunTool, ToolRequest{RunID: input.RunID, OperationID: operationID, Name: call.Name, Role: input.Role, WorkspacePath: input.WorkspacePath, Project: input.Project, TaskID: input.TaskID, ActorID: input.ActorID, AgentID: input.AgentID, Arguments: call.Args, AllowedTools: input.ToolAllowlist, DeniedTools: input.DenyTools, ReadOnly: input.ReadOnly, Secrets: input.Secrets, Sandbox: input.Sandbox})
					if err := startTool(); err != nil {
						return result, err
					}
					commandPreview := compactJSON(call.Args["command"], 120)
					background[operationID] = &backgroundOp{future: future, command: commandPreview, started: workflow.Now(ctx), timeoutSec: timeoutSec, turn: turn}
					toolResult.Content = fmt.Sprintf("Started in background as operation %s. Do not wait idle: continue with other work, and the result (exit code + output tail) will arrive automatically as a follow-up message. The operation times out after %d s. Use kill_operation to cancel it.", operationID, timeoutSec)
					startedBackground = true
				}
				if !startedBackground {
					if err := startTool(); err != nil {
						return result, err
					}
					if err := startMCP(); err != nil {
						return result, err
					}
					// Same wall-clock semantics as the approval path: scheduling,
					// retries and execution in one number.
					toolStarted := workflow.Now(ctx)
					if err := workflow.ExecuteActivity(toolCtx, ActivityRunTool, ToolRequest{RunID: input.RunID, OperationID: operationID, Name: call.Name, Role: input.Role, WorkspacePath: input.WorkspacePath, Project: input.Project, TaskID: input.TaskID, ActorID: input.ActorID, AgentID: input.AgentID, Arguments: call.Args, AllowedTools: input.ToolAllowlist, DeniedTools: input.DenyTools, ReadOnly: input.ReadOnly, Secrets: input.Secrets, Sandbox: input.Sandbox}).Get(ctx, &toolResult); err != nil {
					toolFailed = true
					failure := toolFailureData(operationID, argumentsHash, call.Name, call.Args, err)
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
				turnImages = append(turnImages, toolResult.Images...)
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
		// Vision injection point: all tool replies of the turn (delegated ones
		// included) have landed, so the image blocks ride one trailing user
		// message — inserting them between tool messages would break the
		// tool-replies-must-stay-contiguous contract of the API.
		if len(turnImages) > 0 {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "Image(s) produced by this turn's tool calls are attached below as vision blocks; their metadata is in the tool results above.",
				Images:  turnImages,
			})
		}
		if err := emit(activityCtx, state, "turn.completed", map[string]any{"turn": turn, "tool_calls": len(completion.ToolCalls), "behavior_monitor": input.BehaviorMonitor}); err != nil {
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
	// Notes that arrived after the last drainable point (while the final turn's
	// tools were running, or after a policy stop) can no longer reach the model:
	// they are journaled as run.note_dropped instead of vanishing silently.
	if err := dropUserNotes(ctx, activityCtx, state, input.MaxTurns); err != nil {
		return result, err
	}
	// Deterministic finale: the model spent every turn on tool calls, or a policy
	// limit (time/tokens/budget) stopped the loop. One more model call without
	// tools must turn the accumulated conversation into a final answer, so a
	// limited run still produces a handoff instead of silence. The status stays
	// honest (turn_limit / time_limit / token_limit / budget_limit); events
	// record that the answer came from a forced finale and why.
	//
	// Background operations first (docs/agent-async-operations.md): a finale
	// must not strand detached commands mid-install — wait them out (each is
	// bounded by its own timeout_sec) and journal the outcomes. No further
	// model turns: the finale answer stands, the events carry the outcomes.
	if len(background) > 0 {
		if _, err := awaitBackgroundOps(ctx, activityCtx, state, &messages, background); err != nil {
			return result, err
		}
	}
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
	// Same ceiling and retry shape as the per-turn model call: the forced
	// finale can be as long a generation as any other turn, and deserves the
	// same frozen-host recovery (fresh Temporal attempt after the machine
	// wakes up instead of a dead run).
	modelCtx := workflow.ActivityOptions{StartToCloseTimeout: ModelCallActivityTimeout, HeartbeatTimeout: 90 * time.Second, ScheduleToCloseTimeout: ModelCallActivityTimeout*3 + 5*time.Minute, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second, MaximumAttempts: 3}}
	if err := executeModelWithNudge(ctx, activityCtx, state, modelCtx, ModelRequest{Model: input.Model, ProviderID: input.ProviderID, Messages: finaleMessages, RunID: input.RunID, Turn: finaleTurn, Project: input.Project, PromptCacheKey: input.TaskID}, finaleTurn, &finale); err != nil {
		failureDetail := boundedFailureDetail(err)
		if eventErr := emit(activityCtx, state, "model.failed", map[string]any{"turn": finaleTurn, "forced_finale": true, "model": input.Model, "error_type": "activity_failed", "error": failureDetail}); eventErr != nil {
			return result, eventErr
		}
		if eventErr := emit(activityCtx, state, "run.failed", map[string]any{"turn": finaleTurn, "error_type": "finale_model_call_failed", "error": failureDetail}); eventErr != nil {
			return result, eventErr
		}
		result.Status = "failed"
		return result, err
	}
	finaleData := map[string]any{"turn": finaleTurn, "forced_finale": true, "model": input.Model, "tool_call_count": len(finale.ToolCalls), "finish_reason": finale.Finish, "total_tokens": finale.Usage.TotalTokens}
	maps.Copy(finaleData, modelObservability(finale))
	if err := emit(activityCtx, state, "model.completed", finaleData); err != nil {
		return result, err
	}
	if finale.Content != "" {
		result.Answer = finale.Content
	}
	// The finale is the last chance: a reasoning model can still answer with
	// reasoning only. One explicit retry demanding plain text; if it is empty
	// again, the run FAILS with an honest error instead of completing with an
	// empty answer that reads as success after 15 turns of work.
	if strings.TrimSpace(result.Answer) == "" {
		finaleMessages = append(finaleMessages, llm.Message{Role: "system", Content: "Your reply was empty. Produce the final answer now as plain text — this is the last attempt; an empty reply fails the run."})
		if err := emit(activityCtx, state, "model.started", map[string]any{"turn": finaleTurn, "forced_finale": true, "empty_answer_retry": true, "model": input.Model}); err != nil {
			return result, err
		}
		var finaleRetry llm.Completion
		if retryErr := executeModelWithNudge(ctx, activityCtx, state, modelCtx, ModelRequest{Model: input.Model, ProviderID: input.ProviderID, Messages: finaleMessages, RunID: input.RunID, Turn: finaleTurn, Project: input.Project, PromptCacheKey: input.TaskID}, finaleTurn, &finaleRetry); retryErr != nil {
			if eventErr := emit(activityCtx, state, "model.failed", map[string]any{"turn": finaleTurn, "empty_answer_retry": true, "error_type": "activity_failed", "error": boundedFailureDetail(retryErr)}); eventErr != nil {
				return result, eventErr
			}
		} else {
			finale = finaleRetry
			if finale.Content != "" {
				result.Answer = finale.Content
			}
		}
		if strings.TrimSpace(result.Answer) == "" {
			result.Status = "failed"
			if eventErr := emit(activityCtx, state, "run.failed", map[string]any{"turn": finaleTurn, "error_type": "empty_final_answer", "error": "the model produced no answer text (reasoning only) on the finale and its retry"}); eventErr != nil {
				return result, eventErr
			}
			return result, nil
		}
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

// runTitleAndHints resolves the two startup side quests of a root run — the
// human title and the prior-knowledge hints — and journals them. It emits
// run.started (the title rides its data) and returns the rendered hint block
// for the system prompt ("" when hints are skipped or unavailable).
//
// Replay safety (docs/failure-model.md): titleParallel selects the schedule
// order, gated by GetVersion("run-title-parallel"). Version 1 schedules the
// GenerateTitle and KnowledgeHints activities together when BOTH are needed;
// their results are applied in the same order as the sequential path and
// run.started is still emitted after both complete, so only the schedule order
// differs from the recorded history. Any other version (replays of histories
// recorded before the gate) issues the original sequence: title activity →
// run.started → hints activity. When only one of the two applies, the
// sequential path runs as before — there is nothing to overlap.
func runTitleAndHints(ctx workflow.Context, activityCtx workflow.Context, state *eventState, input RunInput, started map[string]any, titleParallel workflow.Version) (string, error) {
	title := cleanTitle(input.Title)
	needTitle := input.ParentRunID == "" && title == ""
	// A team program run has no model turns at the root, so prior-knowledge
	// hints would never be consumed (docs/agent-teams.md §9).
	needHints := input.Team == nil && !input.SkipKnowledge
	hintsCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	var titleFuture workflow.Future
	var hintsFuture workflow.Future
	if titleParallel == 1 && needTitle && needHints {
		titleCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 25 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		titleFuture = workflow.ExecuteActivity(titleCtx, ActivityGenerateTitle, TitleRequest{Prompt: input.Prompt})
		hintsFuture = workflow.ExecuteActivity(hintsCtx, ActivityKnowledgeHints, HintRequest{Project: input.Project, RunID: input.RunID, TaskID: input.TaskID, ActorID: input.ActorID, Query: input.Prompt, Skills: input.Skills, TeamRoot: input.TeamRootRunID})
	}
	if input.ParentRunID == "" {
		// Root runs get a short human title for the Runs list; delegated child
		// runs are shown inside their parent. Ladder: the task's own title
		// (deterministic, no model) → one minimal model call over the prompt →
		// heuristic first line of the prompt. Best effort only: on total failure
		// the UI falls back to the agent name, so this must not fail the run.
		if title == "" {
			var modelTitle string
			var err error
			if titleFuture != nil {
				err = titleFuture.Get(ctx, &modelTitle)
			} else {
				titleCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, ScheduleToCloseTimeout: 25 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
				err = workflow.ExecuteActivity(titleCtx, ActivityGenerateTitle, TitleRequest{Prompt: input.Prompt}).Get(ctx, &modelTitle)
			}
			if err == nil {
				title = strings.TrimSpace(modelTitle)
			}
		}
		if title == "" {
			title = HeuristicTitle(input.Prompt)
		}
		if title != "" {
			started["title"] = title
		}
	}
	if err := emit(activityCtx, state, "run.started", started); err != nil {
		return "", err
	}
	var priorHints []Hint
	var hintBlock string
	if needHints {
		var hintsErr error
		if hintsFuture != nil {
			hintsErr = hintsFuture.Get(ctx, &priorHints)
		} else {
			hintsErr = workflow.ExecuteActivity(hintsCtx, ActivityKnowledgeHints, HintRequest{Project: input.Project, RunID: input.RunID, TaskID: input.TaskID, ActorID: input.ActorID, Query: input.Prompt, Skills: input.Skills, TeamRoot: input.TeamRootRunID}).Get(ctx, &priorHints)
		}
		if hintsErr != nil {
			// The common miss is the boot-order race (kernel up, journal still
			// projecting) — one short retry closes most of it without delaying
			// a healthy start. A second failure is journaled with the cause.
			// The parallel path retries only after the first attempt's future has
			// been resolved, so the recorded command order stays deterministic.
			workflow.Sleep(ctx, 3*time.Second)
			hintsErr = workflow.ExecuteActivity(hintsCtx, ActivityKnowledgeHints, HintRequest{Project: input.Project, RunID: input.RunID, TaskID: input.TaskID, ActorID: input.ActorID, Query: input.Prompt, Skills: input.Skills, TeamRoot: input.TeamRootRunID}).Get(ctx, &priorHints)
		}
		if hintsErr != nil {
			// Retrieval is best-effort, but the miss is still part of the trajectory.
			if eventErr := emit(activityCtx, state, "memory.read.failed", map[string]any{"error_type": "activation_unavailable", "detail": boundedFailureDetail(hintsErr)}); eventErr != nil {
				return "", eventErr
			}
		} else {
			// One budgeted block instead of per-hint messages: the model sees a
			// compact cue list with the knowledge_id contract, not a wall of prose.
			rendered := observation.RenderHintBlock(toObservationHints(priorHints), observation.DefaultRenderOptions())
			hintBlock = rendered.Block
			if err := emit(activityCtx, state, "memory.read", map[string]any{"hint_count": len(priorHints), "primed_hints": rendered.Kept, "primed_tokens": rendered.Tokens, "dropped_hints": rendered.Dropped}); err != nil {
				return "", err
			}
		}
	}
	return hintBlock, nil
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
	// Stream shape: deltas + first-token latency tell a real stream (many
	// chunks, early first) from a buffered one (a single late chunk) — the
	// operator reads the provider's streaming health straight off the event.
	if completion.Deltas > 0 {
		data["deltas"] = completion.Deltas
	}
	if completion.FirstTokenMs > 0 {
		data["first_token_ms"] = completion.FirstTokenMs
	}
	// A dead stream salvaged by the blocking fallback means the provider
	// generated the answer twice; the marker makes the hidden regeneration
	// visible next to the (inflated) latency. StreamMs/StreamError say how long
	// the dead stream lived and why it died — a multi-minute stall behind
	// gateway keepalives otherwise hides behind a healthy-looking completion.
	if completion.StreamFailed {
		data["stream_failed"] = true
		if completion.StreamMs > 0 {
			data["stream_ms"] = completion.StreamMs
		}
		if completion.StreamError != "" {
			if len(completion.StreamError) > 300 {
				data["stream_error"] = completion.StreamError[:300]
			} else {
				data["stream_error"] = completion.StreamError
			}
		}
	}
	// A stream recovered by the fresh-connection retry kept the transport but
	// still burned the first attempt: the event carries the dead attempt's
	// duration next to the (healthy) latency, so "latency 3.5s" never reads
	// as "the whole call took 3.5s".
	if completion.StreamRetried {
		data["stream_retried"] = true
		if completion.StreamMs > 0 {
			data["stream_retried_ms"] = completion.StreamMs
		}
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
	// The intake dialog tools only work in the intake project's own dialog
	// runs (they resolve the inbound message by the CURRENT run id). In any
	// other run they are advertised dead weight — the model sees "call it
	// exactly once first" and burns a turn on a guaranteed 404. The cut is
	// unconditional for non-intake runs: an allowlist cannot re-advertise a
	// tool that cannot work there.
	if input.Project != IntakeProjectID {
		tools = slices.DeleteFunc(tools, func(tool llm.ToolDef) bool { return intakeDialogTools[tool.Name] })
	}
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
// observe. web_fetch/web_search/fetch_image are plain reads of external
// content: a fetch that died mid-flight cannot have changed anything out
// there, so it must not become an operator reconciliation question.
var readOnlyTools = map[string]bool{
	"read_file": true, "mcp__read_file": true,
	"list_directory": true, "mcp__list_directory": true,
	"search": true, "mcp__search": true,
	"glob": true, "mcp__glob": true,
	"grep": true, "mcp__grep": true,
	"view_image": true, "mcp__view_image": true,
	"web_fetch": true, "mcp__web_fetch": true,
	"web_search": true, "mcp__web_search": true,
	"fetch_image": true, "mcp__fetch_image": true,
	"skill_search": true, "mcp__skill_search": true,
	"skill_inspect": true, "mcp__skill_inspect": true,
	"artifact_search": true, "mcp__artifact_search": true,
	"artifact_read": true, "mcp__artifact_read": true,
	"saga_list": true,
}

// browserReadOnlyActions are browser actions that only observe the page.
// click/fill/evaluate can navigate, submit forms or run arbitrary JS — a
// failure there may have landed, so those stay effect-uncertain.
var browserReadOnlyActions = map[string]bool{
	"open": true, "content": true, "screenshot": true, "close": true,
}

// isReadOnlyTool classifies a tool as side-effect free. Namespaced MCP tools
// (mcp__<server>__<tool>) classify by their bare tool name.
func isReadOnlyTool(tool string) bool {
	if readOnlyTools[tool] {
		return true
	}
	bare, _ := bareToolName(tool)
	return readOnlyTools[prefix+bare]
}

// bareToolName splits a tool name into (bare, namespaced): mcp__srv__read_file
// → (read_file, true), run_command → (run_command, false).
func bareToolName(tool string) (string, bool) {
	rest, ok := strings.CutPrefix(tool, prefix)
	if !ok {
		return tool, false
	}
	if _, bare, found := strings.Cut(rest, "__"); found {
		return bare, true
	}
	return tool, false
}

// isReadOnlyCall is the effect-classification entry point: read-only tool
// names, plus per-action classification for the browser tool.
func isReadOnlyCall(tool string, args map[string]any) bool {
	if isReadOnlyTool(tool) {
		return true
	}
	if bare, _ := bareToolName(tool); bare != "browser" {
		return false
	}
	action, _ := args["action"].(string)
	return browserReadOnlyActions[action]
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
	"web_fetch": true, "web_search": true, "fetch_image": true,
	"browser": true,
}

// policySafeTools are exempt from policy-mandated tool approval
// (approval_mode: tools): observation, knowledge-recording and escalation
// IntakeProjectID is the system project whose dialog agent classifies
// inbound channel messages (docs/inbound-messages.md). Matches
// cmd/agent-kernel's IntakeProjectID — declared here so the workflow can
// scope the dialog tools without importing the kernel main package.
const IntakeProjectID = "intake"

// intakeDialogTools work only inside an intake dialog run: they resolve the
// inbound message (its sender, its routed runs) by the CURRENT run id, so in
// any other run they are guaranteed failures (404 "no inbound message for
// this run"). advertisedTools cuts them from every non-intake run.
var intakeDialogTools = map[string]bool{
	"search_projects": true, "search_org_units": true,
	"start_task": true, "intake_decide": true,
	"run_status": true, "cancel_run": true, "run_search": true, "trace_read": true,
}

// tools the policy regime itself relies on. Consequential tools —
// run_command, write_file/edit_file, delegate, trigger writes, skill_propose,
// mcp__ tools — still need a human decision. Read-only MCP tools are exempt
// via isReadOnlyTool.
var policySafeTools = map[string]bool{
	"echo": true, "remember": true, "remember_many": true, "request_approval": true, "ask_human": true,
	"human_contacts": true, "list_triggers": true, "agents": true,
	// Managing this run's own background operations is bookkeeping, not a side
	// effect on the workspace (docs/agent-async-operations.md).
	"kill_operation": true,
	"skill_search": true, "skill_inspect": true, "skill_validate": true,
	"skill_history": true, "skill_executions": true, "skill_memory": true,
	"skill_diff": true, "skill_evaluate": true, "knowledge_lookup": true,
	"knowledge_explore": true,
	"artifact_search":   true, "artifact_read": true,
	// Artifact authoring (docs/artifact-lifecycle.md): drafts and review
	// requests are agent-writable by design — the approval decision is the
	// human half of the loop, and submits are rate-limited server-side
	// (ARTIFACT_SUBMIT_PER_DAY) instead of by an approval round.
	"artifact_create": true, "artifact_update": true, "artifact_submit": true,
	// Saga tools (docs/sagas.md) only write observation journal events — the
	// system's own memory layer, no workspace or external effect.
	"saga_register": true, "saga_attach": true,
	// user_remember writes one profile fact about the CURRENT sender — the
	// same memory layer scope, no external effect.
	"user_remember": true,
	// Intake dialog tools (docs/inbound-messages.md §7): start_task is the
	// entire point of the dialog and passes full sender-side authorization on
	// the API side, so a policy-mandated approval round trip would only stall
	// the channel conversation. The intake agent has no other consequential
	// tools (no sandbox, no delegation).
	"search_projects": true, "search_org_units": true,
	"start_task": true, "intake_decide": true,
	"run_status": true, "cancel_run": true, "run_search": true, "trace_read": true,
}

// policySafeTool reports whether a tool may run without approval when the
// effective policy demands approval for consequential tools.
func policySafeTool(name string) bool {
	return policySafeTools[name] || isReadOnlyTool(name)
}

func toolFailureData(operationID, argumentsHash, tool string, args map[string]any, err error) map[string]any {
	data := map[string]any{"operation_id": operationID, "arguments_hash": argumentsHash, "tool": tool, "error_type": "activity_failed", "error": boundedFailureDetail(err)}
	if isReadOnlyCall(tool, args) {
		// Read-only calls (plain fetches, file reads, observing browser
		// actions) have no side effect regardless of where they fail.
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
	base := "You are a careful project agent. Use tools when useful and converge to a final answer before the turn budget runs out. Every command sandbox has a writable /scratch directory (HOME and TMPDIR point there) whose files survive between the commands of one run and are wiped after the run — use it for multi-step file preparation, build scratch and package caches, and never write caches into the workspace, which may be read-only for your role. Successful verification and build command outcomes are recorded as knowledge automatically; do not restate them with remember. Call remember only when you are highly confident in a durable conclusion that goes beyond the recorded execution results, and include evidence refs. Treat retrieved memory as fallible and respect cautions."
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
	b.WriteString("# Skills\nVersioned capability packages bound to you: each has a SKILL.md procedure and a manifest contract stored in the skill registry (not files in this workspace). When a task matches a skill, follow its procedure; skill_inspect shows the full text and lists attached reference artifacts with their applicability notes. Load an artifact with artifact_read only when the task matches its description — never preemptively: these documents are long, and the budget is yours. Skill knowledge is contextual: record discoveries with remember (include skill_id and capability). If a task reveals a new repeatable capability, propose it with skill_propose — never hand-write skill files in the workspace.")
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

// executeModelWithNudge runs one call_model activity, cancellable by the
// kernel.nudge signal (the human escape hatch for a stuck provider). A stuck
// endpoint can sit through the whole silent retry ladder — idle watchdog →
// blocking fallback → Temporal retries — for the better part of an hour while
// the UI shows nothing but "waiting for the first token". The nudge cancels
// the in-flight HTTP request immediately and retries the turn: model calls
// are pure (no side effects, prompt prefix cached via PromptCacheKey), so a
// retry is always safe. Stale nudges (older turns, delivered while tools
// ran) are drained and ignored via the turn check; each applied nudge emits a
// model.nudged event for the audit trail. The returned nudged count feeds
// observability.
//
// A nudge retry re-executes the activity with FRESH timeout options, which
// would silently reset the whole patience ladder — an operator nudging every
// few minutes could keep a dead provider on life support forever. The loop
// therefore anchors to the ORIGINAL ScheduleToClose deadline: every retry
// gets only the remaining budget, so the total patience per model call is
// bounded by configuration no matter how many nudges land.
func executeModelWithNudge(ctx workflow.Context, activityCtx workflow.Context, state *eventState, opts workflow.ActivityOptions, request ModelRequest, turn int, out *llm.Completion) error {
	nudgeChannel := workflow.GetSignalChannel(ctx, NudgeSignal)
	deadline := workflow.Now(ctx).Add(opts.ScheduleToCloseTimeout)
	for {
		callOpts := opts
		if remaining := deadline.Sub(workflow.Now(ctx)); remaining > 0 {
			if remaining < callOpts.ScheduleToCloseTimeout {
				callOpts.ScheduleToCloseTimeout = remaining
			}
		} else {
			return fmt.Errorf("model call budget exhausted (schedule-to-close %s) after nudge", opts.ScheduleToCloseTimeout)
		}
		callCtx, cancelCall := workflow.WithCancel(ctx)
		callCtx = workflow.WithActivityOptions(callCtx, callOpts)
		future := workflow.ExecuteActivity(callCtx, ActivityCallModel, request)
		applyNudge := false
		nudgeActor := ""
		selector := workflow.NewSelector(ctx)
		selector.AddFuture(future, func(workflow.Future) {})
		selector.AddReceive(nudgeChannel, func(channel workflow.ReceiveChannel, _ bool) {
			var nudge Nudge
			for channel.ReceiveAsync(&nudge) {
				if nudge.Turn == 0 || nudge.Turn == turn {
					applyNudge = true
					nudgeActor = nudge.ActorID
				}
			}
			if applyNudge {
				cancelCall()
			}
		})
		selector.Select(ctx)
		err := future.Get(ctx, out)
		if err == nil {
			cancelCall()
			return nil
		}
		if applyNudge && temporal.IsCanceledError(err) {
			if eventErr := emit(activityCtx, state, "model.nudged", map[string]any{"turn": turn, "model": request.Model, "actor_id": nudgeActor}); eventErr != nil {
				return eventErr
			}
			continue
		}
		return err
	}
}

// userNotePrefix frames a mid-run chat message so the model treats it as an
// addendum to the original request rather than a new task.
const userNotePrefix = "User follow-up for this task (sent while the run was in progress — treat it as an addendum to the original request, not a new task):\n\n"

// forwardNotePrefix frames a note FORWARDED INTO A CHILD of a delegation: the
// note addresses the whole delegation, not this child's subtask specifically.
// Without the frame children burn turns answering 'this is not my job' (the
// reviewer in the rebaze case) or, worse, re-scope their own subtask to match
// an addendum meant for the lead.
const forwardNotePrefix = "A user follow-up for the WHOLE delegation was received while your subtask was running. It is forwarded for visibility — it may not concern your subtask at all. Read it, assess whether it changes YOUR part of the work: if yes, adapt at your nearest turn; if no, continue as planned and do not reply to the follow-up itself:\n\n"

// pendingUserNotes drains user notes (chat messages signalled via
// kernel.user_note while the run was in progress) from the signal channel
// without touching the conversation. Empty texts are skipped.
func pendingUserNotes(ctx workflow.Context) []UserNote {
	channel := workflow.GetSignalChannel(ctx, UserNoteSignal)
	var notes []UserNote
	var note UserNote
	for channel.ReceiveAsync(&note) {
		if strings.TrimSpace(note.Text) != "" {
			notes = append(notes, note)
		}
	}
	return notes
}

// backgroundOp is one detached run_command in flight (docs/agent-async-operations.md):
// the RunTool activity future plus the human-form bits the completion
// notification needs. Everything here is rebuilt deterministically on replay.
type backgroundOp struct {
	future     workflow.Future
	command    string
	started    time.Time
	timeoutSec int
	turn       int
}

// awaitBackgroundOps blocks until at least one pending background operation
// resolves (the RunTool activity finished — normally, failed or timed out)
// or a user note arrives. Every resolved operation gets its deferred
// tool.completed / tool.failed journal event here, and ONE batched
// notification — the outcome fragment plus the still-running list — is
// appended to the conversation so the next model turn can act on it.
// Returns the wake reason: "completed" or "note".
func awaitBackgroundOps(ctx workflow.Context, activityCtx workflow.Context, state *eventState, messages *[]llm.Message, background map[string]*backgroundOp) (string, error) {
	if len(background) == 0 {
		return "", nil
	}
	// Fast path: everything already resolved (the activity finished while the
	// turn was still executing) — no waiting, just the batch.
	ready := 0
	for _, op := range background {
		if op.future.IsReady() {
			ready++
		}
	}
	workflow.GetLogger(ctx).Debug("background wait state", "pending", len(background), "ready", ready)
	wakeReason := ""
	if ready == 0 {
		selector := workflow.NewSelector(ctx)
		woken := workflow.NewChannel(ctx)
		wakeReason := ""
		for id, op := range background {
			id, op := id, op
			selector.AddFuture(op.future, func(workflow.Future) {
				if wakeReason == "" {
					wakeReason = "completed"
				}
				woken.Send(ctx, id)
			})
		}
		selector.AddReceive(workflow.GetSignalChannel(ctx, UserNoteSignal), func(workflow.ReceiveChannel, bool) {
			if wakeReason == "" {
				wakeReason = "note"
			}
			woken.Send(ctx, "")
		})
		selector.Select(ctx)
		var wakeID string
		woken.Receive(ctx, &wakeID)
	}
	if wakeReason == "" {
		wakeReason = "completed"
	}

	// Drain EVERY operation already resolved at wake time: the notification
	// batch carries them all — one wake-up, one message, not one per op.
	resolved := make([]string, 0, len(background))
	for id, op := range background {
		if op.future.IsReady() {
			resolved = append(resolved, id)
		}
	}
	slices.Sort(resolved)

	lines := make([]string, 0, len(resolved)+2)
	for _, id := range resolved {
		op := background[id]
		var toolResult ToolResult
		activityErr := op.future.Get(ctx, &toolResult)
		elapsed := workflow.Now(ctx).Sub(op.started).Round(time.Second)
		if activityErr != nil {
			detail := boundedFailureDetail(activityErr)
			if err := emit(activityCtx, state, "tool.failed", map[string]any{"operation_id": id, "tool": "run_command", "error_type": "background_failed", "effect": "uncertain", "detail": detail}); err != nil {
				return "", err
			}
			lines = append(lines, fmt.Sprintf("- %s FAILED after %s: %s", id, elapsed, detail))
		} else {
			exit := 0
			if toolResult.ExitCode != nil {
				exit = *toolResult.ExitCode
			}
			tail := toolResult.Content
			if len(tail) > 1500 {
				tail = tail[len(tail)-1500:]
			}
			if err := emit(activityCtx, state, "tool.completed", map[string]any{"operation_id": id, "tool": "run_command", "exit_code": exit, "background": true, "status": map[bool]string{true: "ok", false: "error"}[exit == 0], "output": compactJSON(toolResult.Content, 1000)}); err != nil {
				return "", err
			}
			lines = append(lines, fmt.Sprintf("- %s finished: exit_code=%d (%s)\n  output tail:\n  %s", id, exit, elapsed, strings.ReplaceAll(tail, "\n", "\n  ")))
		}
		delete(background, id)
	}
	stillRunning := make([]string, 0, len(background))
	for id, op := range background {
		running := workflow.Now(ctx).Sub(op.started).Round(time.Second)
		stillRunning = append(stillRunning, fmt.Sprintf("- %s `%s` (running %s, timeout %ds)", id, op.command, running, op.timeoutSec))
	}
	slices.Sort(stillRunning)

	var message string
	if len(lines) > 0 {
		message = "[Background operations report]\n" + strings.Join(lines, "\n")
		if len(stillRunning) > 0 {
			message += "\n\nStill running in the background:\n" + strings.Join(stillRunning, "\n")
		}
		message += "\n\nIncorporate these results and continue."
	} else {
		// Woken by a note before any operation resolved: the note itself is
		// appended by the caller's notes path; this marker keeps the wake
		// visible in the conversation.
		message = "[Background operations] Still running:\n" + strings.Join(stillRunning, "\n")
	}
	*messages = append(*messages, llm.Message{Role: "user", Content: message})
	return wakeReason, nil
}

// runningPlanChildren lists the plan's still-running children: the awaited
// one plus everything else in flight (a note is relevant to all of them —
// the user does not address children by id).
func runningPlanChildren(inflight []*planPendingTask, awaited *planPendingTask) []string {
	ids := make([]string, 0, len(inflight)+1)
	if awaited != nil {
		ids = append(ids, awaited.childRunID)
	}
	for _, task := range inflight {
		ids = append(ids, task.childRunID)
	}
	return ids
}

// forwardUserNotesToChildren implements mid-delegation steering: while the
// lead is blocked waiting on child workflows, a chat addendum must reach the
// working children at their own nearest turn — not after the whole
// delegation lands. Each still-running child gets the note signalled into
// its own kernel.user_note mailbox (the same drain it applies to its human
// notes); the lead journals run.user_note with forwarded_to so its model
// sees at the next turn what was asked meanwhile. Notes whose children all
// finished already stay lead-only — the delegation is done, the note is an
// ordinary addendum to the lead's conversation.
func forwardUserNotesToChildren(ctx workflow.Context, activityCtx workflow.Context, state *eventState, turn int, notes []UserNote, childRunIDs []string) error {
	if len(notes) == 0 {
		return nil
	}
	for i := range notes {
		var forwarded, failed []string
		// The forward carries its own framing: children must assess relevance
		// to their own subtask instead of answering 'not my job'.
		forwardedNote := notes[i]
		forwardedNote.Text = forwardNotePrefix + strings.TrimSpace(notes[i].Text)
		for _, childRunID := range childRunIDs {
			future := workflow.SignalExternalWorkflow(ctx, WorkflowID(state.run.SourceID, state.run.Project, childRunID), "", UserNoteSignal, forwardedNote)
			if err := future.Get(ctx, nil); err != nil {
				// Unregistered/closed child: it finished between the drain and
				// the signal — not a delivery failure of the note itself, the
				// lead still records it below.
				failed = append(failed, childRunID)
			} else {
				forwarded = append(forwarded, childRunID)
			}
		}
		truncated := strings.TrimSpace(notes[i].Text)
		if len(truncated) > 4096 {
			truncated = truncated[:4096]
		}
		data := map[string]any{"turn": turn, "actor_id": notes[i].ActorID, "text": truncated, "forwarded": true}
		if len(forwarded) > 0 {
			data["forwarded_to"] = forwarded
		}
		if len(failed) > 0 {
			data["forward_failed"] = failed
		}
		if err := emit(activityCtx, state, "run.user_note", data); err != nil {
			return err
		}
	}
	return nil
}

// appendForwardedNotes appends already-forwarded notes to the lead's
// conversation so its model meets them at the next built turn (append-only
// history keeps the prompt-cache prefix stable).
func appendForwardedNotes(messages *[]llm.Message, notes []UserNote) {
	for _, note := range notes {
		*messages = append(*messages, userNoteMessage(note))
	}
}

func userNoteMessage(note UserNote) llm.Message {
	return llm.Message{Role: "user", Content: userNotePrefix + strings.TrimSpace(note.Text)}
}

// appendUserNotes journals each note as run.user_note and appends it to the
// conversation. Notes land at turn boundaries only: `messages` stays
// append-only, so the prompt-cache prefix keeps its byte-stability, and the
// model meets the addendum at the earliest turn that has not been built yet.
// The returned count tells the caller whether the run should continue
// instead of finishing.
func appendUserNotes(activityCtx workflow.Context, state *eventState, turn int, messages *[]llm.Message, notes []UserNote) (int, error) {
	count := 0
	for _, note := range notes {
		truncated := strings.TrimSpace(note.Text)
		if len(truncated) > 4096 {
			truncated = truncated[:4096]
		}
		if err := emit(activityCtx, state, "run.user_note", map[string]any{"turn": turn, "actor_id": note.ActorID, "text": truncated}); err != nil {
			return count, err
		}
		*messages = append(*messages, userNoteMessage(note))
		count++
	}
	return count, nil
}

// dropUserNotes journals notes that can no longer reach the model — the turn
// or policy budget is exhausted and no further turn will be built — as
// run.note_dropped. Mid-run user input must never vanish silently.
func dropUserNotes(ctx workflow.Context, activityCtx workflow.Context, state *eventState, turn int) error {
	for _, note := range pendingUserNotes(ctx) {
		truncated := strings.TrimSpace(note.Text)
		if len(truncated) > 4096 {
			truncated = truncated[:4096]
		}
		if err := emit(activityCtx, state, "run.note_dropped", map[string]any{"turn": turn, "actor_id": note.ActorID, "text": truncated, "reason": "budget_exhausted"}); err != nil {
			return err
		}
	}
	return nil
}

// recordEventLocalChangeID is the GetVersion change id gating emit's event
// transport (docs/failure-model.md replay rules): version 1 journals runtime
// events through a local activity running inside the workflow task process —
// RecordEvent is a fast outbox insert with no heartbeat, so N events collapse
// into the workflow tasks that already exist instead of costing one server
// round-trip per event. Both AgentRun and KnowledgeExtractionWorkflow call
// GetVersion with this id; the change id is shared, the gate position is
// fixed per workflow.
const recordEventLocalChangeID = "record-event-local"

// recordEventLocalVersion is the newest gate version; it only ever grows.
const recordEventLocalVersion workflow.Version = 1

// RecordEventLocal carries the RecordEvent implementation into the workflow
// package: the Activities instance is built at worker start with its outbox
// dependency, so workflow code cannot reference the method value directly.
// Local activities execute in-process against this function and need no
// registration. Nil keeps the queued ExecuteActivity transport.
var RecordEventLocal func(ctx context.Context, item observation.Event) error

// SetRecordEventLocal wires the production transport. Call it in the worker
// bootstrap next to RegisterActivityWithOptions(activities.RecordEvent, ...)
// with the same Activities instance, before the worker starts serving. The
// wiring must be unconditional and permanent: a history recorded through the
// local path replayed on a binary without the hook would take the
// ExecuteActivity fallback and fail the determinism check.
func SetRecordEventLocal(f func(context.Context, observation.Event) error) {
	RecordEventLocal = f
}

type eventState struct {
	run             RunInput
	frame           string
	parentFrame     string
	sequence        int
	eventScope      string
	previousEventID string
	recordLocal     workflow.Version
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
	// Version 1 of the record-event-local gate journals through a local
	// activity when the worker injected the RecordEvent implementation; a nil
	// hook (or an older gated history) keeps the queued activity transport.
	// The whole deterministic bookkeeping above runs before either transport.
	if state.recordLocal == recordEventLocalVersion && RecordEventLocal != nil {
		localCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
			ScheduleToCloseTimeout: 2 * time.Minute,
			RetryPolicy:            &temporal.RetryPolicy{InitialInterval: 500 * time.Millisecond, BackoffCoefficient: 2, MaximumInterval: 5 * time.Second, MaximumAttempts: 3},
		})
		if err := workflow.ExecuteLocalActivity(localCtx, RecordEventLocal, item).Get(ctx, nil); err != nil {
			return err
		}
	} else if err := workflow.ExecuteActivity(ctx, ActivityRecordEvent, item).Get(ctx, nil); err != nil {
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
		data := map[string]any{"knowledge_id": proposal.KnowledgeID, "proposition": proposal.Proposition, "kind": "observation", "policy_id": proposal.Policy, "command": proposal.Command, "command_class": proposal.Class, "topics": []string{proposal.Class}, "exit_code": 0, "operation_id": operationID, "arguments_hash": argumentsHash}
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
	// Same record-event-local gate as AgentRun and always the first command of
	// this workflow: the extraction journals its summary and failure events
	// through emit, so its histories need the transport gate in a fixed
	// position before anything else can branch on run configuration.
	recordLocal := workflow.GetVersion(ctx, recordEventLocalChangeID, workflow.DefaultVersion, recordEventLocalVersion)
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
		recordLocal:     recordLocal,
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

// ExtractionWorkflowIDPublic exposes the deterministic extraction workflow id
// to the server package: the external-agents kickoff starts the same workflow
// native runs use, so REJECT_DUPLICATE keeps one extraction per run for both
// paths (and a re-extraction under a bumped ExtractorVersion opens a new id
// for native and external runs alike).
func ExtractionWorkflowIDPublic(sourceID, project, runID string) string {
	return extractionWorkflowID(sourceID, project, runID)
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
	// Extraction is best-effort, but that must not mean "fails on the first
	// transient blip": the journal's ~30s token-reload window after a kernel
	// restart and flaky providers are both transient conditions. Three attempts
	// with 10s initial backoff ride them out; ScheduleToClose is raised so the
	// last attempt still gets a full StartToClose window.
	extractCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 3 * time.Minute, ScheduleToCloseTimeout: 10 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: 10 * time.Second, BackoffCoefficient: 2.0}})
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
	// Judged outcomes close the usefulness loop (plan-priming-relevance.md
	// §11): used ≠ helpful, so the extractor's verdict is a separate event the
	// projection aggregates into the knowledge's B4 ranking factor. matched_by
	// travels along (joined from the offered hints) so the tier effectiveness
	// report (docs/supervisor-model.md §6.6) can split helpful/harmful rates
	// by matcher tier — semantic versus lexical.
	feedbackByKnowledge := map[string]string{}
	for _, hint := range extraction.HintFeedback {
		if len(hint.MatchedBy) > 0 && hint.MatchedBy[0] != "" {
			feedbackByKnowledge[hint.KnowledgeID] = hint.MatchedBy[0]
		}
	}
	for _, outcome := range extraction.HintOutcomes {
		data := map[string]any{"hint_id": outcome.HintID, "knowledge_id": outcome.KnowledgeID, "outcome": outcome.Outcome, "matcher": "extraction-judgment.v1"}
		if tier, ok := feedbackByKnowledge[outcome.KnowledgeID]; ok {
			data["matched_by"] = tier
		}
		if err := emit(activityCtx, state, "hint.outcome", data); err != nil {
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
		if len(candidate.Topics) > 0 {
			data["topics"] = candidate.Topics
		}
		if len(candidate.Entities) > 0 {
			data["entities"] = candidate.Entities
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
		// Re-derivation may be the first extraction under v4+: the confirm
		// event carries areas so the projection can backfill an older node.
		if len(candidate.Topics) > 0 {
			data["topics"] = candidate.Topics
		}
		if len(candidate.Entities) > 0 {
			data["entities"] = candidate.Entities
		}
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
		{Name: "remember_many", Description: "Record MANY atomic conclusions in one call — the way to memorize a document, a map, a spec or a domain area. Decompose the material yourself: one self-contained, verifiable fact per item (a hint surfaces one item, not a chapter). Submit batches of up to 50 items; call repeatedly for larger material and report the total to the user", Parameters: map[string]any{"type": "object", "properties": map[string]any{"items": map[string]any{"type": "array", "description": "Atomic facts, one per item", "items": map[string]any{"type": "object", "properties": map[string]any{"proposition": map[string]any{"type": "string", "description": "One self-contained fact, e.g. 'The billing domain is covered by tests A, B, C in e2e/billing/'"}, "evidence": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "required": []string{"proposition"}}}}, "required": []string{"items"}}},
		{Name: "request_approval", Description: "Pause this run and request a human decision before a consequential action", Parameters: map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}, "required": []string{"action"}}},
		{Name: "ask_human", Description: "Ask a human a question when you cannot proceed without additional context or a decision. The question must be self-contained: what task you are running, what you have established, what exactly is missing and which options exist. Prefer short structured options over open-ended questions; for structured input beyond one choice, attach a form schema — the human gets a real form to fill. The run pauses until an answer arrives or the timeout expires.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"question": map[string]any{"type": "string", "description": "The exact question for the human, self-contained"}, "recipient": map[string]any{"type": "string", "description": "Logical recipient: a workspace user id/name (user:ruslan), role:admin, org:<unit-id>, or project_owner. Defaults to the operator who started this run"}, "context": map[string]any{"type": "string", "description": "Brief human-facing background: what you are doing and what you already established"}, "options": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Short answer variants to choose from"}, "schema": map[string]any{"type": "object", "description": "Optional form definition (JSON Schema subset) the human fills instead of free text: {type:'object', properties:{name:{type:'string',minLength:3},priority:{type:'string',enum:['high','normal']},count:{type:'integer',minimum:1,maximum:100},confirm:{type:'boolean'}}, required:['name']}. Supported field types: string (+enum, pattern RE2, minLength/maxLength, format: date/date-time/time/email/url/password), integer/number (+minimum/maximum), boolean, array (+enum = multi-select choices, minItems/maxItems, items format date with minItems=maxItems=2 = date range). Use it to build surveys and structured intake forms. A pattern field MUST carry a title (or description) saying what to enter — the human cannot read regexes. Max 20 fields; the answer arrives as JSON in the response", "properties": map[string]any{}}, "timeout_sec": map[string]any{"type": "integer", "description": "How long to wait for the answer"}, "timeout_policy": map[string]any{"type": "string", "enum": []string{"fallback", "fail", "retry", "escalate", "cancel"}, "description": "What happens on timeout: fallback (default) proceeds with the safest option, fail aborts the tool call, retry asks once more, escalate re-asks the project owner, cancel stops the run"}}, "required": []string{"question"}}},
		{Name: "human_contacts", Description: "Find who to ask before ask_human: search workspace users by name, role, job title or org unit, or resolve an escalation target with escalate_for (the nearest org level above that user with active people, falling back to installation admins). Returns contact cards with user_id, job_title, role, org unit and enabled channel types — channel addresses are never exposed, delivery is automatic", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "Optional substring matched against user name or id"}, "role": map[string]any{"type": "string", "description": "Filter by workspace role: admin, operator, writer, reader"}, "org_unit_id": map[string]any{"type": "string", "description": "Filter by org unit id (the exact unit, not its subtree)"}, "job_title": map[string]any{"type": "string", "description": "Filter by job title substring (QA engineer, разработчик) — group address: ask_human recipient title:<job title> fans out to everyone matching"}, "escalate_for": map[string]any{"type": "string", "description": "User id or name: resolve who to escalate to when that user cannot answer"}}}},
		{Name: "send_file", Description: "Deliver a file you produced in the workspace to a human: it is uploaded to their enabled messaging channels (Matrix, Telegram) and appears as a download in the product UI. Use it to hand over reports, archives, generated documents — not for files the user can already find in the repo", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "Workspace path of the file, e.g. /workspace/report.md"}, "note": map[string]any{"type": "string", "description": "One-line human-facing caption for the file"}, "recipient": map[string]any{"type": "string", "description": "Logical recipient like ask_human: user id/name, role:admin, org:<unit>, project_owner. Defaults to the operator who started this run"}}, "required": []string{"path"}}},
		{Name: "list_triggers", Description: "List all configured triggers (schedules, webhooks, event listeners) for this project", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "agents", Description: "List agents available for delegation in this project: exact agent_id values, display names, purpose and capabilities. Ids are not always derivable from display names, so call this before delegate/plan and use the reported agent_id verbatim", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "Optional substring matched against agent id or name"}}}},
		{Name: "create_trigger", Description: "Create a new trigger to automatically launch agent runs. Types: schedule (cron-based), webhook (HTTP endpoint), event (reacts to journal events).", Parameters: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "type": map[string]any{"type": "string", "enum": []string{"schedule", "webhook", "event"}}, "cron": map[string]any{"type": "string", "description": "Cron expression for schedule triggers, e.g. '0 9 * * 1-5'"}, "prompt": map[string]any{"type": "string", "description": "The prompt sent to the agent when the trigger fires"}, "path": map[string]any{"type": "string", "description": "URL path for webhook triggers"}, "event_type": map[string]any{"type": "string", "description": "Event type to react to for event triggers, e.g. 'tool.failed', 'artifact.review.requested', 'secret.issued'. Fires only on events observed after creation"}, "filter": map[string]any{"type": "object", "description": "Event trigger filters, all must match: actor_id, actor_type, project, data.<field> (exact string match on the event payload)", "additionalProperties": map[string]any{"type": "string"}}, "agent_id": map[string]any{"type": "string", "description": "Agent to use (optional, uses default if empty)"}}, "required": []string{"name", "type", "prompt"}}},
		{Name: "update_trigger", Description: "Update an existing trigger's configuration (enable/disable, change cron, update prompt, etc.)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"trigger_id": map[string]any{"type": "string"}, "enabled": map[string]any{"type": "boolean"}, "cron": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}}, "required": []string{"trigger_id"}}},
		{Name: "delete_trigger", Description: "Delete a trigger by ID", Parameters: map[string]any{"type": "object", "properties": map[string]any{"trigger_id": map[string]any{"type": "string"}}, "required": []string{"trigger_id"}}},
		{Name: "skill_search", Description: "List living skills in this project: versioned capability packages (SKILL.md instructions + manifest contract) stored in the skill registry, not files in the repo. Returns id, name, version, capabilities, tools", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "Optional space-separated keywords; a skill matches when any keyword occurs in id, name, capability or tool (case-insensitive); results are ranked by keyword coverage"}}}},
		{Name: "artifact_search", Description: "Search the reference artifact registry: regulation documents, approved instructions and checklists that skills point at but never inline into the prompt. Returns id, title, description (the applicability condition), provider, size_bytes, status, version, origin_type", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "Optional space-separated keywords; an artifact matches when any keyword occurs in id, title or description (case-insensitive); results are ranked by keyword coverage"}, "status": map[string]any{"type": "string", "description": "Optional lifecycle filter: approved (established documents), draft, in_review or rejected"}, "kind": map[string]any{"type": "string", "description": "Optional kind filter: reference (regulations, instructions) or work (saga work products)"}}}},
		{Name: "artifact_read", Description: "Read a reference artifact's body in 32 KiB chunks (UTF-8-safe boundaries). Load an artifact when the task matches its description — not preemptively: these documents are long and the budget is yours. skill_inspect lists the artifacts attached to a skill with their applicability notes", Parameters: map[string]any{"type": "object", "properties": map[string]any{"artifact_id": map[string]any{"type": "string", "description": "Artifact id from artifact_search or a skill's artifact listing"}, "offset": map[string]any{"type": "integer", "description": "Byte offset of the next chunk (pass next_offset from the previous call)"}}, "required": []string{"artifact_id"}}},
		{Name: "artifact_create", Description: "Author a new work product as a draft artifact (requirements, specs, reports, checklists). Drafts never enter skills or prompts until a human approves them; origin_run is recorded for you. After creating, hand it to review with artifact_submit", Parameters: map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string", "description": "Human-readable title"}, "id": map[string]any{"type": "string", "description": "Optional registry id (slug); derived from the title when omitted"}, "description": map[string]any{"type": "string", "description": "Applicability condition: when this document matters"}, "content": map[string]any{"type": "string", "description": "Markdown body"}, "mime_type": map[string]any{"type": "string", "description": "Body format (default text/markdown)"}, "kind": map[string]any{"type": "string", "description": "reference (approved docs) or work (saga work product); default work for agent-authored drafts"}, "saga_id": map[string]any{"type": "string", "description": "Saga this work product belongs to (from saga_register)"}}, "required": []string{"title", "content"}}},
		{Name: "artifact_update", Description: "Edit a draft or rejected artifact in place; artifacts in review are locked until the round closes. For an APPROVED artifact pass new_version=true: the approved version stays live and your edit opens as a revision draft — then submit it with artifact_submit for human re-approval", Parameters: map[string]any{"type": "object", "properties": map[string]any{"artifact_id": map[string]any{"type": "string", "description": "Artifact id to edit"}, "new_version": map[string]any{"type": "boolean", "description": "Approved artifact only: open the edit as a new revision draft instead of editing in place"}, "title": map[string]any{"type": "string", "description": "New title (optional)"}, "description": map[string]any{"type": "string", "description": "New applicability description (optional)"}, "content": map[string]any{"type": "string", "description": "New full body (required for new_version)"}, "kind": map[string]any{"type": "string", "description": "reference or work"}, "saga_id": map[string]any{"type": "string", "description": "Saga link (kept when omitted)"}}, "required": []string{"artifact_id"}}},
		{Name: "artifact_submit", Description: "Submit a draft artifact to human review (202 + review round). Reviewer syntax like ask_human: user:<id>, org:<unit>, role:<role>, project_owner, or a bare user id. The decision is human: approved artifacts may enter skill manifests, rejected ones return as editable drafts", Parameters: map[string]any{"type": "object", "properties": map[string]any{"artifact_id": map[string]any{"type": "string", "description": "Draft artifact id"}, "reviewers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Who should approve (same recipient syntax as ask_human)"}, "mode": map[string]any{"type": "string", "description": "all (default: everyone must approve) or any (first decision wins)"}, "note": map[string]any{"type": "string", "description": "Why this deserves approval now"}}, "required": []string{"artifact_id", "reviewers"}}},
		{Name: "secret_list", Description: "List the secrets granted to this agent: names and descriptions only, never values. Granted secrets are injected automatically as SECRET_<NAME> environment variables into every run_command call — reference them from env, do not paste literals", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "knowledge_lookup", Description: "Fetch the full knowledge item (proposition, state, topics, entities, evidence, history) by its knowledge_id. Priming cues carry a knowledge_id handle; call this when a compact cue matters to your task and you need the grounds, details or lifecycle behind it", Parameters: map[string]any{"type": "object", "properties": map[string]any{"knowledge_id": map[string]any{"type": "string", "description": "The knowledge_id from a priming cue or a prior run"}}, "required": []string{"knowledge_id"}}},
		{Name: "knowledge_explore", Description: "Research the project knowledge base (not priming): orient maps topic areas for a fuzzy question, search finds knowledge by approximate meaning (RU/EN word forms, identifiers), expand walks relations from knowledge ids, trace rebuilds an item's lifecycle history, compare surfaces contradictions and replacements, synthesize assembles a dossier (established/disputed/retired/gaps) with references. Start with orient/search when you have no ids; use expand/trace/compare on ids you found; synthesize to wrap up. Intake dialogs: pass project=<resolved project> to research the sender's project", Parameters: map[string]any{"type": "object", "properties": map[string]any{"operation": map[string]any{"type": "string", "enum": []string{"orient", "search", "expand", "trace", "compare", "synthesize"}, "description": "Research operation"}, "project": map[string]any{"type": "string", "description": "Target project for the research (intake dialog: the resolved sender's project)"}, "question": map[string]any{"type": "string", "description": "Natural-language question or task description; required for orient/search"}, "targets": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Knowledge ids; required for expand/trace, optional for compare/synthesize"}, "relations": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Restrict expand to relation types: supports, contradicts, derived_from, depends_on, supersedes, related_to"}, "max_depth": map[string]any{"type": "integer", "description": "Expand depth cap, 1-3 (default 1)"}, "max_items": map[string]any{"type": "integer", "description": "Result cap, 1-100 (default 20)"}, "include_candidates": map[string]any{"type": "boolean", "description": "Also report heuristic same-topic candidates (always marked as candidates, never facts)"}}, "required": []string{"operation"}}},
		{Name: "saga_register", Description: "Register the current task's work as a saga — an explicitly tracked long-running effort that may span many runs, chat follow-ups and delegated agents. Call it once when starting work you expect to be long or multi-stage, or when asked to track a saga. The saga rides the task: every follow-up run and delegation joins it automatically. Registering an already-bound task returns the existing saga", Parameters: map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string", "description": "Short human-readable saga title"}, "goal": map[string]any{"type": "string", "description": "Optional one-line goal/outcome of the saga"}}, "required": []string{"title"}}},
		{Name: "saga_attach", Description: "Attach the current task to an existing saga (docs/sagas.md). Use it to join related efforts — for example when taking over work another agent already tracks in a saga. The saga_id comes from saga_list", Parameters: map[string]any{"type": "object", "properties": map[string]any{"saga_id": map[string]any{"type": "string", "description": "The saga to join, from saga_list"}, "note": map[string]any{"type": "string", "description": "Optional one-line reason for joining this saga"}}, "required": []string{"saga_id"}}},
		{Name: "saga_list", Description: "List sagas registered in this project: saga_id, title, goal, bound tasks and registration time. Use it to find an existing saga before saga_attach instead of registering a duplicate", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "skill_inspect", Description: "Show a skill's full SKILL.md and manifest contract (capabilities, tools, runtime, preconditions, postconditions, evidence)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_validate", Description: "Validate a skill's manifest and return issues", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_history", Description: "List a skill's versions", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_executions", Description: "List recent executions of a skill (runs with this skill attached)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_memory", Description: "List knowledge recorded from a skill's executions (memory stays a separate temporal layer; it never modifies the skill)", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}}, "required": []string{"skill_id"}}},
		{Name: "skill_propose", Description: "Propose a new living skill from a natural-language description of a repeatable capability. The skill builder extracts the contract (procedure, capabilities, tools, runtime, constraints) referencing only tools that actually exist, and saves a 0.x draft pending human review in the Skills UI. If the skill already exists, the draft becomes its next version. Never hand-write skill files in the repo — the registry is the only source of truth", Parameters: map[string]any{"type": "object", "properties": map[string]any{"description": map[string]any{"type": "string", "description": "What the skill should do, when to use it, and any constraints — the same way you would describe it to a human"}}, "required": []string{"description"}}},
		{Name: "skill_evaluate", Description: "Run the stored evaluation suite of a skill against its current revision or a specific version (e.g. a pending draft). Each case is a grounded model call checked against expected answer patterns; results are recorded as an evaluation run. Use it to validate a proposal before asking a human to apply it", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}, "version": map[string]any{"type": "string", "description": "Optional specific version to test (a draft); defaults to the current revision"}}, "required": []string{"skill_id"}}},
		{Name: "skill_diff", Description: "Show what changed between two skill versions (manifest contract + SKILL.md). Defaults to current revision vs the newest draft proposal — use it to review an evolution proposal before recommending apply", Parameters: map[string]any{"type": "object", "properties": map[string]any{"skill_id": map[string]any{"type": "string"}, "from_version": map[string]any{"type": "string", "description": "Optional base version; defaults to the current revision"}, "to_version": map[string]any{"type": "string", "description": "Optional target version; defaults to the newest draft"}}, "required": []string{"skill_id"}}},
		{Name: "search_projects", Description: "Search projects visible to the sender of the current message by name or id substring. Use it to resolve which project an incoming request belongs to before starting a task", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "Case-insensitive substring matched against project id and name"}}}},
		{Name: "search_org_units", Description: "List the org structure tree (id, kind, name, path) to resolve teams, departments and escalation targets mentioned in requests", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "start_task", Description: "Create a task in the resolved project from the incoming message and immediately start its run, attributed to the sender. The prompt must be self-contained: the executing agent sees only what you write here. Call it at most once per message", Parameters: map[string]any{"type": "object", "properties": map[string]any{"project_id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string", "description": "Short task title"}, "prompt": map[string]any{"type": "string", "description": "Self-contained task prompt with explicit steps and checkpoints"}, "agent_id": map[string]any{"type": "string", "description": "Optional agent override; defaults to the project's default agent"}, "saga": map[string]any{"type": "string", "description": "Optional saga hint — the long-running effort this task belongs to: a saga id, its title, or words from it. Matched fuzzily (id, title, meaning); with no hint the best-matching saga above the confidence bar binds automatically"}}, "required": []string{"project_id", "title", "prompt"}}},
		{Name: "intake_decide", Description: "Record the classification of the incoming message: task (do something new), amendment (adds to running work), status (progress question), cancel (stop earlier work), chatter (no work), reply (answers an agent question). Call it exactly once, before anything else", Parameters: map[string]any{"type": "object", "properties": map[string]any{"classification": map[string]any{"type": "string", "enum": []string{"task", "amendment", "status", "cancel", "chatter", "reply"}}, "note": map[string]any{"type": "string", "description": "Optional one-line justification"}}, "required": []string{"classification"}}},
		{Name: "user_remember", Description: "Save a durable fact or preference about the CURRENT sender — how they want answers (style, length, language), what they own or lead, working agreements. Only durable facts: a one-off request is work, not memory. Never store secrets or credentials", Parameters: map[string]any{"type": "object", "properties": map[string]any{"fact": map[string]any{"type": "string", "description": "One short fact in the sender's own language, e.g. 'предпочитает краткие ответы' or 'ведёт проект Omni'"}}, "required": []string{"fact"}}},
		{Name: "run_search", Description: "Search recent agent runs you are allowed to see through the org structure: your own runs, your teammates' and subordinate units' runs (admins see everything). Returns run id, title, status, project, initiator and start time. Use it to find a run by topic or teammate, then read its trace with trace_read", Parameters: map[string]any{"type": "object", "properties": map[string]any{"project": map[string]any{"type": "string", "description": "Optional project id to narrow the search"}, "status": map[string]any{"type": "string", "description": "Optional lifecycle filter: running, completed, failed, cancelled"}, "since_days": map[string]any{"type": "integer", "description": "How far back to look, days (default 14, max 90)"}, "limit": map[string]any{"type": "integer", "description": "Optional cap, default 15"}}}},
		{Name: "trace_read", Description: "Read a compact tail of one run's journal: model turns, tool steps, errors and the final status — org visibility applies (your runs, teammates', subordinate units'). Use it to learn how a run went before continuing it, diagnosing a failure, or reporting progress", Parameters: map[string]any{"type": "object", "properties": map[string]any{"run_id": map[string]any{"type": "string", "description": "Run id from run_search"}}, "required": []string{"run_id"}}},
		{Name: "run_status", Description: "List the earlier tasks the message sender started through this channel, with the current status and answer of each run. Use it for progress questions (\"what happened to my estimate?\") instead of starting new work", Parameters: map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer", "description": "Optional cap, default 10"}}}},
		{Name: "cancel_run", Description: "Cancel a run the sender started through this channel. Only runs produced from the sender's own messages may be cancelled; pass the run_id exactly as run_status reported it", Parameters: map[string]any{"type": "object", "properties": map[string]any{"run_id": map[string]any{"type": "string", "description": "Target run id from run_status"}}, "required": []string{"run_id"}}},
		{Name: "agent_propose", Description: "Propose an evolution of an agent definition grounded in evidence: what problem you observed, what change you propose and the expected effect. Additive by contract: omitted parts keep the current definition, and the agent's purpose (description) never changes through a proposal — a human edits that themselves. The proposal lands as pending and changes nothing until a human applies it in the Agents UI. Use it to turn repeated friction into a durable definition fix; capabilities cannot be proposed — a proposal never grants new powers", Parameters: map[string]any{"type": "object", "properties": map[string]any{"agent_id": map[string]any{"type": "string", "description": "Agent whose definition should change (usually you)"}, "problem": map[string]any{"type": "string", "description": "The observed problem: what kept going wrong or missing, grounded in concrete runs"}, "change": map[string]any{"type": "string", "description": "The proposed definition change: constraints or completion checks"}, "effect": map[string]any{"type": "string", "description": "Expected effect once a human applies it"}, "constraints": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Full replacement list of constraints (what the agent must never do)"}, "completion": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Full replacement list of completion checks (what to verify before declaring done)"}, "evidence": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Run ids, knowledge ids or other refs supporting the problem"}}, "required": []string{"agent_id", "problem", "change"}}},
		{Name: "delegate", Description: "Delegate a self-contained subtask to another agent and wait for its result. The delegated agent runs with its own model, system prompt and capabilities — you cannot grant it anything beyond what it already has. Give a complete, self-contained prompt: everything the agent needs to know must be in it. Concurrent delegates share your project workspace with no isolation: parallel calls must touch disjoint files — chain mutating work (git, builds, the same files) instead of running it side by side", Parameters: map[string]any{"type": "object", "properties": map[string]any{"agent_id": map[string]any{"type": "string", "description": "Agent to delegate to"}, "prompt": map[string]any{"type": "string", "description": "Self-contained subtask description"}, "max_turns": map[string]any{"type": "integer", "description": "Turn budget for the delegated run, 24–100 (values below 24 are raised): size it generously, a child that dies mid-work returns incomplete. Omitted: the agent's configured budget applies — see max_turns in the agents listing; real subtasks need 25+ turns"}}, "required": []string{"agent_id", "prompt"}}},
		{Name: "plan", Description: "Execute a plan of dependent subtasks as a DAG across agents, in one call. Each task names an agent and a self-contained prompt.\n\nHOW TO ASSEMBLE THE DAG — follow these rules exactly; wrong dependencies corrupt the work:\n1. For EVERY task, before writing it, answer: \"what must already exist or be true before this task may start?\" EVERY such thing is a blocker — list its task id in depends_on. No blocker — no entry in depends_on.\n2. DEFAULT IS SEQUENTIAL. When you cannot name a concrete reason two tasks cannot conflict, chain them (a → b → c). Tasks may run in parallel ONLY if all three hold: disjoint file sets, no data flow between them, and neither runs build/tests/git. When in doubt — add the blocker; an unnecessary blocker costs a little time, a missing one corrupts the work.\n3. Typical blockers you must not miss: edits depend on the task that creates/changes the files they touch; build depends on the edits; tests depend on BOTH the code tasks they verify and the build; review depends on everything it reviews; the final summary depends on ALL tasks (list every id explicitly).\n4. A task consuming another task's result MUST depend on it: the answer is appended to the dependent task's prompt automatically, or place it inline with {{task-id.answer}}.\n5. All tasks run in the SAME shared project workspace with no isolation — parallel writers on the same tree clobber each other and fail. Never parallel-write.\n\nMECHANICS. A failed task skips only its transitive dependents; independent branches still finish. on_failure tunes what a failure costs you: continue (default) keeps everything independent running; stop — after the first failed or incomplete task no new task launches and the summary returns as soon as running ones settle (bounds the waste without killing healthy branches); abort — running tasks are cancelled and the summary returns immediately (only when the remaining work is pointless regardless). For step-by-step control use mode=\"wave\": the call returns after each ready batch settles instead of driving the whole DAG, so every outcome reaches you before the next batch — diagnose and re-issue the remainder corrected; completed results stay addressable as {{task-id.answer}}. A task with review_of is an acceptance gate: its run receives the submit_review tool, and a rework verdict sends the rejected tasks back with concrete feedback and re-runs their consumers (bounded by max_rework). Use this instead of several delegate calls whenever the work has ordering dependencies, can fan out read-only, or needs review gates.", Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal":       map[string]any{"type": "string", "description": "Short human-readable goal of the plan"},
				"max_rework": map[string]any{"type": "integer", "description": "How many times a reviewer may send a task back for rework, 0-4 (default 2)"},
				"mode":       map[string]any{"type": "string", "enum": []string{"full", "wave"}, "description": "full (default) drives the whole DAG in one call; wave returns after each ready batch settles so you see every outcome before committing the next — re-issue the plan for the remainder, results addressable as {{task-id.answer}}"},
				"on_failure": map[string]any{"type": "string", "enum": []string{"continue", "stop", "abort"}, "description": "What happens when a task fails or finishes incomplete: continue (default) keeps independent branches running; stop stops all new launches but lets running tasks finish; abort cancels running tasks and returns immediately — reserve it for goals that are dead regardless"},
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
							"max_turns":  map[string]any{"type": "integer", "description": "Turn budget for this task's run, 24–100 (values below 24 are raised; a build/test loop needs 25+). Omitted: the agent's configured budget applies — see max_turns in the agents listing"},
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
