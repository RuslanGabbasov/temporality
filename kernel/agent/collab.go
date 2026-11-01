package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// Agent collaboration tools (docs/temporality-agent-collaboration-spec.md):
// get_collaborators discovers the humans and agents of the current saga;
// ResolveCollaborator maps an ask_agent address to a live target run.
// Everything derives from the journal — saga bindings are event-sourced
// (kernel/agent/sagatools.go) and delegated children carry parent linkage
// only in run.started data — so no new storage is introduced.

// collaborator is one participant row of get_collaborators (§3): only the
// fields the journal actually carries — never secrets or contact addresses.
type collaborator struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // agent | human
	Role   string `json:"role,omitempty"`
	Task   string `json:"task,omitempty"`
	Status string `json:"status,omitempty"`
	RunID  string `json:"run_id,omitempty"`
}

// runParticipant is the folded state of one run within the saga scope.
type runParticipant struct {
	RunID     string
	AgentID   string
	Role      string
	Title     string
	Status    string // running | completed | failed | cancelled
	SourceID  string
	ActorID   string // the starting principal, inherited by every delegated child
	StartedAt time.Time
}

const collaboratorsAgentCap = 50
const collaboratorsHumanCap = 20

// fetchTaskEvents pages through the task-scoped journal for a set of event
// types (the filter takes a single type per query — same shape as
// fetchSagaEvents, kernel/agent/sagatools.go).
func (a *Activities) fetchTaskEvents(ctx context.Context, project, taskID string, types []string) ([]observation.Event, error) {
	if project == "" || taskID == "" {
		return nil, nil
	}
	var all []observation.Event
	for _, eventType := range types {
		cursor := ""
		for {
			endpoint := fmt.Sprintf("%s/v1/observations/events?project=%s&task=%s&type=%s&limit=500",
				a.TemporalityURL, url.QueryEscape(project), url.QueryEscape(taskID), url.QueryEscape(eventType))
			if cursor != "" {
				endpoint += "&cursor=" + url.QueryEscape(cursor)
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if err != nil {
				return nil, err
			}
			if a.apiToken() != "" {
				request.Header.Set("Authorization", "Bearer "+a.apiToken())
			}
			response, err := a.HTTP.Do(request)
			if err != nil {
				return nil, err
			}
			if response.StatusCode >= 400 {
				response.Body.Close()
				return nil, fmt.Errorf("journal query for %s failed (HTTP %d)", eventType, response.StatusCode)
			}
			var page struct {
				Events     []observation.Event `json:"events"`
				NextCursor string              `json:"next_cursor"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&page)
			response.Body.Close()
			if decodeErr != nil {
				return nil, decodeErr
			}
			all = append(all, page.Events...)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
	}
	return all, nil
}

// sagaScope folds the saga projection for one bound task: the surviving saga
// id (merges follow the target), the task set bound to it, and the title.
// The fold compares timestamps — the journal's per-type queries are not
// globally chronological (kernel/agent/sagatools.go currentBinding).
func sagaScope(events []sagaEvent, taskID string) (sagaID string, tasks map[string]bool, title string, ok bool) {
	binding, bound := currentBinding(events, taskID)
	if !bound {
		return "", nil, "", false
	}
	sagaID = binding.SagaID
	type mergeMark struct {
		target string
		at     time.Time
	}
	merges := map[string]mergeMark{}
	deletedAt := map[string]time.Time{}
	for _, ev := range events {
		if ev.Type == "saga.merged" {
			// ev.SagaID is the TARGET; the sources ride in ev.Sources.
			for _, source := range ev.Sources {
				if prev, ok := merges[source]; ok && !prev.at.After(ev.OccurredAt) {
					continue
				}
				merges[source] = mergeMark{target: ev.SagaID, at: ev.OccurredAt}
			}
		}
		if ev.Type == "saga.deleted" {
			if prev, ok := deletedAt[ev.SagaID]; ok && prev.After(ev.OccurredAt) {
				continue
			}
			deletedAt[ev.SagaID] = ev.OccurredAt
		}
	}
	// Task → latest binding by time; a merge rebinds a source binding at merge
	// time; a detach releases the task.
	type taskBinding struct {
		sagaID string
		at     time.Time
	}
	latest := map[string]taskBinding{}
	for _, ev := range events {
		if ev.Type != "saga.registered" && ev.Type != "saga.attached" && ev.Type != "saga.detached" {
			continue
		}
		if prev, ok := latest[ev.TaskID]; ok && prev.at.After(ev.OccurredAt) {
			continue
		}
		if ev.Type == "saga.detached" {
			latest[ev.TaskID] = taskBinding{"", ev.OccurredAt}
			continue
		}
		sID := ev.SagaID
		at := ev.OccurredAt
		if m, was := merges[sID]; was && m.at.After(at) {
			// The binding predates the merge: it rebinds to the target.
			sID = m.target
			at = m.at
		}
		latest[ev.TaskID] = taskBinding{sID, at}
	}
	tasks = map[string]bool{}
	for task, taskBind := range latest {
		if taskBind.sagaID != sagaID {
			continue
		}
		if deleted, was := deletedAt[sagaID]; was && !taskBind.at.After(deleted) {
			continue
		}
		tasks[task] = true
	}
	renames := sagaRenames(events)
	if renamed, has := renames[sagaID]; has && renamed.Title != "" {
		title = renamed.Title
	} else {
		title = binding.Title
	}
	return sagaID, tasks, title, true
}

// foldRunParticipants reduces task-scoped journal events to one participant
// per run: identity from the latest run.started, status from the latest
// terminal event (retries can emit several of each — latest by time wins,
// mirroring kernel/cost). humanActors collects the humans that touched the
// scope: approvers, ask-answerers, steering-note senders.
func foldRunParticipants(events []observation.Event) (runs map[string]*runParticipant, humanActors map[string]bool) {
	runs = map[string]*runParticipant{}
	humanActors = map[string]bool{}
	for _, ev := range events {
		runID := ev.Context.Run
		if runID == "" {
			continue
		}
		switch ev.Type {
		case "run.started":
			agentID, _ := ev.Data["agent_id"].(string)
			role, _ := ev.Data["role"].(string)
			title, _ := ev.Data["title"].(string)
			prev, seen := runs[runID]
			if !seen || ev.OccurredAt.After(prev.StartedAt) {
				runs[runID] = &runParticipant{
					RunID: runID, AgentID: agentID, Role: role, Title: title,
					Status: "running", SourceID: ev.Source.ID, ActorID: ev.Context.Actor.ID, StartedAt: ev.OccurredAt,
				}
			} else if seen && prev.AgentID == "" && agentID != "" {
				prev.AgentID = agentID
			}
		case "run.completed", "run.failed", "run.cancelled":
			prev, seen := runs[runID]
			if !seen {
				// A terminal event without a started page row (paged out): still
				// a participant, minimally identified.
				prev = &runParticipant{RunID: runID, StartedAt: ev.OccurredAt}
				runs[runID] = prev
			}
			prev.Status = map[string]string{
				"run.completed": "completed", "run.failed": "failed", "run.cancelled": "cancelled",
			}[ev.Type]
		case "approval.granted":
			if approver, _ := ev.Data["approver"].(string); approver != "" {
				humanActors[approver] = true
			}
		case "human.answered":
			if actor, _ := ev.Data["actor"].(string); actor != "" {
				humanActors[actor] = true
			}
		case "run.user_note":
			if actor, _ := ev.Data["actor_id"].(string); actor != "" {
				humanActors[actor] = true
			}
		}
	}
	return runs, humanActors
}

// buildCollaborators assembles the participant rows (§3): agents from runs,
// humans from their journal touches. The calling run is excluded from the
// agent list — a collaborator is someone else. Sorts: agents by start time
// descending (the freshest work first), humans alphabetically.
func buildCollaborators(runs map[string]*runParticipant, humanActors map[string]bool, ownRunID string) []collaborator {
	agents := make([]collaborator, 0, len(runs))
	for _, run := range runs {
		if run.RunID == ownRunID {
			continue
		}
		id := run.AgentID
		if id == "" {
			// Legacy runs carry no agent_id — the run id stays addressable.
			id = "run:" + run.RunID
		}
		task := run.Title
		if len(task) > 200 {
			task = task[:200]
		}
		agents = append(agents, collaborator{
			ID: "agent:" + id, Kind: "agent", Role: run.Role, Task: task,
			Status: run.Status, RunID: run.RunID,
		})
	}
	sort.Slice(agents, func(i, j int) bool {
		return runs[agents[i].RunID].StartedAt.After(runs[agents[j].RunID].StartedAt)
	})
	if len(agents) > collaboratorsAgentCap {
		agents = agents[:collaboratorsAgentCap]
	}
	humans := make([]string, 0, len(humanActors))
	for actor := range humanActors {
		humans = append(humans, actor)
	}
	sort.Strings(humans)
	if len(humans) > collaboratorsHumanCap {
		humans = humans[:collaboratorsHumanCap]
	}
	out := agents
	for _, actor := range humans {
		out = append(out, collaborator{ID: "human:" + actor, Kind: "human"})
	}
	return out
}

// handleGetCollaborators serves the get_collaborators tool: the participant
// list of the saga bound to this run's task (§3). No saga → an empty list
// with a note, not an error — the agent just has nobody to talk to.
func (a *Activities) handleGetCollaborators(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.Project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	if request.TaskID == "" {
		return ToolResult{Content: `{"collaborators": [], "note": "this run has no task, so it has no saga scope and no collaborators"}`}, nil
	}
	sagaEvents, err := a.fetchSagaEvents(ctx, request.Project, "")
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	sagaID, tasks, title, bound := sagaScope(sagaEvents, request.TaskID)
	if !bound {
		return ToolResult{Content: `{"collaborators": [], "note": "this task is not bound to a saga — call saga_register to start tracking one, then collaborators will be discoverable"}`}, nil
	}
	var all []observation.Event
	for task := range tasks {
		pageEvents, err := a.fetchTaskEvents(ctx, request.Project, task, []string{
			"run.started", "run.completed", "run.failed", "run.cancelled",
			"approval.granted", "human.answered", "run.user_note",
		})
		if err != nil {
			return ToolResult{Content: "error: " + err.Error()}, nil
		}
		all = append(all, pageEvents...)
	}
	runs, humans := foldRunParticipants(all)
	list := buildCollaborators(runs, humans, request.RunID)
	if list == nil {
		list = []collaborator{}
	}
	result := map[string]any{"collaborators": list, "saga_id": sagaID}
	if title != "" {
		result["saga_title"] = title
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	return ToolResult{Content: string(encoded)}, nil
}

// ResolveCollaboratorRequest routes an ask_agent address to a concrete run.
type ResolveCollaboratorRequest struct {
	Project      string
	TaskID       string
	To           string
	ExcludeRunID string // the asker itself is not a valid target
}

// ResolvedCollaborator is the delivery address: the run's workflow identity
// is WorkflowID(SourceID, Project, RunID). Found with Status != running means
// a FINISHED run — the caller launches a relay ask-run instead of signaling.
type ResolvedCollaborator struct {
	Found    bool   `json:"found"`
	RunID    string `json:"run_id,omitempty"`
	SourceID string `json:"source_id,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
	Role     string `json:"role,omitempty"`
	Status   string `json:"status,omitempty"`
	ActorID  string `json:"actor_id,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// collaboratorTarget is the pure addressing core of ResolveCollaborator:
// parseCollaboratorTo normalizes the `to` argument, pickCollaboratorRun
// chooses the run from the folded participants — a live run of that agent
// first (latest start wins), else the latest terminal one with an explicit
// status so the caller reports «finished, not addressable» (§4: a completed
// delegation is not automatically addressable; no run is ever started here).
func parseCollaboratorTo(to string) (kind string, value string) {
	normalized := strings.TrimSpace(to)
	switch {
	case strings.HasPrefix(normalized, "agent:"):
		return "agent", strings.TrimPrefix(normalized, "agent:")
	case strings.HasPrefix(normalized, "human:"):
		return "human", strings.TrimPrefix(normalized, "human:")
	case strings.HasPrefix(normalized, "run:"):
		return "run", strings.TrimPrefix(normalized, "run:")
	default:
		// A delegated run id always contains '/', a root run id may not —
		// anything unprefixed is tried as a run id first.
		return "run", normalized
	}
}

func pickCollaboratorRun(toKind, toValue string, runs map[string]*runParticipant) *runParticipant {
	if toKind == "agent" {
		var live, latest *runParticipant
		for _, run := range runs {
			if run.AgentID != toValue {
				continue
			}
			if run.Status == "running" {
				if live == nil || run.StartedAt.After(live.StartedAt) {
					live = run
				}
			} else if latest == nil || run.StartedAt.After(latest.StartedAt) {
				latest = run
			}
		}
		if live != nil {
			return live
		}
		return latest
	}
	// run:<id> or a bare run id: exact match.
	if run, ok := runs[toValue]; ok {
		return run
	}
	return nil
}

// ResolveCollaborator validates an ask_agent address kernel-side (§6: the
// model-supplied `to` is never trusted): the target must be a run within the
// asker's saga scope — its tasks plus every delegation under them (children
// share the task id, so the task-scoped query sees the whole tree).
func (a *Activities) ResolveCollaborator(ctx context.Context, request ResolveCollaboratorRequest) (ResolvedCollaborator, error) {
	if request.Project == "" || request.TaskID == "" || strings.TrimSpace(request.To) == "" {
		return ResolvedCollaborator{Found: false, Detail: "project, task and to are required"}, nil
	}
	toKind, toValue := parseCollaboratorTo(request.To)
	if toKind == "human" {
		return ResolvedCollaborator{Found: false, Detail: "humans are reached with ask_human, not ask_agent"}, nil
	}
	sagaEvents, err := a.fetchSagaEvents(ctx, request.Project, "")
	if err != nil {
		return ResolvedCollaborator{}, err
	}
	_, tasks, _, bound := sagaScope(sagaEvents, request.TaskID)
	if !bound {
		return ResolvedCollaborator{Found: false, Detail: "this run's task is not bound to a saga — there is no collaboration scope to address"}, nil
	}
	var all []observation.Event
	for task := range tasks {
		pageEvents, err := a.fetchTaskEvents(ctx, request.Project, task, []string{"run.started", "run.completed", "run.failed", "run.cancelled"})
		if err != nil {
			return ResolvedCollaborator{}, err
		}
		all = append(all, pageEvents...)
	}
	runs, _ := foldRunParticipants(all)
	// The asker itself is never the target.
	delete(runs, request.ExcludeRunID)
	target := pickCollaboratorRun(toKind, toValue, runs)
	if target == nil {
		return ResolvedCollaborator{Found: false, Detail: fmt.Sprintf("no collaborator %q in this saga's scope; call get_collaborators for the available ones", request.To)}, nil
	}
	// Both live and finished targets resolve: a finished run gets a relay
	// ask-run of the same agent (docs/temporality-agent-collaboration-spec.md
	// §4 — addressing a completed agent is defined by the run-resumption
	// support, which the delegation model provides). The caller reads Status
	// to choose signal vs relay.
	return ResolvedCollaborator{Found: true, RunID: target.RunID, SourceID: target.SourceID, AgentID: target.AgentID, Role: target.Role, Status: target.Status, ActorID: target.ActorID}, nil
}
