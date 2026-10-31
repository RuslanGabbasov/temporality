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
	Type       string // saga.registered | saga.attached | saga.detached | saga.renamed | saga.merged | saga.deleted
	SagaID     string
	Title      string
	Goal       string
	Sources    []string // saga.merged: source saga ids
	TaskID     string
	RunID      string
	OccurredAt time.Time
}

// sagaEventTask resolves the task an event talks about: agent-emitted events
// carry it in context, human-UI ones in data.task_id.
func sagaEventTask(ev observation.Event) string {
	if ev.Context.Task != "" {
		return ev.Context.Task
	}
	taskID, _ := ev.Data["task_id"].(string)
	return taskID
}

// fetchSagaEvents pages through the project's saga.* journal events (the
// filter takes a single type, so per-type queries merge in arrival order —
// same approach as kernel/cost queryEventsFiltered).
func (a *Activities) fetchSagaEvents(ctx context.Context, project, taskID string) ([]sagaEvent, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	var all []sagaEvent
	for _, eventType := range []string{"saga.registered", "saga.attached", "saga.detached", "saga.renamed", "saga.merged", "saga.deleted"} {
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
			for _, ev := range page.Events {
				sagaID, _ := ev.Data["saga_id"].(string)
				task := sagaEventTask(ev)
				// Renames, merges and taskless manual registrations carry no
				// saga_id/task — they still matter for the projection.
				if sagaID == "" && ev.Type != "saga.detached" {
					continue
				}
				if task == "" && ev.Type != "saga.renamed" && ev.Type != "saga.merged" {
					continue
				}
				item := sagaEvent{Type: ev.Type, SagaID: sagaID, TaskID: task, RunID: ev.Context.Run, OccurredAt: ev.OccurredAt}
				if ev.Type == "saga.registered" || ev.Type == "saga.renamed" {
					item.Title, _ = ev.Data["title"].(string)
					item.Goal, _ = ev.Data["goal"].(string)
				}
				if ev.Type == "saga.merged" {
					if raw, ok := ev.Data["source_saga_ids"].([]any); ok {
						for _, entry := range raw {
							if source, _ := entry.(string); source != "" {
								item.Sources = append(item.Sources, source)
							}
						}
					}
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
// queries, so global chronological order is not guaranteed. A saga.detached
// that is the latest event counts as "no binding".
func currentBinding(events []sagaEvent, taskID string) (sagaEvent, bool) {
	found := false
	var binding sagaEvent
	for _, ev := range events {
		if ev.TaskID != taskID {
			continue
		}
		if ev.Type != "saga.registered" && ev.Type != "saga.attached" && ev.Type != "saga.detached" {
			continue
		}
		if !found || !ev.OccurredAt.Before(binding.OccurredAt) {
			binding = ev
			found = true
		}
	}
	if found && binding.Type == "saga.detached" {
		return sagaEvent{}, false
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

	sagaID := SagaID(title, request.TaskID)
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
	// A merged source saga no longer owns its bindings: point the agent at the
	// surviving target instead of silently re-projecting.
	if target := sagaMerges(events)(sagaID); target != sagaID {
		return ToolResult{Content: fmt.Sprintf("error: saga %q was merged into %q; attach to the target saga instead", sagaID, target)}, nil
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
	MergedInto string   `json:"merged_into,omitempty"` // the saga survives only as a redirect
	Tasks      []string `json:"tasks"`
	Registered string   `json:"registered_at"`
	ByRun      string   `json:"registered_by_run,omitempty"`
}

// sagaMerges folds saga.merged events into a source→target resolver: latest
// merge per source wins by time, chains resolve to the final target.
func sagaMerges(events []sagaEvent) func(string) string {
	type merge struct {
		target string
		at     time.Time
	}
	merges := map[string]merge{}
	for _, ev := range events {
		if ev.Type != "saga.merged" {
			continue
		}
		for _, source := range ev.Sources {
			if source == "" || source == ev.SagaID {
				continue
			}
			if prev, ok := merges[source]; ok && prev.at.After(ev.OccurredAt) {
				continue
			}
			merges[source] = merge{target: ev.SagaID, at: ev.OccurredAt}
		}
	}
	return func(sagaID string) string {
		seen := map[string]bool{}
		for {
			next, ok := merges[sagaID]
			if !ok || seen[sagaID] {
				break
			}
			seen[sagaID] = true
			sagaID = next.target
		}
		return sagaID
	}
}

// sagaRenames folds saga.renamed events per saga: the latest one wins and
// overrides the registered title/goal.
func sagaRenames(events []sagaEvent) map[string]sagaEvent {
	renames := map[string]sagaEvent{}
	for _, ev := range events {
		if ev.Type != "saga.renamed" || ev.SagaID == "" {
			continue
		}
		if prev, ok := renames[ev.SagaID]; ok && prev.OccurredAt.After(ev.OccurredAt) {
			continue
		}
		renames[ev.SagaID] = ev
	}
	return renames
}

func (a *Activities) handleSagaList(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.Project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	events, err := a.fetchSagaEvents(ctx, request.Project, "")
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	// Merge is a REBIND (docs/sagas.md §8): the sources' bindings move to the
	// target at merge time and each source ceases to exist — no redirect rows.
	// The journal's per-type queries are not globally chronological, so both
	// the survival rule and the binding rewrite compare by timestamps.
	mergedAt := map[string]time.Time{}      // source → when it was merged away
	mergeTargets := map[string]string{}     // source → surviving target
	mergeTimeline := []struct {
		source, target string
		at             time.Time
	}{}
	renames := sagaRenames(events)
	deletedAt := map[string]time.Time{}
	sagas := map[string]*sagaSummary{}
	registrationTime := map[string]time.Time{}
	for _, ev := range events {
		switch ev.Type {
		case "saga.registered":
			if prev, ok := mergedAt[ev.SagaID]; ok && !ev.OccurredAt.After(prev) {
				continue // registered before it was merged away: not a revival
			}
			if existing, ok := sagas[ev.SagaID]; ok {
				if existing.Title == "" {
					existing.Title = ev.Title
				}
				continue
			}
			sagas[ev.SagaID] = &sagaSummary{SagaID: ev.SagaID, Title: ev.Title, Goal: ev.Goal, Tasks: []string{}, Registered: ev.OccurredAt.Format(time.RFC3339), ByRun: ev.RunID}
			registrationTime[ev.SagaID] = ev.OccurredAt
		case "saga.renamed":
			// Folded by sagaRenames below — nothing to do here.
			_ = ev
		case "saga.deleted":
			if prev, ok := deletedAt[ev.SagaID]; ok && prev.After(ev.OccurredAt) {
				continue
			}
			deletedAt[ev.SagaID] = ev.OccurredAt
		case "saga.merged":
			// ev.SagaID is the TARGET; the sources ride in ev.Sources.
			target := ev.SagaID
			if target == "" {
				continue
			}
			for _, source := range ev.Sources {
				if prev, ok := mergedAt[source]; ok && !prev.After(ev.OccurredAt) {
					continue
				}
				mergedAt[source] = ev.OccurredAt
				mergeTargets[source] = target
				mergeTimeline = append(mergeTimeline, struct {
					source, target string
					at             time.Time
				}{source, target, ev.OccurredAt})
			}
		}
	}
	// The saga list: registered-and-alive sagas only. Deleted or merged-away
	// sources are not rows at all; a post-merge re-registration revives.
	for sagaID := range sagas {
		if deleted, ok := deletedAt[sagaID]; ok && !registrationTime[sagaID].After(deleted) {
			delete(sagas, sagaID)
		}
		if merged, ok := mergedAt[sagaID]; ok && !registrationTime[sagaID].After(merged) {
			delete(sagas, sagaID)
		}
	}
	// Human renames override the registered title/goal.
	for sagaID, ev := range renames {
		if saga := sagas[sagaID]; saga != nil {
			if ev.Title != "" {
				saga.Title = ev.Title
			}
			if ev.Goal != "" {
				saga.Goal = ev.Goal
			}
		}
	}
	// The task→saga projection: the latest binding per task wins (a re-attach
	// moves the task, a detach unbinds it), mirroring the cost rollup.
	// Comparison is by time — the merged event list is not globally
	// chronological. A merge rebinds every task still on a source onto the
	// target at merge time; bindings created after the merge cannot point at
	// the source (saga_attach refuses merged sources).
	type taskBinding struct {
		sagaID string
		at     time.Time
	}
	latest := map[string]taskBinding{}
	// Non-merge events first, in whatever order the journal gave them: the
	// per-task "latest wins by time" comparison below does not care.
	for _, ev := range events {
		if ev.Type == "saga.deleted" {
			for taskID, prev := range latest {
				if prev.sagaID != ev.SagaID || prev.at.After(ev.OccurredAt) {
					continue
				}
				if target, ok := mergeTargets[ev.SagaID]; ok && target != ev.SagaID {
					latest[taskID] = taskBinding{sagaID: target, at: ev.OccurredAt}
				} else {
					delete(latest, taskID)
				}
			}
			continue
		}
		if ev.Type != "saga.registered" && ev.Type != "saga.attached" && ev.Type != "saga.detached" {
			continue
		}
		if prev, ok := latest[ev.TaskID]; ok && prev.at.After(ev.OccurredAt) {
			continue
		}
		binding := taskBinding{sagaID: ev.SagaID, at: ev.OccurredAt}
		if ev.Type != "saga.detached" {
			if _, registered := registrationTime[ev.SagaID]; !registered {
				continue // a binding to an unknown saga has no effect
			}
		} else {
			binding.sagaID = ""
		}
		latest[ev.TaskID] = binding
	}
	// Merge rebinds, oldest merge first: every binding that points at a
	// source (or at a source merged earlier in the same pass) and whose
	// binding time predates the merge moves onto the target.
	mergePass := make([]struct {
		source, target string
		at             time.Time
	}, 0, len(mergeTimeline))
	seenMerge := map[string]bool{}
	for _, m := range mergeTimeline {
		key := m.source + "→" + m.target
		if seenMerge[key] {
			continue
		}
		seenMerge[key] = true
		mergePass = append(mergePass, m)
	}
	sort.Slice(mergePass, func(i, j int) bool { return mergePass[i].at.Before(mergePass[j].at) })
	for _, m := range mergePass {
		for taskID, prev := range latest {
			if prev.sagaID == m.source && !prev.at.After(m.at) {
				latest[taskID] = taskBinding{sagaID: m.target, at: m.at}
			}
		}
	}
	for taskID, binding := range latest {
		if binding.sagaID == "" {
			continue // detached: back to the unbound pool
		}
		// Merge rebinds already re-pointed sources at their targets above —
		// no further resolution needed here.
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

// SagaID derives the saga id from a title and seed (docs/sagas.md):
// "saga-" + slug(title) + "-" + shortHash(seed). The agent tool seeds it
// with the task id (retries land on the same saga); the human-UI route seeds
// it with the bound task or a title+actor+time nonce.
func SagaID(title, seed string) string {
	return "saga-" + skillSlug(title) + "-" + shortHash(seed)
}

// shortHash derives a stable 6-hex-char nonce (same task → same saga id, so
// retries of saga_register after a lost response land on the same id).
func shortHash(seed string) string {
	sum := sha256.Sum256([]byte("saga:" + seed))
	return hex.EncodeToString(sum[:])[:6]
}

// liveSagas projects the event log into the currently-live saga list: every
// registered saga that is not merged away, with its latest title/goal.
func liveSagas(events []sagaEvent) []sagaEvent {
	mergeTarget := sagaMerges(events)
	type state struct {
		title, goal string
		last        time.Time
	}
	order := []string{}
	states := map[string]*state{}
	for _, ev := range events {
		target := mergeTarget(ev.SagaID)
		if target != ev.SagaID {
			continue // merged away — the target carries the effort now
		}
		st, ok := states[ev.SagaID]
		if !ok {
			st = &state{}
			states[ev.SagaID] = st
			order = append(order, ev.SagaID)
		}
		if ev.OccurredAt.After(st.last) {
			st.last = ev.OccurredAt
		}
		if ev.Title != "" {
			st.title = ev.Title
		}
		if ev.Goal != "" {
			st.goal = ev.Goal
		}
	}
	out := make([]sagaEvent, 0, len(order))
	for _, id := range order {
		st := states[id]
		out = append(out, sagaEvent{Type: "saga.registered", SagaID: id, Title: st.title, Goal: st.goal, OccurredAt: st.last})
	}
	return out
}

// sagaResolveConfidence marks how sure the resolver is: exact ids and titles
// bind outright, semantic matches bind only above the auto-attach bar.
type sagaResolveConfidence int

const (
	sagaUnresolved sagaResolveConfidence = iota
	sagaExact                            // saga id or exact title match
	sagaFuzzy                            // unique substring / semantic match
)

// resolveSaga matches the caller's free-text saga hint onto one live saga of
// the project: exact id, exact title (case-insensitive), unique substring,
// then semantic proximity via the multilingual embedder (the same bridge
// skill_search uses — the user writes Russian, sagas are named in Russian or
// English). Empty hint returns unresolved without touching the embedder.
func (a *Activities) resolveSaga(ctx context.Context, project, hint string, sagas []sagaEvent) (sagaEvent, sagaResolveConfidence) {
	hint = strings.TrimSpace(hint)
	if hint == "" || len(sagas) == 0 {
		return sagaEvent{}, sagaUnresolved
	}
	lower := strings.ToLower(hint)
	for _, saga := range sagas {
		if saga.SagaID == hint {
			return saga, sagaExact
		}
	}
	for _, saga := range sagas {
		if strings.EqualFold(strings.TrimSpace(saga.Title), hint) {
			return saga, sagaExact
		}
	}
	substring := []sagaEvent{}
	matchedWords := 0
	for _, saga := range sagas {
		title := strings.ToLower(strings.TrimSpace(saga.Title))
		goal := strings.ToLower(saga.Goal)
		hits := 0
		for _, word := range strings.Fields(lower) {
			if len([]rune(word)) < 4 {
				continue // short words match everything
			}
			if strings.Contains(title, word) || strings.Contains(goal, word) {
				hits++
			}
		}
		if hits == 0 {
			continue
		}
		if hits > matchedWords {
			matchedWords, substring = hits, []sagaEvent{saga}
		} else if hits == matchedWords {
			substring = append(substring, saga) // two sagas equally close — ambiguous
		}
	}
	if len(substring) == 1 {
		return substring[0], sagaFuzzy
	}
	if len(substring) > 1 {
		return sagaEvent{}, sagaUnresolved // ambiguous hint — better unbound than wrong
	}
	// Semantic tier: embed the hint and every saga's title+goal, take the top
	// match above the bar. Failures degrade to unresolved (task starts
	// unbound — the human can attach it in two clicks).
	vectors, err := a.EmbedTexts(ctx, project, EmbedQuery, []string{hint})
	if err != nil || len(vectors) != 1 {
		return sagaEvent{}, sagaUnresolved
	}
	texts := make([]string, len(sagas))
	for i, saga := range sagas {
		texts[i] = strings.TrimSpace(saga.Title + " " + saga.Goal)
	}
	passageVectors, err := a.EmbedTexts(ctx, project, EmbedPassage, texts)
	if err != nil || len(passageVectors) != len(sagas) {
		return sagaEvent{}, sagaUnresolved
	}
	best, bestScore := -1, float32(0)
	for i, vec := range passageVectors {
		score := cosine32(vectors[0], vec)
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	// The margin check needs the runner-up: a hint sitting between two sagas
	// must not bind to either.
	runnerUp := float32(0)
	for i, vec := range passageVectors {
		if i == best {
			continue
		}
		if score := cosine32(vectors[0], vec); score > runnerUp {
			runnerUp = score
		}
	}
	if best < 0 || bestScore < sagaAutoAttachCosine || bestScore-runnerUp < sagaAutoAttachMargin {
		return sagaEvent{}, sagaUnresolved
	}
	return sagas[best], sagaFuzzy
}

// The auto-attach bar: e5-small relevant pairs sit at 0.6–0.8 cosine; the
// bar plus a margin over the runner-up keeps a vague hint unbound instead of
// wrongly bound (a wrong saga is worse than no saga — the human fixes the
// latter in two clicks).
const (
	sagaAutoAttachCosine = 0.6
	sagaAutoAttachMargin = 0.05
)

// BindIntakeSaga attaches a channel-started task to the project saga it
// belongs to (docs/inbound-messages.md: the dialog continues an effort, not
// just a project). Resolution order: the agent's explicit hint (fuzzy — id,
// title, substring, meaning), then an automatic match of the task's own
// title+prompt against the live sagas. A confident match emits saga.attached
// in the TARGET project's scope and returns its id; an ambiguous or absent
// one leaves the task unbound and returns the top candidates so the intake
// dialog can name them.
func (a *Activities) BindIntakeSaga(ctx context.Context, project, taskID, runID, sagaHint, taskText string) (boundID string, note string, candidates []map[string]any) {
	events, err := a.fetchSagaEvents(ctx, project, "")
	if err != nil {
		return "", "saga lookup failed: " + err.Error(), nil
	}
	sagas := liveSagas(events)
	if len(sagas) == 0 {
		return "", "", nil
	}
	hint := strings.TrimSpace(sagaHint)
	var resolved sagaEvent
	confidence := sagaUnresolved
	if hint != "" {
		resolved, confidence = a.resolveSaga(ctx, project, hint, sagas)
		if confidence == sagaUnresolved {
			return "", fmt.Sprintf("saga hint %q did not match any saga; task left unbound", hint), sagaCandidates(sagas)
		}
	} else {
		resolved, confidence = a.resolveSaga(ctx, project, strings.TrimSpace(taskText), sagas)
		if confidence == sagaUnresolved {
			return "", "", nil // nothing close enough — unbound is the honest answer
		}
	}
	_ = confidence
	attach := ToolRequest{Project: project, RunID: runID, TaskID: taskID, OperationID: runID + "/saga-attach"}
	data := map[string]any{"saga_id": resolved.SagaID, "task_id": taskID, "origin": "intake-auto"}
	if err := a.emitSagaEvent(ctx, attach, "saga.attached", data); err != nil {
		return "", "saga attach failed: " + err.Error(), nil
	}
	return resolved.SagaID, "bound to saga «" + resolved.Title + "»", nil
}

// sagaCandidates renders the top of the live saga list for the dialog: the
// model names them when a hint did not resolve.
func sagaCandidates(sagas []sagaEvent) []map[string]any {
	out := make([]map[string]any, 0, len(sagas))
	for i, saga := range sagas {
		if i >= 5 {
			break
		}
		out = append(out, map[string]any{"saga_id": saga.SagaID, "title": saga.Title})
	}
	return out
}
