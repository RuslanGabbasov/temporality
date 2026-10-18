package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
)

// fakeSagaJournal answers the saga.registered journal queries the routes use
// for existence validation, the way the real observation API would.
type fakeSagaJournal struct {
	mu    sync.Mutex
	url   string
	pages map[string][]observation.Event // project → registered events
}

func (f *fakeSagaJournal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/v1/observations/events" {
		http.NotFound(w, r)
		return
	}
	events := f.pages[r.URL.Query().Get("project")]
	if r.URL.Query().Get("type") != "saga.registered" {
		events = nil
	}
	var matched []observation.Event
	for _, ev := range events {
		if id, _ := ev.Data["saga_id"].(string); id != "" {
			matched = append(matched, ev)
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"events": matched, "next_cursor": ""})
}

func (f *fakeSagaJournal) register(project, sagaID, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pages == nil {
		f.pages = map[string][]observation.Event{}
	}
	f.pages[project] = append(f.pages[project], observation.Event{
		Type: "saga.registered",
		Data: map[string]any{"saga_id": sagaID, "title": title},
	})
}

// startFakeSagaJournal mounts the fake journal on a real HTTP server so the
// routes can query it exactly like the observation API.
func startFakeSagaJournal(t *testing.T) *fakeSagaJournal {
	t.Helper()
	journal := &fakeSagaJournal{}
	server := httptest.NewServer(journal)
	t.Cleanup(server.Close)
	journal.url = server.URL
	return journal
}

// newSagaLifecycleServer mounts the saga router the way main wires it: real
// RBAC gate, capture outbox and a fake journal behind its real HTTP shape.
func newSagaLifecycleServer(t *testing.T, outbox *captureArtifactOutbox, journal *fakeSagaJournal) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*\nadmintoken:admin:admin:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterSagaRoutes(mux, gate, outbox, journal.url)
	return httptest.NewServer(gate.Authenticate(mux))
}

func sagaPost(t *testing.T, server *httptest.Server, token, path, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(raw)
}

// Creating a saga by hand emits a saga.registered event with the human origin,
// the project in context (so journal queries by project find it) and the
// deterministic saga id.
func TestSagaCreateEmitsRegistered(t *testing.T) {
	outbox := &captureArtifactOutbox{}
	server := newSagaLifecycleServer(t, outbox, startFakeSagaJournal(t))
	defer server.Close()

	status, body := sagaPost(t, server, "writertoken", "/v1/workspace/sagas?project=p",
		`{"title":"Смета на разработку","goal":"согласовать с Ивановым","task_id":"T42"}`)
	require.Equal(t, 201, status)
	require.Contains(t, body, `"task_id":"T42"`)
	registered := outbox.byType("saga.registered")
	require.Len(t, registered, 1)
	require.Equal(t, "human-ui", registered[0].Data["origin"])
	require.Equal(t, "p", registered[0].Context.Project)
	require.Equal(t, "T42", registered[0].Context.Task)
	require.True(t, strings.HasPrefix(registered[0].Data["saga_id"].(string), "saga-"))
	require.Equal(t, "human", registered[0].Context.Actor.Type)
}

// Rename validates against the journal: an unknown saga is a 404, a known one
// gets a saga.renamed event carrying the new title.
func TestSagaRenameValidatesAndEmits(t *testing.T) {
	journal := startFakeSagaJournal(t)
	journal.register("p", "saga-real-bbbbbb", "Real saga")
	outbox := &captureArtifactOutbox{}
	server := newSagaLifecycleServer(t, outbox, journal)
	defer server.Close()

	status, body := sagaPost(t, server, "writertoken", "/v1/workspace/sagas/saga-real-bbbbbb/rename?project=p",
		`{"title":"Renamed"}`)
	require.Equal(t, 200, status, body)
	renamed := outbox.byType("saga.renamed")
	require.Len(t, renamed, 1)
	require.Equal(t, "Renamed", renamed[0].Data["title"])
	require.Equal(t, "p", renamed[0].Context.Project)

	status, _ = sagaPost(t, server, "writertoken", "/v1/workspace/sagas/saga-missing/rename?project=p", `{"title":"X"}`)
	require.Equal(t, 404, status)

	status, _ = sagaPost(t, server, "writertoken", "/v1/workspace/sagas/saga-real-bbbbbb/rename?project=p", `{}`)
	require.Equal(t, 422, status)
}

// Merge refuses unknown sources and self-merges; a valid one emits the
// source list the projections replay.
func TestSagaMergeValidatesAndEmits(t *testing.T) {
	journal := startFakeSagaJournal(t)
	journal.register("p", "saga-real-bbbbbb", "Real")
	journal.register("p", "saga-dup-aaaaaa", "Duplicate")
	journal.register("p", "saga-dup2-cccccc", "Duplicate 2")
	outbox := &captureArtifactOutbox{}
	server := newSagaLifecycleServer(t, outbox, journal)
	defer server.Close()

	status, body := sagaPost(t, server, "writertoken", "/v1/workspace/sagas/saga-real-bbbbbb/merge?project=p",
		`{"source_saga_ids":["saga-dup-aaaaaa","saga-dup2-cccccc"]}`)
	require.Equal(t, 200, status, body)
	merged := outbox.byType("saga.merged")
	require.Len(t, merged, 1)
	require.Equal(t, "saga-real-bbbbbb", merged[0].Data["saga_id"])
	require.Equal(t, []string{"saga-dup-aaaaaa", "saga-dup2-cccccc"}, merged[0].Data["source_saga_ids"])

	status, _ = sagaPost(t, server, "writertoken", "/v1/workspace/sagas/saga-real-bbbbbb/merge?project=p", `{}`)
	require.Equal(t, 422, status) // valid body without source_saga_ids

	status, _ = sagaPost(t, server, "writertoken", "/v1/workspace/sagas/saga-real-bbbbbb/merge?project=p",
		`{"source_saga_ids":["saga-unknown"]}`)
	require.Equal(t, 404, status)

	status, _ = sagaPost(t, server, "writertoken", "/v1/workspace/sagas/saga-real-bbbbbb/merge?project=p",
		`{"source_saga_ids":["saga-real-bbbbbb"]}`)
	require.Equal(t, 422, status)
}

// Binding a task emits saga.attached; an empty saga_id emits saga.detached —
// the two halves of the "re-bind or unbind" UI action.
func TestTaskSagaBindAndDetach(t *testing.T) {
	journal := startFakeSagaJournal(t)
	journal.register("p", "saga-real-bbbbbb", "Real")
	outbox := &captureArtifactOutbox{}
	server := newSagaLifecycleServer(t, outbox, journal)
	defer server.Close()

	status, body := sagaPost(t, server, "writertoken", "/v1/workspace/tasks/T9/saga?project=p",
		`{"saga_id":"saga-real-bbbbbb"}`)
	require.Equal(t, 200, status, body)
	attached := outbox.byType("saga.attached")
	require.Len(t, attached, 1)
	require.Equal(t, "T9", attached[0].Context.Task)
	require.Equal(t, "T9", attached[0].Data["task_id"])
	require.Equal(t, "human-ui", attached[0].Data["origin"])

	status, _ = sagaPost(t, server, "writertoken", "/v1/workspace/tasks/T9/saga?project=p", `{"saga_id":""}`)
	require.Equal(t, 200, status)
	detached := outbox.byType("saga.detached")
	require.Len(t, detached, 1)
	require.Equal(t, "T9", detached[0].Data["task_id"])

	status, _ = sagaPost(t, server, "writertoken", "/v1/workspace/tasks/T9/saga?project=p", `{"saga_id":"saga-nope"}`)
	require.Equal(t, 404, status)
}

// Reader tokens cannot rewrite accounting: every route is writer-only.
func TestSagaRoutesReaderForbidden(t *testing.T) {
	outbox := &captureArtifactOutbox{}
	server := newSagaLifecycleServer(t, outbox, startFakeSagaJournal(t))
	defer server.Close()

	status, _ := sagaPost(t, server, "readertoken", "/v1/workspace/sagas?project=p", `{"title":"X"}`)
	require.Equal(t, 403, status)
	status, _ = sagaPost(t, server, "readertoken", "/v1/workspace/sagas/saga-x/rename?project=p", `{"title":"X"}`)
	require.Equal(t, 403, status)
	status, _ = sagaPost(t, server, "readertoken", "/v1/workspace/sagas/saga-x/merge?project=p", `{"source_saga_ids":["y"]}`)
	require.Equal(t, 403, status)
	status, _ = sagaPost(t, server, "readertoken", "/v1/workspace/tasks/T/saga?project=p", `{"saga_id":"x"}`)
	require.Equal(t, 403, status)
	require.Zero(t, len(outbox.events))
}

// The project is not optional: without it nothing is emitted and the caller
// gets a 400, not a cross-project mutation.
func TestSagaRoutesRequireProject(t *testing.T) {
	outbox := &captureArtifactOutbox{}
	server := newSagaLifecycleServer(t, outbox, startFakeSagaJournal(t))
	defer server.Close()

	status, _ := sagaPost(t, server, "writertoken", "/v1/workspace/sagas", `{"title":"X"}`)
	require.Equal(t, 400, status)
	require.Zero(t, len(outbox.events))
}
