package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
)

// Manual saga management HTTP surface (docs/sagas.md §8). Sagas stay
// event-sourced — no tables — so these routes only validate against the
// journal and emit events through the durable outbox:
//
//	POST /v1/workspace/sagas                    {title, goal?, task_id?}
//	POST /v1/workspace/sagas/{id}/rename        {title?, goal?}
//	POST /v1/workspace/sagas/{id}/merge         {source_saga_ids: [...]}
//	POST /v1/workspace/tasks/{taskID}/saga      {saga_id}   ("" detaches)
//
// Every event carries origin "human-ui" and the acting user, so the
// projections (kernel/cost sagaBindings, agent saga_list) replay the same
// history the agent tools write into.

// sagaLifecycleOutbox is the durable-outbox surface for saga events.
type sagaLifecycleOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// emitSagaLifecycleEvent journals one manual saga mutation, best-effort: the
// projection derives from all saga.* events, so a lost event degrades the UI
// hint but never corrupts state. Context mirrors the agent tools (project and
// task set, human actor) so project-filtered journal queries find the event.
func emitSagaLifecycleEvent(ctx context.Context, events sagaLifecycleOutbox, eventType, sagaID, project, taskID, actor string, data map[string]any) {
	if events == nil {
		return
	}
	if actor == "" {
		actor = "workspace"
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("saga-lifecycle/%s/%s/%d", sagaID, eventType, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: project, Task: taskID,
			Actor: observation.Actor{ID: actor, Type: "human"},
		},
		Type: eventType,
		Data: data,
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit saga lifecycle event", "saga", sagaID, "type", eventType, "error", err)
	}
}

// sagaJournalPage fetches one page of a single-type journal query.
func sagaJournalPage(ctx context.Context, journalURL, token, project, eventType, cursor string) ([]observation.Event, string, error) {
	path := fmt.Sprintf("%s/v1/observations/events?project=%s&type=%s&limit=500", journalURL, url.QueryEscape(project), eventType)
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if response.StatusCode != 200 {
		return nil, "", fmt.Errorf("journal query for %s: %s", eventType, strings.TrimSpace(string(body)))
	}
	var page struct {
		Events     []observation.Event `json:"events"`
		NextCursor string              `json:"next_cursor"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", err
	}
	return page.Events, page.NextCursor, nil
}

// sagaRegisteredIDs pages through the project's saga.registered events and
// returns the set of known saga ids. Registration alone is the existence
// proof: no other event type creates a saga.
func sagaRegisteredIDs(ctx context.Context, journalURL, token, project string) (map[string]bool, error) {
	ids := map[string]bool{}
	cursor := ""
	for {
		events, next, err := sagaJournalPage(ctx, journalURL, token, project, "saga.registered", cursor)
		if err != nil {
			return nil, err
		}
		for _, ev := range events {
			if id, _ := ev.Data["saga_id"].(string); id != "" {
				ids[id] = true
			}
		}
		if next == "" {
			return ids, nil
		}
		cursor = next
	}
}

type sagaCreateRequest struct {
	Title  string `json:"title"`
	Goal   string `json:"goal"`
	TaskID string `json:"task_id"`
}

type sagaRenameRequest struct {
	Title string `json:"title"`
	Goal  string `json:"goal"`
}

type sagaMergeRequest struct {
	SourceSagaIDs []string `json:"source_saga_ids"`
}

type taskSagaBindRequest struct {
	SagaID string `json:"saga_id"`
}

// RegisterSagaRoutes mounts the manual saga management endpoints. Writer-only:
// renaming, merging and re-binding change how a project's spend is accounted.
func RegisterSagaRoutes(mux *http.ServeMux, gate *controlplane.Gate, events sagaLifecycleOutbox, journalURL string) {
	requireProject := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project is required"))
			return "", false
		}
		if !gate.Allow(w, r, controlplane.RoleWriter, project) {
			return "", false
		}
		return project, true
	}

	// Create a saga by hand, optionally binding a task to it right away (the
	// "account this run's work" entry point of docs/sagas.md §8).
	mux.HandleFunc("POST /v1/workspace/sagas", func(w http.ResponseWriter, r *http.Request) {
		project, ok := requireProject(w, r)
		if !ok {
			return
		}
		var req sagaCreateRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		req.Title = strings.TrimSpace(req.Title)
		req.TaskID = strings.TrimSpace(req.TaskID)
		if req.Title == "" {
			writeError(w, 422, errors.New("title is required"))
			return
		}
		actor := requestUserID(r)
		// Seed with the bound task when present so retries converge on one id;
		// a taskless saga gets a unique nonce — same-title sagas must not collide.
		seed := req.TaskID
		if seed == "" {
			seed = req.Title + "\x00" + actor + "\x00" + strconv.FormatInt(time.Now().UnixNano(), 10)
		}
		sagaID := agent.SagaID(req.Title, seed)
		data := map[string]any{"saga_id": sagaID, "title": req.Title, "origin": "human-ui", "actor_id": actor}
		if req.Goal != "" {
			data["goal"] = req.Goal
		}
		if req.TaskID != "" {
			data["task_id"] = req.TaskID
		}
		emitSagaLifecycleEvent(r.Context(), events, "saga.registered", sagaID, project, req.TaskID, actor, data)
		writeJSON(w, 201, map[string]any{"saga_id": sagaID, "title": req.Title, "task_id": req.TaskID, "status": "registered"})
	})

	// Rename a saga / fix its goal: the latest saga.renamed wins in every
	// projection, so history stays intact and the correction is auditable.
	mux.HandleFunc("POST /v1/workspace/sagas/{sagaID}/rename", func(w http.ResponseWriter, r *http.Request) {
		project, ok := requireProject(w, r)
		if !ok {
			return
		}
		var req sagaRenameRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		req.Title = strings.TrimSpace(req.Title)
		req.Goal = strings.TrimSpace(req.Goal)
		if req.Title == "" && req.Goal == "" {
			writeError(w, 422, errors.New("title or goal is required"))
			return
		}
		sagaID := r.PathValue("sagaID")
		known, err := sagaRegisteredIDs(r.Context(), journalURL, os.Getenv("TEMPORALITY_API_TOKEN"), project)
		if err != nil {
			writeError(w, 502, fmt.Errorf("journal query failed: %w", err))
			return
		}
		if !known[sagaID] {
			writeError(w, 404, fmt.Errorf("saga %q is not registered in project %q", sagaID, project))
			return
		}
		data := map[string]any{"saga_id": sagaID, "origin": "human-ui", "actor_id": requestUserID(r)}
		if req.Title != "" {
			data["title"] = req.Title
		}
		if req.Goal != "" {
			data["goal"] = req.Goal
		}
		emitSagaLifecycleEvent(r.Context(), events, "saga.renamed", sagaID, project, "", requestUserID(r), data)
		writeJSON(w, 200, map[string]any{"saga_id": sagaID, "title": req.Title, "goal": req.Goal, "status": "renamed"})
	})

	// Merge sagas: the sources' bindings re-project onto the target and the
	// sources keep a redirect row. Chains (a target merged further) resolve in
	// the projection, so no rewrite is needed here.
	mux.HandleFunc("POST /v1/workspace/sagas/{sagaID}/merge", func(w http.ResponseWriter, r *http.Request) {
		project, ok := requireProject(w, r)
		if !ok {
			return
		}
		var req sagaMergeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		sagaID := r.PathValue("sagaID")
		if len(req.SourceSagaIDs) == 0 {
			writeError(w, 422, errors.New("source_saga_ids is required"))
			return
		}
		known, err := sagaRegisteredIDs(r.Context(), journalURL, os.Getenv("TEMPORALITY_API_TOKEN"), project)
		if err != nil {
			writeError(w, 502, fmt.Errorf("journal query failed: %w", err))
			return
		}
		if !known[sagaID] {
			writeError(w, 404, fmt.Errorf("target saga %q is not registered in project %q", sagaID, project))
			return
		}
		sources := make([]string, 0, len(req.SourceSagaIDs))
		for _, source := range req.SourceSagaIDs {
			source = strings.TrimSpace(source)
			if source == sagaID {
				writeError(w, 422, fmt.Errorf("saga %q cannot be merged into itself", source))
				return
			}
			if !known[source] {
				writeError(w, 404, fmt.Errorf("source saga %q is not registered in project %q", source, project))
				return
			}
			sources = append(sources, source)
		}
		emitSagaLifecycleEvent(r.Context(), events, "saga.merged", sagaID, project, "", requestUserID(r), map[string]any{
			"saga_id": sagaID, "source_saga_ids": sources, "origin": "human-ui", "actor_id": requestUserID(r),
		})
		writeJSON(w, 200, map[string]any{"saga_id": sagaID, "merged_sources": sources, "status": "merged"})
	})

	// Delete a saga (docs/sagas.md §8): operator hygiene for a mistyped
	// registration or a wrongly merged effort. Event-sourced deletion: one
	// saga.deleted event — every projection (cost rollup, saga_list) forgets
	// the saga, its bound tasks fall back to the unbound pool, and a fresh
	// saga.registered with the same id later resurrects it cleanly. The
	// underlying runs/tasks/journal events are untouched: this retracts the
	// grouping, not the work.
	mux.HandleFunc("POST /v1/workspace/sagas/{sagaID}/delete", func(w http.ResponseWriter, r *http.Request) {
		project, ok := requireProject(w, r)
		if !ok {
			return
		}
		sagaID := r.PathValue("sagaID")
		known, err := sagaRegisteredIDs(r.Context(), journalURL, os.Getenv("TEMPORALITY_API_TOKEN"), project)
		if err != nil {
			writeError(w, 502, fmt.Errorf("journal query failed: %w", err))
			return
		}
		if !known[sagaID] {
			writeError(w, 404, fmt.Errorf("saga %q is not registered in project %q", sagaID, project))
			return
		}
		emitSagaLifecycleEvent(r.Context(), events, "saga.deleted", sagaID, project, "", requestUserID(r), map[string]any{
			"saga_id": sagaID, "origin": "human-ui", "actor_id": requestUserID(r),
		})
		writeJSON(w, 200, map[string]any{"saga_id": sagaID, "status": "deleted"})
	})

	// Bind a task to a saga, re-bind it to another one, or detach (empty
	// saga_id). The latest binding wins in every projection.
	mux.HandleFunc("POST /v1/workspace/tasks/{taskID}/saga", func(w http.ResponseWriter, r *http.Request) {
		project, ok := requireProject(w, r)
		if !ok {
			return
		}
		var req taskSagaBindRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		taskID := r.PathValue("taskID")
		sagaID := strings.TrimSpace(req.SagaID)
		if sagaID == "" {
			emitSagaLifecycleEvent(r.Context(), events, "saga.detached", taskID, project, taskID, requestUserID(r), map[string]any{
				"task_id": taskID, "origin": "human-ui", "actor_id": requestUserID(r),
			})
			writeJSON(w, 200, map[string]any{"task_id": taskID, "status": "detached"})
			return
		}
		known, err := sagaRegisteredIDs(r.Context(), journalURL, os.Getenv("TEMPORALITY_API_TOKEN"), project)
		if err != nil {
			writeError(w, 502, fmt.Errorf("journal query failed: %w", err))
			return
		}
		if !known[sagaID] {
			writeError(w, 404, fmt.Errorf("saga %q is not registered in project %q", sagaID, project))
			return
		}
		emitSagaLifecycleEvent(r.Context(), events, "saga.attached", sagaID, project, taskID, requestUserID(r), map[string]any{
			"saga_id": sagaID, "task_id": taskID, "origin": "human-ui", "actor_id": requestUserID(r),
		})
		writeJSON(w, 200, map[string]any{"task_id": taskID, "saga_id": sagaID, "status": "attached"})
	})
}
