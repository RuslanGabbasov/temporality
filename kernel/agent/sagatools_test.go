package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

type recordingOutbox struct{ events []observation.Event }

func (o *recordingOutbox) Enqueue(_ context.Context, ev observation.Event) error {
	o.events = append(o.events, ev)
	return nil
}

// sagaJournalStub serves /v1/observations/events with the recorded saga events;
// query parsing mirrors the real endpoint loosely (project/task/type filters).
type sagaJournalStub struct {
	events []observation.Event
}

func (s *sagaJournalStub) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/observations/events" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	kept := make([]observation.Event, 0, len(s.events))
	for _, ev := range s.events {
		if q.Get("type") != "" && ev.Type != q.Get("type") {
			continue
		}
		if q.Get("task") != "" && ev.Context.Task != q.Get("task") {
			continue
		}
		kept = append(kept, ev)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"events": kept, "count": len(kept)})
}

func sagaTestActivities(server *httptest.Server, outbox *recordingOutbox) *Activities {
	return &Activities{HTTP: server.Client(), TemporalityURL: server.URL, APIToken: "internal", SourceID: "kernel-test", Events: outbox}
}

func TestSagaRegisterEmitsBinding(t *testing.T) {
	journal := &sagaJournalStub{}
	server := httptest.NewServer(http.HandlerFunc(journal.handler))
	defer server.Close()
	outbox := &recordingOutbox{}
	activities := sagaTestActivities(server, outbox)

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-1", TaskID: "T1", Name: "saga_register",
		Arguments: map[string]any{"title": "API Migration", "goal": "client regenerated"},
	})
	if err != nil {
		t.Fatalf("saga_register: %v", err)
	}
	var payload struct {
		SagaID string `json:"saga_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("bad payload %q: %v", result.Content, err)
	}
	if payload.Status != "registered" || !strings.HasPrefix(payload.SagaID, "saga-api-migration-") {
		t.Fatalf("unexpected payload: %s", result.Content)
	}
	if len(outbox.events) != 1 {
		t.Fatalf("expected 1 emitted event, got %d", len(outbox.events))
	}
	ev := outbox.events[0]
	if ev.Type != "saga.registered" || ev.Context.Task != "T1" || ev.Context.Project != "demo" || ev.Data["saga_id"] != payload.SagaID {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if ev.Data["title"] != "API Migration" || ev.Data["goal"] != "client regenerated" {
		t.Fatalf("event must carry title and goal: %+v", ev.Data)
	}
	// Deterministic id: the same task always maps to the same saga id.
	again, _ := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-2", TaskID: "T1", Name: "saga_register",
		Arguments: map[string]any{"title": "API Migration"},
	})
	// The stub journal never learns about the emission, but ids must match.
	if !strings.Contains(again.Content, payload.SagaID) {
		t.Fatalf("saga id must be deterministic per task: %s vs %s", again.Content, payload.SagaID)
	}
}

func TestSagaRegisterIdempotentForBoundTask(t *testing.T) {
	base := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	journal := &sagaJournalStub{events: []observation.Event{{
		Type: "saga.registered", OccurredAt: base,
		Context: observation.Context{Project: "demo", Run: "run-0", Task: "T1"},
		Data:    map[string]any{"saga_id": "saga-existing-aa0000", "title": "Existing"},
	}}}
	server := httptest.NewServer(http.HandlerFunc(journal.handler))
	defer server.Close()
	outbox := &recordingOutbox{}
	activities := sagaTestActivities(server, outbox)

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-1", TaskID: "T1", Name: "saga_register",
		Arguments: map[string]any{"title": "Another try"},
	})
	if err != nil {
		t.Fatalf("saga_register: %v", err)
	}
	if !strings.Contains(result.Content, "saga-existing-aa0000") || !strings.Contains(result.Content, "existing") {
		t.Fatalf("expected the existing saga back, got %s", result.Content)
	}
	if len(outbox.events) != 0 {
		t.Fatalf("no new event may be emitted for a bound task, got %d", len(outbox.events))
	}
}

func TestSagaAttachValidatesAndEmits(t *testing.T) {
	base := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	journal := &sagaJournalStub{events: []observation.Event{
		{Type: "saga.registered", OccurredAt: base, Context: observation.Context{Project: "demo", Run: "run-0", Task: "T9"},
			Data: map[string]any{"saga_id": "saga-live-001122", "title": "Live saga"}},
	}}
	server := httptest.NewServer(http.HandlerFunc(journal.handler))
	defer server.Close()
	outbox := &recordingOutbox{}
	activities := sagaTestActivities(server, outbox)

	// Unknown saga is rejected instead of silently creating a phantom binding.
	result, _ := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-1", TaskID: "T1", Name: "saga_attach",
		Arguments: map[string]any{"saga_id": "saga-missing-ff0000"},
	})
	if !strings.Contains(result.Content, "error:") || !strings.Contains(result.Content, "saga_list") {
		t.Fatalf("unknown saga must fail with guidance, got %q", result.Content)
	}

	// Runs without a task have nothing to bind.
	result, _ = activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-1", Name: "saga_attach",
		Arguments: map[string]any{"saga_id": "saga-live-001122"},
	})
	if !strings.Contains(result.Content, "error:") {
		t.Fatalf("taskless attach must fail, got %q", result.Content)
	}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-1", TaskID: "T1", Name: "saga_attach",
		Arguments: map[string]any{"saga_id": "saga-live-001122", "note": "takeover"},
	})
	if err != nil {
		t.Fatalf("saga_attach: %v", err)
	}
	if !strings.Contains(result.Content, "attached") {
		t.Fatalf("expected attached status, got %s", result.Content)
	}
	if len(outbox.events) != 1 || outbox.events[0].Type != "saga.attached" {
		t.Fatalf("expected one saga.attached event, got %+v", outbox.events)
	}
	ev := outbox.events[0]
	if ev.Context.Task != "T1" || ev.Data["saga_id"] != "saga-live-001122" || ev.Data["note"] != "takeover" {
		t.Fatalf("unexpected attach event: %+v", ev)
	}

	// Re-attach to the same saga is idempotent — no second event.
	journal.events = append(journal.events, ev)
	result, _ = activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "run-2", TaskID: "T1", Name: "saga_attach",
		Arguments: map[string]any{"saga_id": "saga-live-001122"},
	})
	if !strings.Contains(result.Content, "already_attached") {
		t.Fatalf("expected already_attached, got %s", result.Content)
	}
	if len(outbox.events) != 1 {
		t.Fatalf("re-attach must not emit, got %d events", len(outbox.events))
	}
}

func TestSagaListProjectsLatestBinding(t *testing.T) {
	base := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	journal := &sagaJournalStub{events: []observation.Event{
		{Type: "saga.registered", OccurredAt: base, Context: observation.Context{Project: "demo", Run: "r0", Task: "T1"},
			Data: map[string]any{"saga_id": "saga-one-aaaaaa", "title": "One", "goal": "g1"}},
		// Attached events are queried in a second request and arrive after the
		// registered batch — the merged order must not break last-wins.
		{Type: "saga.attached", OccurredAt: base.Add(10 * time.Minute), Context: observation.Context{Project: "demo", Run: "r2", Task: "T2"},
			Data: map[string]any{"saga_id": "saga-one-aaaaaa"}},
		{Type: "saga.registered", OccurredAt: base.Add(20 * time.Minute), Context: observation.Context{Project: "demo", Run: "r3", Task: "T3"},
			Data: map[string]any{"saga_id": "saga-two-bbbbbb", "title": "Two"}},
		// Re-bind T2 away from One: latest wins.
		{Type: "saga.attached", OccurredAt: base.Add(30 * time.Minute), Context: observation.Context{Project: "demo", Run: "r2", Task: "T2"},
			Data: map[string]any{"saga_id": "saga-two-bbbbbb"}},
	}}
	server := httptest.NewServer(http.HandlerFunc(journal.handler))
	defer server.Close()
	outbox := &recordingOutbox{}
	activities := sagaTestActivities(server, outbox)

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", RunID: "r9", Name: "saga_list", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("saga_list: %v", err)
	}
	var payload struct {
		Sagas []struct {
			SagaID string   `json:"saga_id"`
			Title  string   `json:"title"`
			Goal   string   `json:"goal"`
			Tasks  []string `json:"tasks"`
		} `json:"sagas"`
	}
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("bad payload %q: %v", result.Content, err)
	}
	byID := map[string]int{}
	for i, saga := range payload.Sagas {
		byID[saga.SagaID] = i
	}
	if len(payload.Sagas) != 2 {
		t.Fatalf("expected 2 sagas, got %d: %s", len(payload.Sagas), result.Content)
	}
	one := payload.Sagas[byID["saga-one-aaaaaa"]]
	if one.Title != "One" || one.Goal != "g1" || len(one.Tasks) != 1 || one.Tasks[0] != "T1" {
		t.Fatalf("saga one must own only T1: %+v", one)
	}
	two := payload.Sagas[byID["saga-two-bbbbbb"]]
	if len(two.Tasks) != 2 {
		t.Fatalf("saga two must own T3 and the re-bound T2: %+v", two)
	}
}

func TestSagaToolsValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc((&sagaJournalStub{}).handler))
	defer server.Close()
	activities := sagaTestActivities(server, &recordingOutbox{})

	cases := []struct {
		name      string
		request   ToolRequest
		wantError string
	}{
		{"register without title", ToolRequest{Project: "demo", RunID: "r", TaskID: "T", Name: "saga_register", Arguments: map[string]any{}}, "title is required"},
		{"register without project", ToolRequest{RunID: "r", TaskID: "T", Name: "saga_register", Arguments: map[string]any{"title": "x"}}, "project not set"},
		{"register without task", ToolRequest{Project: "demo", RunID: "r", Name: "saga_register", Arguments: map[string]any{"title": "x"}}, "no task"},
		{"attach without saga_id", ToolRequest{Project: "demo", RunID: "r", TaskID: "T", Name: "saga_attach", Arguments: map[string]any{}}, "saga_id is required"},
		{"list without project", ToolRequest{RunID: "r", Name: "saga_list", Arguments: map[string]any{}}, "project not set"},
	}
	for _, tc := range cases {
		result, err := activities.RunTool(context.Background(), tc.request)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !strings.Contains(result.Content, "error:") || !strings.Contains(result.Content, tc.wantError) {
			t.Fatalf("%s: want %q in %q", tc.name, tc.wantError, result.Content)
		}
	}
}
