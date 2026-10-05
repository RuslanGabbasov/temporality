package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxPlanTasks bounds the size of one plan DAG: the executor holds per-task
// state and launches real child runs, so an unbounded spec from a model is a
// resource risk, not just a big graph.
const MaxPlanTasks = 16

// PlanTask is one node of a plan DAG (docs/agent-delegation.md, plans): an
// agent plus a self-contained instruction, gated on other tasks finishing.
type PlanTask struct {
	ID        string   `json:"id"`
	AgentID   string   `json:"agent_id"`
	Prompt    string   `json:"prompt"`
	DependsOn []string `json:"depends_on,omitempty"`
	MaxTurns  int      `json:"max_turns,omitempty"`
}

// PlanSpec is the validated form of the `plan` tool arguments.
type PlanSpec struct {
	Goal  string     `json:"goal,omitempty"`
	Tasks []PlanTask `json:"tasks"`
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
		if v, ok := numericArg(fields["max_turns"]); ok && v > 0 {
			task.MaxTurns = int(v)
		}
		spec.Tasks = append(spec.Tasks, task)
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
		if task.MaxTurns < 0 || task.MaxTurns > 16 {
			return nil, fmt.Sprintf("task %q: max_turns must be between 1 and 16", task.ID)
		}
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

// planTaskStatus is the per-task terminal status inside a plan execution.
const (
	planStatusCompleted = "completed"
	planStatusFailed    = "failed"
	planStatusSkipped   = "skipped"
)

// planTaskOutcome is one task's terminal record for the summary.
type planTaskOutcome struct {
	TaskID      string
	AgentID     string
	ChildRunID  string
	Status      string // completed | failed | skipped
	ChildStatus string
	Turns       int
	Answer      string
	Error       string
	BlockedBy   string
	SkipReason  string
}

// planSummary renders the tool-result content the parent model reads: every
// task with its status and, for completed tasks, a bounded final answer.
func planSummary(spec PlanSpec, outcomes []planTaskOutcome) string {
	counts := map[string]int{}
	for index := range outcomes {
		counts[outcomes[index].Status]++
	}
	var b strings.Builder
	goal := strings.TrimSpace(spec.Goal)
	if goal == "" {
		goal = "plan"
	}
	fmt.Fprintf(&b, "Plan %q finished: %d completed, %d failed, %d skipped.", goal, counts[planStatusCompleted], counts[planStatusFailed], counts[planStatusSkipped])
	for index := range outcomes {
		outcome := &outcomes[index]
		switch outcome.Status {
		case planStatusCompleted:
			fmt.Fprintf(&b, "\n\n## %s (%s) — completed", outcome.TaskID, outcome.AgentID)
			if outcome.Turns > 0 {
				fmt.Fprintf(&b, ", %d turns", outcome.Turns)
			}
			b.WriteString("\n")
			b.WriteString(outcome.Answer)
		case planStatusFailed:
			fmt.Fprintf(&b, "\n\n## %s (%s) — failed\n%s", outcome.TaskID, outcome.AgentID, outcome.Error)
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
	return b.String()
}
