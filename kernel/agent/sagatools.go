package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// Saga tools (docs/sagas.md). A saga is an explicitly registered long-running
// effort; the binding lives at the task level, so follow-up runs and delegated
// children join automatically and the cross-agent takeover case is one
// saga_attach call. Sagas are event-sourced: saga.registered / saga.attached
// journal events are the only state, and every reader (cost rollup, saga_list)
// derives the projection from them.

// sagaEvent is one saga.* journal event relevant to bindings.
type sagaEvent struct {
	Type       string // saga.registered | saga.attached
	SagaID     string
	Title      string
	Goal       string
	TaskID     string
	RunID      string
	OccurredAt time.Time
}

// fetchSagaEvents pages through the project's saga.registered and saga.attached
// journal events (the filter takes a single type, so two queries merge in
// arrival order — same approach as kernel/cost queryEventsFiltered).
func (a *Activities) fetchSagaEvents(ctx context.Context, project, taskID string) ([]sagaEvent, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	var all []sagaEvent
	for _, eventType := range []string{"saga.registered", "saga.attached"} {
		cursor := ""
		for {
			endpoint := fmt.Sprintf("%s/v1/observations/events?project=%s&type=%s&limit=500", a.TemporalityURL, url.QueryEscape(project), eventType)
			if taskID != "" {
				endpoint += "&task=" + url.QueryEscape(taskID)
			}
			if cursor != "" {
				endpoint += "&cursor=" + url.QueryEscape(cursor)
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if err != nil {
				return nil, err
			}
			if a.APIToken != "" {
				request.Header.Set("Authorization", "Bearer "+a.APIToken)
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
			for _, ev := range page.Events {
				sagaID, _ := ev.Data["saga_id"].(string)
				if sagaID == "" || ev.Context.Task == "" {
					continue
				}
				item := sagaEvent{Type: ev.Type, SagaID: sagaID, TaskID: ev.Context.Task, RunID: ev.Context.Run, OccurredAt: ev.OccurredAt}
				if ev.Type == "saga.registered" {
					item.Title, _ = ev.Data["title"].(string)
					item.Goal, _ = ev.Data["goal"].(string)
				}
				all = append(all, item)
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
	}
	return all, nil
}

// currentBinding returns the task's latest saga binding, if any. Bindings
// are compared by time, not array order: fetchSagaEvents merges per-type
// queries, so global chronological order is not guaranteed.
func currentBinding(events []sagaEvent, taskID string) (sagaEvent, bool) {
	found := false
	var binding sagaEvent
	for _, ev := range events {
		if ev.TaskID != taskID {
			continue
		}
		if !found || !ev.OccurredAt.Before(binding.OccurredAt) {
			binding = ev
			found = true
		}
	}
	return binding, found
}

func (a *Activities) handleSagaRegister(ctx context.Context, request ToolRequest) (ToolResult, error) {
	title, _ := request.Arguments["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		return ToolResult{Content: "error: title is required"}, nil
	}
	if request.Project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	if request.TaskID == "" {
		return ToolResult{Content: "error: this run has no task, so there is nothing to bind the saga to"}, nil
	}
	goal, _ := request.Arguments["goal"].(string)

	events, err := a.fetchSagaEvents(ctx, request.Project, request.TaskID)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if binding, ok := currentBinding(events, request.TaskID); ok {
		// Idempotent: one saga per task through register. Re-attaching to
		// another saga is saga_attach's job.
		result := map[string]any{
			"saga_id": binding.SagaID, "title": binding.Title, "task_id": request.TaskID,
			"status": "existing", "note": "this task is already bound to a saga; use saga_attach to move it to a different one",
		}
		encoded, _ := json.Marshal(result)
		return ToolResult{Content: string(encoded)}, nil
	}

	sagaID := "saga-" + skillSlug(title) + "-" + shortHash(request.TaskID)
	data := map[string]any{"saga_id": sagaID, "title": title, "origin": "agent-tool"}
	if goal != "" {
		data["goal"] = goal
	}
	if err := a.emitSagaEvent(ctx, request, "saga.registered", data); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	result := map[string]any{"saga_id": sagaID, "title": title, "task_id": request.TaskID, "status": "registered"}
	encoded, _ := json.Marshal(result)
	return ToolResult{Content: string(encoded)}, nil
}

func (a *Activities) handleSagaAttach(ctx context.Context, request ToolRequest) (ToolResult, error) {
	sagaID, _ := request.Arguments["saga_id"].(string)
	sagaID = strings.TrimSpace(sagaID)
	if sagaID == "" {
		return ToolResult{Content: "error: saga_id is required"}, nil
	}
	if request.Project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	if request.TaskID == "" {
		return ToolResult{Content: "error: this run has no task, so there is nothing to bind to the saga"}, nil
	}
	note, _ := request.Arguments["note"].(string)

	events, err := a.fetchSagaEvents(ctx, request.Project, "")
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	registered := false
	for _, ev := range events {
		if ev.Type == "saga.registered" && ev.SagaID == sagaID {
			registered = true
			break
		}
	}
	if !registered {
		return ToolResult{Content: fmt.Sprintf("error: saga %q is not registered in this project; call saga_list for ids", sagaID)}, nil
	}
	if binding, ok := currentBinding(events, request.TaskID); ok && binding.SagaID == sagaID {
		result := map[string]any{"saga_id": sagaID, "task_id": request.TaskID, "status": "already_attached"}
		encoded, _ := json.Marshal(result)
		return ToolResult{Content: string(encoded)}, nil
	}

	data := map[string]any{"saga_id": sagaID, "task_id": request.TaskID}
	if note != "" {
		data["note"] = note
	}
	if err := a.emitSagaEvent(ctx, request, "saga.attached", data); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	result := map[string]any{"saga_id": sagaID, "task_id": request.TaskID, "status": "attached"}
	encoded, _ := json.Marshal(result)
	return ToolResult{Content: string(encoded)}, nil
}

type sagaSummary struct {
	SagaID     string   `json:"saga_id"`
	Title      string   `json:"title"`
	Goal       string   `json:"goal,omitempty"`
	Tasks      []string `json:"tasks"`
	Registered string   `json:"registered_at"`
	ByRun      string   `json:"registered_by_run,omitempty"`
}

func (a *Activities) handleSagaList(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.Project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	events, err := a.fetchSagaEvents(ctx, request.Project, "")
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	sagas := map[string]*sagaSummary{}
	registrationTime := map[string]time.Time{}
	for _, ev := range events {
		if ev.Type != "saga.registered" {
			continue
		}
		if existing, ok := sagas[ev.SagaID]; ok {
			if existing.Title == "" {
				existing.Title = ev.Title
			}
			continue
		}
		sagas[ev.SagaID] = &sagaSummary{SagaID: ev.SagaID, Title: ev.Title, Goal: ev.Goal, Tasks: []string{}, Registered: ev.OccurredAt.Format(time.RFC3339), ByRun: ev.RunID}
		registrationTime[ev.SagaID] = ev.OccurredAt
	}
	// The task→saga projection: the latest binding per task wins (a re-attach
	// moves the task), mirroring the cost rollup. Comparison is by time — the
	// merged event list is not globally chronological.
	type taskBinding struct {
		sagaID string
		at     time.Time
	}
	latest := map[string]taskBinding{}
	for _, ev := range events {
		if _, registered := registrationTime[ev.SagaID]; !registered {
			continue // a binding to an unknown saga has no effect
		}
		if prev, ok := latest[ev.TaskID]; ok && prev.at.After(ev.OccurredAt) {
			continue
		}
		latest[ev.TaskID] = taskBinding{sagaID: ev.SagaID, at: ev.OccurredAt}
	}
	for taskID, binding := range latest {
		if saga := sagas[binding.sagaID]; saga != nil {
			saga.Tasks = append(saga.Tasks, taskID)
		}
	}
	list := make([]sagaSummary, 0, len(sagas))
	for _, saga := range sagas {
		sort.Strings(saga.Tasks)
		list = append(list, *saga)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Registered != list[j].Registered {
			return list[i].Registered > list[j].Registered
		}
		return list[i].SagaID < list[j].SagaID
	})
	if len(list) == 0 {
		return ToolResult{Content: `{"sagas":[],"note":"no sagas registered in this project yet"}`}, nil
	}
	return ToolResult{Content: compactJSON(map[string]any{"sagas": list}, 6000)}, nil
}

// emitSagaEvent records a saga.* event in the journal via the durable outbox.
// The EventID embeds UnixNano so repeated emissions cannot collide.
func (a *Activities) emitSagaEvent(ctx context.Context, request ToolRequest, eventType string, data map[string]any) error {
	if a.Events == nil {
		return fmt.Errorf("event outbox is not configured")
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("%s/%s/%d", request.RunID, eventType, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: a.SourceID, Integration: "temporality-agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: request.Project, Run: request.RunID, Task: request.TaskID,
			Actor: observation.Actor{ID: a.SourceID, Type: "agent"},
		},
		Type: eventType,
		Data: data,
	}
	return a.Events.Enqueue(ctx, event)
}

// shortHash derives a stable 6-hex-char nonce (same task → same saga id, so
// retries of saga_register after a lost response land on the same id).
func shortHash(seed string) string {
	sum := sha256.Sum256([]byte("saga:" + seed))
	return hex.EncodeToString(sum[:])[:6]
}
