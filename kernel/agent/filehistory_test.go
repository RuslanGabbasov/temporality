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

// fileChangeLocation speaks the storage API's (area, path) vocabulary: the
// attribution events must land on the same coordinates the browser reads,
// or the per-file history would never match the listed paths.
func TestFileChangeLocationNormalizesAreas(t *testing.T) {
	root := "/srv/sbx"
	activities := &Activities{Sandbox: &fileToolSandbox{root: root}}
	cases := []struct {
		abs  string
		area string
		path string
		ok   bool
	}{
		{root + "/projects/demo/src/main.go", "workspace", "src/main.go", true},
		{root + "/cache/demo/go-build/x.o", "cache", "go-build/x.o", true},
		{root + "/scratch/demo/run-1/prep.txt", "scratch", "run-1/prep.txt", true},
		{root + "/projects/demo", "", "", false}, // the area root itself
		{"/etc/passwd", "", "", false},           // outside the sandbox root
	}
	for _, c := range cases {
		area, path, ok := activities.fileChangeLocation(c.abs)
		if ok != c.ok || area != c.area || path != c.path {
			t.Fatalf("fileChangeLocation(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.abs, area, path, ok, c.area, c.path, c.ok)
		}
	}
}

// A successful write journals file.changed with the run it belongs to and
// the payload shape the history endpoint reads; an unconfigured journal is a
// silent no-op (the write must never fail over telemetry).
func TestJournalFileChangeRecordsRunAttribution(t *testing.T) {
	root := t.TempDir()
	outbox := &capturedOutbox{}
	activities := &Activities{Events: outbox, Sandbox: &fileToolSandbox{root: root}, SourceID: "kernel"}
	request := ToolRequest{RunID: "run-hist", OperationID: "frame/call-1", Project: "demo"}

	activities.journalFileChange(request, "write", root+"/projects/demo/notes.md", 42)
	if len(outbox.events) != 1 {
		t.Fatalf("journaled events = %d, want 1", len(outbox.events))
	}
	event := outbox.events[0]
	if event.Type != "file.changed" || event.Context.Run != "run-hist" || event.Context.Project != "demo" {
		t.Fatalf("event shape: type=%q run=%q project=%q", event.Type, event.Context.Run, event.Context.Project)
	}
	if event.Data["area"] != "workspace" || event.Data["path"] != "notes.md" || event.Data["op"] != "write" {
		t.Fatalf("payload: %v", event.Data)
	}
	if event.Data["bytes"] != 42 || event.Data["operation"] != "frame/call-1" {
		t.Fatalf("payload bytes/operation: %v", event.Data)
	}

	// No outbox configured: attribution is best-effort, never fatal.
	silent := &Activities{Sandbox: &fileToolSandbox{root: root}, SourceID: "kernel"}
	silent.journalFileChange(request, "write", root+"/projects/demo/notes.md", 42)
}

// fakeJournal serves the events listing the history walks: it records the
// query it received (so tests can pin the path filter) and answers with the
// scripted events regardless of the filter — exactly like a journal that
// predates the path parameter, which FileChangeHistory must survive.
func fakeJournal(t *testing.T, events []observation.Event) (*httptest.Server, *string) {
	t.Helper()
	query := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "next_cursor": ""})
	}))
	t.Cleanup(server.Close)
	return server, &query
}

func fileChangedEvent(t *testing.T, id, project, run, path, op string, at time.Time) observation.Event {
	t.Helper()
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    id,
		OccurredAt: at,
		Source:     observation.Source{ID: "kernel", Integration: "temporality-agent-kernel", Version: "0.1"},
		Context:    observation.Context{Project: project, Run: run},
		Type:       "file.changed",
		Data:       map[string]any{"area": "workspace", "path": path, "op": op, "bytes": 7},
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("fixture event invalid: %v", err)
	}
	return event
}

// The history walk asks the journal for (project, type, path) and re-checks
// the payload path itself: a peer journal that ignores the path filter must
// not leak other files' events into one file's history.
func TestFileChangeHistoryFiltersByPathClientSide(t *testing.T) {
	now := time.Now()
	events := []observation.Event{
		fileChangedEvent(t, "e1", "demo", "run-a", "notes.md", "write", now),
		fileChangedEvent(t, "e2", "demo", "run-b", "other.md", "edit", now.Add(-time.Minute)), // foreign path
		fileChangedEvent(t, "e3", "demo", "run-c", "notes.md", "edit", now.Add(-2*time.Minute)),
	}
	server, query := fakeJournal(t, events)
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL}

	got, err := activities.FileChangeHistory(context.Background(), "demo", "notes.md", 10)
	if err != nil {
		t.Fatalf("FileChangeHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("history events = %d, want 2 (foreign path filtered out)", len(got))
	}
	for _, event := range got {
		if event.Data["path"] != "notes.md" {
			t.Fatalf("foreign path leaked: %v", event.Data)
		}
	}
	if !strings.Contains(*query, "type=file.changed") || !strings.Contains(*query, "path=notes.md") || !strings.Contains(*query, "project=demo") {
		t.Fatalf("journal query misses the filters: %s", *query)
	}
}

// The run listing pages the journal with (project, run, type) so the
// «files of this run» view is one bounded request instead of a scan.
func TestRunFileChangesQueriesByRun(t *testing.T) {
	server, query := fakeJournal(t, []observation.Event{
		fileChangedEvent(t, "e1", "demo", "run-7", "a.txt", "write", time.Now()),
	})
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL}

	got, err := activities.RunFileChanges(context.Background(), "demo", "run-7")
	if err != nil {
		t.Fatalf("RunFileChanges: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("run events = %d, want 1", len(got))
	}
	for _, want := range []string{"run=run-7", "type=file.changed", "project=demo"} {
		if !strings.Contains(*query, want) {
			t.Fatalf("journal query misses %q: %s", want, *query)
		}
	}
}
