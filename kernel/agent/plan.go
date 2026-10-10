package agent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
)

// MaxPlanTasks bounds the size of one plan DAG: the executor holds per-task
// state and launches real child runs, so an unbounded spec from a model is a
// resource risk, not just a big graph.
const MaxPlanTasks = 16

// Rework rounds (docs/agent-delegation.md, acceptance gates): how many times
// a reviewer may send a task back. The plan-level max_rework argument may set
// 0..MaxPlanRework; when absent the default applies. The cap is four: real
// DAGs with review gates occasionally need more than two send-backs before a
// task converges, and stopping the whole branch at two wasted full reviews.
const (
	DefaultMaxRework = 2
	MaxPlanRework    = 4
)

// PlanTask is one node of a plan DAG (docs/agent-delegation.md, plans): an
// agent plus a self-contained instruction, gated on other tasks finishing.
// ReviewOf marks an acceptance-gate task: its child run receives the
// submit_review tool and its verdict may reopen rejected upstream tasks.
type PlanTask struct {
	ID        string   `json:"id"`
	AgentID   string   `json:"agent_id"`
	Prompt    string   `json:"prompt"`
	DependsOn []string `json:"depends_on,omitempty"`
	ReviewOf  []string `json:"review_of,omitempty"`
	MaxTurns  int      `json:"max_turns,omitempty"`
}

// PlanSpec is the validated form of the `plan` tool arguments.
type PlanSpec struct {
	Goal      string     `json:"goal,omitempty"`
	MaxRework int        `json:"max_rework,omitempty"` // 0 when absent; executor applies DefaultMaxRework
	Tasks     []PlanTask `json:"tasks"`
}

// ReviewVerdict is the acceptance-gate verdict of a reviewer task
// (docs/agent-delegation.md, acceptance gates): accept, or rework with
// per-task feedback. Feedback keys are plan task ids.
type ReviewVerdict struct {
	Verdict  string            `json:"verdict"` // accept | rework
	Feedback map[string]string `json:"feedback,omitempty"`
	Summary  string            `json:"summary,omitempty"`
}

// planTaskIDPattern keeps child run ids readable and workflow-safe.
var planTaskIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// planTemplateRef matches {{task-id.answer}} placeholders in task prompts.
var planTemplateRef = regexp.MustCompile(`\{\{([a-zA-Z0-9][a-zA-Z0-9._-]*)\.answer\}\}`)

// planDAG is the derived graph used by both validation and execution.
type planDAG struct {
	DependsOn    map[string][]string // task -> deps in spec order
	Dependents   map[string][]string // task -> reverse edges in spec order
	DepsLeft     map[string]int      // task -> unresolved dep count
	TemplateRefs map[string][]string // task -> placeholder ids referenced by its prompt
}

// planSpecFromArgs converts the model's tool-call arguments into a PlanSpec.
// The returned string is a rejection reason; empty means the spec parsed.
func planSpecFromArgs(args map[string]any) (PlanSpec, string) {
	spec := PlanSpec{}
	if goal, ok := args["goal"].(string); ok {
		spec.Goal = goal
	}
	rawTasks, ok := args["tasks"].([]any)
	if !ok || len(rawTasks) == 0 {
		return spec, "tasks must be a non-empty array"
	}
	if len(rawTasks) > MaxPlanTasks {
		return spec, fmt.Sprintf("a plan may have at most %d tasks", MaxPlanTasks)
	}
	for index, raw := range rawTasks {
		fields, ok := raw.(map[string]any)
		if !ok {
			return spec, fmt.Sprintf("tasks[%d] must be an object", index)
		}
		task := PlanTask{ID: stringField(fields, "id"), AgentID: stringField(fields, "agent_id"), Prompt: stringField(fields, "prompt")}
		for _, dep := range stringList(fields["depends_on"]) {
			task.DependsOn = append(task.DependsOn, dep)
		}
		for _, id := range stringList(fields["review_of"]) {
			task.ReviewOf = append(task.ReviewOf, id)
		}
		if v, ok := numericArg(fields["max_turns"]); ok && v > 0 {
			task.MaxTurns = int(v)
		}
		spec.Tasks = append(spec.Tasks, task)
	}
	if v, ok := numericArg(args["max_rework"]); ok {
		spec.MaxRework = int(v)
	}
	return spec, ""
}

// validatePlanSpec checks the whole DAG up front — before any child run starts,
// so an invalid plan is rejected with effect=none. Template references may
// point at this task's dependencies or at results already recorded from
// earlier plans of the same run (re-plan support).
func validatePlanSpec(spec PlanSpec, priorAnswers map[string]string) (*planDAG, string) {
	if len(spec.Tasks) == 0 {
		return nil, "a plan needs at least one task"
	}
	if len(spec.Tasks) > MaxPlanTasks {
		return nil, fmt.Sprintf("a plan may have at most %d tasks", MaxPlanTasks)
	}
	dag := &planDAG{
		DependsOn:    map[string][]string{},
		Dependents:   map[string][]string{},
		DepsLeft:     map[string]int{},
		TemplateRefs: map[string][]string{},
	}
	ids := map[string]bool{}
	for index := range spec.Tasks {
		task := &spec.Tasks[index]
		task.AgentID = strings.TrimSpace(task.AgentID)
		task.Prompt = strings.TrimSpace(task.Prompt)
		if !planTaskIDPattern.MatchString(task.ID) {
			return nil, fmt.Sprintf("tasks[%d].id %q is invalid: use 1-64 chars of letters, digits, '.', '_' or '-', starting with a letter or digit", index, task.ID)
		}
		if ids[task.ID] {
			return nil, fmt.Sprintf("duplicate task id %q", task.ID)
		}
		ids[task.ID] = true
		if task.AgentID == "" {
			return nil, fmt.Sprintf("task %q: agent_id is required", task.ID)
		}
		if task.Prompt == "" {
			return nil, fmt.Sprintf("task %q: prompt is required", task.ID)
		}
		if task.MaxTurns < 0 || task.MaxTurns > MaxTurnsCeiling {
			return nil, fmt.Sprintf("task %q: max_turns must be between 1 and %d", task.ID, MaxTurnsCeiling)
		}
	}
	if spec.MaxRework < 0 || spec.MaxRework > MaxPlanRework {
		return nil, fmt.Sprintf("max_rework must be between 0 and %d", MaxPlanRework)
	}
	for index := range spec.Tasks {
		task := &spec.Tasks[index]
		seen := map[string]bool{}
		for _, dep := range task.DependsOn {
			if dep == task.ID {
				return nil, fmt.Sprintf("task %q depends on itself", task.ID)
			}
			if !ids[dep] {
				return nil, fmt.Sprintf("task %q depends on unknown task %q", task.ID, dep)
			}
			if seen[dep] {
				continue
			}
			seen[dep] = true
			dag.DependsOn[task.ID] = append(dag.DependsOn[task.ID], dep)
			dag.Dependents[dep] = append(dag.Dependents[dep], task.ID)
		}
		// Acceptance gates: review_of ids must exist in this plan and are
		// dependencies by definition — the reviewer consumes what it accepts.
		// Auto-added deps keep spec order (review_of entries first).
		for _, reviewed := range task.ReviewOf {
			if reviewed == task.ID {
				return nil, fmt.Sprintf("task %q cannot review itself", task.ID)
			}
			if !ids[reviewed] {
				return nil, fmt.Sprintf("task %q reviews unknown task %q", task.ID, reviewed)
			}
			if !seen[reviewed] {
				seen[reviewed] = true
				dag.DependsOn[task.ID] = append(dag.DependsOn[task.ID], reviewed)
				dag.Dependents[reviewed] = append(dag.Dependents[reviewed], task.ID)
			}
		}
		// Keep the task struct in sync with the derived graph: the prompt
		// builder and executor iterate task.DependsOn.
		task.DependsOn = dag.DependsOn[task.ID]
		dag.DepsLeft[task.ID] = len(dag.DependsOn[task.ID])
	}
	// Template references: a placeholder must resolve to a dependency of this
	// task (ordering is guaranteed) or to a completed result from an earlier
	// plan of the same run. A same-plan non-dependency reference is an ordering
	// violation and is rejected before execution.
	for index := range spec.Tasks {
		task := &spec.Tasks[index]
		deps := map[string]bool{}
		for _, dep := range dag.DependsOn[task.ID] {
			deps[dep] = true
		}
		for _, ref := range planTemplateRef.FindAllStringSubmatch(task.Prompt, -1) {
			id := ref[1]
			if ids[id] && !deps[id] {
				return nil, fmt.Sprintf("task %q references {{%s.answer}} but %q is in this plan and not a dependency — add it to depends_on", task.ID, id, id)
			}
			if !ids[id] && !planPriorKeyExists(priorAnswers, id) {
				return nil, fmt.Sprintf("task %q references {{%s.answer}} but %q is neither a dependency nor a completed task of an earlier plan in this run", task.ID, id, id)
			}
			dag.TemplateRefs[task.ID] = append(dag.TemplateRefs[task.ID], id)
		}
	}
	// Kahn cycle check.
	remaining := map[string]int{}
	for id, left := range dag.DepsLeft {
		remaining[id] = left
	}
	var queue []string
	for index := range spec.Tasks {
		if remaining[spec.Tasks[index].ID] == 0 {
			queue = append(queue, spec.Tasks[index].ID)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, dependent := range dag.Dependents[id] {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	if visited != len(spec.Tasks) {
		return nil, "the plan has a dependency cycle"
	}
	return dag, ""
}

// planPriorKeyExists reports whether a prior answer was recorded for the id.
// An explicitly recorded empty answer still counts: the task ran.
func planPriorKeyExists(prior map[string]string, id string) bool {
	_, ok := prior[id]
	return ok
}

// planTaskPrompt composes the child prompt. Without placeholders every
// dependency's bounded answer is appended as an "Upstream results" section;
// with at least one {{id.answer}} placeholder only the referenced results are
// inlined (explicit placement wins, nothing is appended twice).
func planTaskPrompt(task PlanTask, upstream map[string]string, prior map[string]string) string {
	refs := planTemplateRef.FindAllStringSubmatchIndex(task.Prompt, -1)
	if len(refs) == 0 {
		var b strings.Builder
		b.WriteString(task.Prompt)
		for _, dep := range task.DependsOn {
			answer, ok := upstream[dep]
			if !ok {
				answer = "[unavailable: " + dep + "]"
			}
			b.WriteString("\n\nUpstream results\n\n## " + dep + "\n" + answer)
		}
		return b.String()
	}
	var b strings.Builder
	last := 0
	for _, ref := range refs {
		b.WriteString(task.Prompt[last:ref[0]])
		id := task.Prompt[ref[2]:ref[3]]
		answer, ok := upstream[id]
		if !ok {
			answer, ok = prior[id]
		}
		if !ok {
			answer = "[unavailable: " + id + "]"
		}
		b.WriteString(answer)
		last = ref[1]
	}
	b.WriteString(task.Prompt[last:])
	return b.String()
}

// planExecution is the settled outcome of one plan run: per-task outcomes in
// spec order plus the rework history. Team runs read it for the team-level
// summary (status, rework rounds); the plain plan tool path ignores it.
type planExecution struct {
	Outcomes   []planTaskOutcome
	Rejections []planRejection
}

// completed reports whether every task of the plan settled as completed.
func (p *planExecution) completed() bool {
	if p == nil {
		return false
	}
	for index := range p.Outcomes {
		if p.Outcomes[index].Status != planStatusCompleted {
			return false
		}
	}
	return len(p.Outcomes) > 0
}

// firstFailure returns the first non-completed outcome, if any.
func (p *planExecution) firstFailure() *planTaskOutcome {
	if p == nil {
		return nil
	}
	for index := range p.Outcomes {
		if p.Outcomes[index].Status != planStatusCompleted {
			return &p.Outcomes[index]
		}
	}
	return nil
}

// planSpecToArgs renders a validated PlanSpec back into `plan` tool-call
// arguments. Team runs compile a manifest into a PlanSpec and execute it
// through the same plan executor as a model-issued plan call; this helper is
// the bridge (planSpecFromArgs inverts it losslessly for the fields we set).
func planSpecToArgs(spec PlanSpec) map[string]any {
	tasks := make([]any, 0, len(spec.Tasks))
	for index := range spec.Tasks {
		task := spec.Tasks[index]
		entry := map[string]any{"id": task.ID, "agent_id": task.AgentID, "prompt": task.Prompt}
		if len(task.DependsOn) > 0 {
			deps := make([]any, 0, len(task.DependsOn))
			for _, dep := range task.DependsOn {
				deps = append(deps, dep)
			}
			entry["depends_on"] = deps
		}
		if len(task.ReviewOf) > 0 {
			reviews := make([]any, 0, len(task.ReviewOf))
			for _, reviewed := range task.ReviewOf {
				reviews = append(reviews, reviewed)
			}
			entry["review_of"] = reviews
		}
		if task.MaxTurns > 0 {
			entry["max_turns"] = task.MaxTurns
		}
		tasks = append(tasks, entry)
	}
	args := map[string]any{"goal": spec.Goal, "tasks": tasks}
	if spec.MaxRework > 0 {
		args["max_rework"] = spec.MaxRework
	}
	return args
}

// planTaskStatus is the per-task terminal status inside a plan execution.
const (
	planStatusCompleted   = "completed"
	planStatusFailed      = "failed"
	planStatusSkipped     = "skipped"
	planStatusInvalidated = "invalidated" // result discarded: upstream was sent back or died
)

// planTaskOutcome is one task's terminal record for the summary.
type planTaskOutcome struct {
	TaskID      string
	AgentID     string
	ChildRunID  string
	Status      string // completed | failed | skipped | invalidated
	ChildStatus string
	Turns       int
	Round       int // child runs launched for this task (1 = no rework)
	Verdict     string
	Answer      string
	Error       string
	BlockedBy   string
	SkipReason  string
}

// planRejection is one reviewer rejection from the plan's rework history.
type planRejection struct {
	TaskID   string
	By       string
	Round    int
	Feedback string
}

// rejectionFeedbackBound caps one feedback entry in the summary.
const rejectionFeedbackBound = 240

// planSummary renders the tool-result content the parent model reads: every
// task with its status and, for completed tasks, a bounded final answer.
// Rework history makes the quality loop visible: who rejected what and why.
func planSummary(spec PlanSpec, outcomes []planTaskOutcome, rejections []planRejection) string {
	counts := map[string]int{}
	for index := range outcomes {
		counts[outcomes[index].Status]++
	}
	var b strings.Builder
	goal := strings.TrimSpace(spec.Goal)
	if goal == "" {
		goal = "plan"
	}
	fmt.Fprintf(&b, "Plan %q finished: %d completed, %d failed, %d skipped, %d invalidated.", goal, counts[planStatusCompleted], counts[planStatusFailed], counts[planStatusSkipped], counts[planStatusInvalidated])
	for index := range outcomes {
		outcome := &outcomes[index]
		switch outcome.Status {
		case planStatusCompleted:
			fmt.Fprintf(&b, "\n\n## %s (%s) — completed", outcome.TaskID, outcome.AgentID)
			if outcome.Round > 1 {
				fmt.Fprintf(&b, ", round %d", outcome.Round)
			}
			if outcome.Verdict != "" {
				fmt.Fprintf(&b, ", verdict %s", outcome.Verdict)
			}
			if outcome.Turns > 0 {
				fmt.Fprintf(&b, ", %d turns", outcome.Turns)
			}
			b.WriteString("\n")
			b.WriteString(outcome.Answer)
		case planStatusFailed:
			fmt.Fprintf(&b, "\n\n## %s (%s) — failed\n%s", outcome.TaskID, outcome.AgentID, outcome.Error)
		case planStatusInvalidated:
			fmt.Fprintf(&b, "\n\n## %s (%s) — invalidated (result discarded: %s)", outcome.TaskID, outcome.AgentID, outcome.SkipReason)
		default:
			reason := outcome.SkipReason
			if reason == "" {
				reason = "upstream_failed"
			}
			blocked := outcome.BlockedBy
			if blocked == "" {
				blocked = "an upstream task"
			}
			fmt.Fprintf(&b, "\n\n## %s — skipped (%s: %s)", outcome.TaskID, reason, blocked)
		}
	}
	if len(rejections) > 0 {
		b.WriteString("\n\nRework history\n")
		for index := range rejections {
			rejection := &rejections[index]
			feedback := rejection.Feedback
			if len(feedback) > rejectionFeedbackBound {
				feedback = feedback[:rejectionFeedbackBound] + "…"
			}
			fmt.Fprintf(&b, "\n- %s rejected by %s (round %d): %s", rejection.TaskID, rejection.By, rejection.Round, feedback)
		}
	}
	return b.String()
}

// stringMapField coerces a model-supplied object argument to map[string]string.
func stringMapField(value any) map[string]string {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, item := range raw {
		if text, ok := item.(string); ok {
			out[key] = text
		}
	}
	return out
}

// ReviewVerdictTool returns the tool definition injected into reviewer child
// runs (docs/agent-delegation.md, acceptance gates). It is not part of
// KernelTools: only plan reviewers see it.
func ReviewVerdictTool() llm.ToolDef {
	return llm.ToolDef{
		Name:        "submit_review",
		Description: "Submit the acceptance verdict for the plan tasks you reviewed. Call it exactly once when your review is done — the run finishes with this verdict.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict":  map[string]any{"type": "string", "enum": []string{"accept", "rework"}, "description": "accept = the reviewed work is acceptable; rework = some reviewed tasks must be redone"},
				"feedback": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "required for rework: task id -> concrete fix instructions; tasks you do not mention are accepted"},
				"summary":  map[string]any{"type": "string", "description": "2-3 sentence overall review"},
			},
			"required": []string{"verdict"},
		},
	}
}

// reviewContractPrompt is appended to a reviewer task's prompt: the output
// contract for the acceptance gate.
func reviewContractPrompt(reviewOf []string) string {
	return "\n\nAcceptance gate: you review the results of these plan tasks: " + strings.Join(reviewOf, ", ") + ". Assess whether each result meets the goal. When your review is done, call submit_review exactly once: verdict \"accept\", or verdict \"rework\" with a feedback object mapping each failing task id to concrete, actionable fix instructions (tasks you do not mention are accepted). Put the overall review in summary. The run ends when you submit the verdict."
}
