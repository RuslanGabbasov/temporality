package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
)

// File attribution routes (docs/sandbox-storage.md §7.2): the history panel
// reads file.changed events shaped by (area, path), the run view groups the
// run's events per path — both served through the kernel's journal client.

// historyJournal answers the events listing with the scripted payload; the
// kernel Activities walks it exactly like production.
func historyJournal(t *testing.T, events []observation.Event) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "next_cursor": ""})
	}))
	t.Cleanup(server.Close)
	return server
}

func historyEvent(t *testing.T, id, run, area, path, op string, nbytes int, at time.Time) observation.Event {
	t.Helper()
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    id,
		OccurredAt: at,
		Source:     observation.Source{ID: "kernel", Integration: "temporality-agent-kernel", Version: "0.1"},
		Context:    observation.Context{Project: "alpha", Run: run},
		Type:       "file.changed",
		Data:       map[string]any{"area": area, "path": path, "op": op, "bytes": nbytes},
	}
	require.NoError(t, event.Validate())
	return event
}

func newStorageHistoryTestServer(t *testing.T, journal *httptest.Server, visible map[string][]string) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*")
	require.NoError(t, err)
	activities := &agent.Activities{HTTP: journal.Client(), TemporalityURL: journal.URL}
	mux := http.NewServeMux()
	registerStorageHistory(mux, gate, func(*http.Request) (map[string][]string, error) {
		return visible, nil
	}, activities)
	return httptest.NewServer(gate.Authenticate(mux))
}

// historyGet performs an authenticated reader GET — the routes are gated like
// every other storage read.
func historyGet(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer readertoken")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	return response
}

// The same area-relative path exists in both areas; the history of one of
// them must not leak the other's events, and the shaped row carries the run,
// the op and the size the card renders.
func TestStorageFileHistoryShapesAndFiltersByArea(t *testing.T) {
	base := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	journal := historyJournal(t, []observation.Event{
		historyEvent(t, "e1", "run-a", "workspace", "notes.md", "write", 40, base),
		historyEvent(t, "e2", "run-b", "cache", "notes.md", "upload", 99, base.Add(time.Minute)),
		historyEvent(t, "e3", "run-c", "workspace", "other.md", "edit", 7, base.Add(2*time.Minute)), // foreign path
	})
	server := newStorageHistoryTestServer(t, journal, map[string][]string{"alpha": nil})
	defer server.Close()

	response := historyGet(t, server, "/v1/workspace/storage/projects/alpha/file/history?area=workspace&path=notes.md")
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var payload struct {
		Changes []struct {
			OccurredAt string `json:"occurred_at"`
			Run        string `json:"run"`
			Op         string `json:"op"`
			Bytes      int    `json:"bytes"`
		} `json:"changes"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
	require.Len(t, payload.Changes, 1, "cache and foreign-path events must not leak into the workspace history")
	require.Equal(t, "run-a", payload.Changes[0].Run)
	require.Equal(t, "write", payload.Changes[0].Op)
	require.Equal(t, 40, payload.Changes[0].Bytes)
	require.Equal(t, base.Format(time.RFC3339), payload.Changes[0].OccurredAt)
}

// The run view aggregates per path: two touches of one file count as one row
// with changes=2, newest touch leads the listing.
func TestStorageRunFilesGroupsByPath(t *testing.T) {
	base := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	journal := historyJournal(t, []observation.Event{
		historyEvent(t, "e1", "run-7", "workspace", "b.txt", "write", 10, base.Add(2*time.Minute)),
		historyEvent(t, "e2", "run-7", "workspace", "a.txt", "write", 20, base.Add(time.Minute)),
		historyEvent(t, "e3", "run-7", "workspace", "a.txt", "edit", 25, base),
	})
	server := newStorageHistoryTestServer(t, journal, map[string][]string{"alpha": nil})
	defer server.Close()

	response := historyGet(t, server, "/v1/workspace/storage/projects/alpha/file/run-files?run=run-7")
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var payload struct {
		Files []struct {
			Path    string `json:"path"`
			Area    string `json:"area"`
			Changes int    `json:"changes"`
			LastOp  string `json:"last_op"`
		} `json:"files"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
	require.Len(t, payload.Files, 2)
	require.Equal(t, "b.txt", payload.Files[0].Path, "newest touch leads")
	require.Equal(t, "write", payload.Files[0].LastOp)
	require.Equal(t, "a.txt", payload.Files[1].Path)
	require.Equal(t, 2, payload.Files[1].Changes)
}

// Validation mirrors the neighbouring storage routes: a missing path or run
// is 422, an invisible project is 404 — never a silent empty page.
func TestStorageHistoryValidation(t *testing.T) {
	journal := historyJournal(t, nil)
	server := newStorageHistoryTestServer(t, journal, map[string][]string{"alpha": nil})
	defer server.Close()

	cases := []struct {
		url  string
		want int
	}{
		{"/v1/workspace/storage/projects/alpha/file/history?area=workspace", http.StatusUnprocessableEntity}, // no path
		{"/v1/workspace/storage/projects/alpha/file/history?path=notes.md", http.StatusBadRequest},           // no/invalid area
		{"/v1/workspace/storage/projects/alpha/file/run-files", http.StatusUnprocessableEntity},              // no run
		{"/v1/workspace/storage/projects/hidden/file/history?area=workspace&path=a.md", http.StatusNotFound}, // not visible
	}
	for _, c := range cases {
		response := historyGet(t, server, c.url)
		require.Equal(t, c.want, response.StatusCode, c.url)
		response.Body.Close()
	}
}
