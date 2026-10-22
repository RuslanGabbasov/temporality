package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// fakeRunListStore answers the three journal queries of GET /v1/agent/runs
// and records how the route called them, so the contract (page size, cursor
// decoding, visibility scoping, partial degradation) is asserted without a
// database.
type fakeRunListStore struct {
	mu sync.Mutex

	page      []workspace.RunListEntry
	next      string
	pageErr   error
	statuses  map[string]string
	statusErr error
	names     map[string]string
	nameErr   error

	listCalls   []listCall
	statusCalls int
}

type listCall struct {
	project  string
	projects []string
	limit    int
	after    *workspace.RunListCursor
}

func (f *fakeRunListStore) ListRunsPage(_ context.Context, project string, projects []string, limit int, after *workspace.RunListCursor) ([]workspace.RunListEntry, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, listCall{project: project, projects: projects, limit: limit, after: after})
	if f.pageErr != nil {
		return nil, "", f.pageErr
	}
	return f.page, f.next, nil
}

func (f *fakeRunListStore) RunTerminalStatuses(_ context.Context, runIDs []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	out := make(map[string]string, len(runIDs))
	for _, id := range runIDs {
		if status, ok := f.statuses[id]; ok {
			out[id] = status
		}
	}
	return out, nil
}

func (f *fakeRunListStore) AgentDisplayNames(_ context.Context, agentIDs []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nameErr != nil {
		return nil, f.nameErr
	}
	out := make(map[string]string, len(agentIDs))
	for _, id := range agentIDs {
		if name, ok := f.names[id]; ok {
			out[id] = name
		}
	}
	return out, nil
}

// runListTestServer mounts the route the way main wires it: real RBAC gate,
// fake journal store behind the interface.
func runListTestServer(t *testing.T, store *fakeRunListStore) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate(
		"admintoken:admin:admin:*\n" +
			"readertoken:reader:reader:*\n" +
			"projecttoken:scoped:reader:proj-a")
	require.NoError(t, err)
	mux := http.NewServeMux()
	registerRunListRoute(mux, runListDeps{
		store: store,
		gate:  gate,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	server := httptest.NewServer(gate.Authenticate(mux))
	t.Cleanup(server.Close)
	return server
}

func runListGet(t *testing.T, server *httptest.Server, token, query string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/agent/runs"+query, nil)
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body), "response must be JSON: %s", string(raw))
	return response.StatusCode, body
}

func runListEntries(ids ...string) []workspace.RunListEntry {
	started := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	entries := make([]workspace.RunListEntry, 0, len(ids))
	for i, id := range ids {
		entries = append(entries, workspace.RunListEntry{
			RunID:       id,
			Project:     "proj-a",
			Title:       "run " + id,
			Role:        "agent",
			ParentRunID: "",
			StartedAt:   started.Add(time.Duration(i) * time.Minute),
			SourceID:    "kernel",
			EventID:     "e-" + id,
			Workflow:    "",
			AgentID:     "agent-1",
		})
	}
	return entries
}

// TestRunListPageShape covers the success path: default page size 100,
// first page (no cursor), statuses resolved per row (missing → running),
// agent name resolution, next-cursor passthrough and the exact JSON field
// set the debugger client depends on.
func TestRunListPageShape(t *testing.T) {
	store := &fakeRunListStore{
		page:     runListEntries("run-1", "run-2"),
		next:     "cursor-page-2",
		statuses: map[string]string{"run-1": "completed", "run-2": "failed"},
		names:    map[string]string{"agent-1": "Coder"},
	}
	server := runListTestServer(t, store)

	code, body := runListGet(t, server, "admintoken", "")
	require.Equal(t, http.StatusOK, code)

	require.Len(t, store.listCalls, 1)
	call := store.listCalls[0]
	require.Equal(t, 100, call.limit, "default page size must be 100 (spec: 50–100)")
	require.Equal(t, "", call.project)
	require.Nil(t, call.projects, "an all-projects caller gets no visibility constraint")
	require.Nil(t, call.after, "no cursor = first page")

	runs, ok := body["runs"].([]any)
	require.True(t, ok, "runs array present")
	require.Len(t, runs, 2)
	first := runs[0].(map[string]any)
	require.Equal(t, "run-1", first["run_id"])
	require.Equal(t, "proj-a", first["project"])
	require.Equal(t, "run run-1", first["title"])
	require.Equal(t, "agent", first["role"])
	require.Equal(t, "", first["parent_run_id"])
	require.Equal(t, "kernel", first["source_id"])
	require.Equal(t, "completed", first["status"])
	require.Equal(t, "agent-1", first["agent_id"])
	require.Equal(t, "Coder", first["agent_name"])
	require.NotEmpty(t, first["started_at"], "started_at is rendered as a timestamp")
	// run-2 has no agent name? it does — names map covers agent-1; its status:
	require.Equal(t, "failed", runs[1].(map[string]any)["status"])

	// A run without a terminal event stays "running" (the old client scan's
	// default), not "".
	store.mu.Lock()
	store.statuses = map[string]string{}
	store.mu.Unlock()
	code, body = runListGet(t, server, "admintoken", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "running", body["runs"].([]any)[0].(map[string]any)["status"])

	require.Equal(t, "cursor-page-2", body["next_cursor"])
	require.Equal(t, false, body["partial"])
	require.Equal(t, "", body["partial_error"])
	require.Equal(t, float64(2), body["count"])
}

// TestRunListLimitValidation pins the page-size contract: 50–100 accepted,
// anything else is a 400 — never a silent clamp that hides a contract drift.
func TestRunListLimitValidation(t *testing.T) {
	store := &fakeRunListStore{page: runListEntries("run-1")}
	server := runListTestServer(t, store)

	for _, query := range []string{"?limit=20", "?limit=500", "?limit=abc"} {
		code, _ := runListGet(t, server, "admintoken", query)
		require.Equal(t, http.StatusBadRequest, code, query)
	}
	require.Empty(t, store.listCalls, "invalid limit never reaches the store")

	code, _ := runListGet(t, server, "admintoken", "?limit=50")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, 50, store.listCalls[0].limit)
}

// TestRunListCursorRoundTrip pins the opaque cursor contract: a well-formed
// token decodes into the exact position handed to the store, a corrupted one
// is a 400 and never silently restarts the listing.
func TestRunListCursorRoundTrip(t *testing.T) {
	store := &fakeRunListStore{page: runListEntries("run-1")}
	server := runListTestServer(t, store)

	position := workspace.RunListCursor{
		OccurredAt: time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC),
		SourceID:   "kernel",
		EventID:    "e-42",
	}
	code, _ := runListGet(t, server, "admintoken", "?cursor="+workspace.EncodeRunListCursor(position))
	require.Equal(t, http.StatusOK, code)
	after := store.listCalls[0].after
	require.NotNil(t, after)
	require.True(t, after.OccurredAt.Equal(position.OccurredAt))
	require.Equal(t, position.SourceID, after.SourceID)
	require.Equal(t, position.EventID, after.EventID)

	for _, bad := range []string{"!!!!", "aGVsbG8", "e30"} {
		code, _ := runListGet(t, server, "admintoken", "?cursor="+bad)
		require.Equal(t, http.StatusBadRequest, code, "cursor %q must be refused", bad)
	}
	require.Len(t, store.listCalls, 1, "a bad cursor never reaches the store")
}

// TestRunListPartialStatuses is the K-3 contract: when the terminal-status
// lookup fails, the page still answers 200 with every row intact, statuses
// degraded to "unknown", partial:true and the reason surfaced for the UI
// indicator and the server log — no more "everything looks running".
func TestRunListPartialStatuses(t *testing.T) {
	store := &fakeRunListStore{
		page:      runListEntries("run-1", "run-2"),
		statusErr: errors.New("journal status query timed out"),
	}
	server := runListTestServer(t, store)

	code, body := runListGet(t, server, "admintoken", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, body["partial"])
	require.Contains(t, body["partial_error"], "status query timed out")
	runs := body["runs"].([]any)
	require.Len(t, runs, 2, "the list survives a status failure")
	require.Equal(t, "unknown", runs[0].(map[string]any)["status"])
	require.Equal(t, "unknown", runs[1].(map[string]any)["status"])
	require.Positive(t, store.statusCalls)
}

// TestRunListVisibilityScoping pins docs/activity-scoping.md §5: a concrete
// project rides into the query, a scoped principal's global listing is
// constrained to its visible projects, and a principal with no visible
// projects gets an empty page without a store round-trip.
func TestRunListVisibilityScoping(t *testing.T) {
	store := &fakeRunListStore{page: runListEntries("run-1")}
	server := runListTestServer(t, store)

	code, _ := runListGet(t, server, "projecttoken", "?project=proj-b")
	require.Equal(t, http.StatusForbidden, code, "a project outside the token is refused")
	require.Empty(t, store.listCalls)

	code, _ = runListGet(t, server, "projecttoken", "?project=proj-a")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "proj-a", store.listCalls[0].project)

	code, _ = runListGet(t, server, "projecttoken", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"proj-a"}, store.listCalls[1].projects, "global listing constrained to visible projects")
}

// TestRunListScopeEmptyVisibility covers the branch a static token cannot
// express (NewGate refuses project-less tokens): a principal whose visible
// set resolves empty must be answered with an empty page, never an
// unfiltered query.
func TestRunListScopeEmptyVisibility(t *testing.T) {
	scoped := controlplane.Principal{Subject: "nobody", Role: controlplane.RoleReader, Projects: []string{}}
	projects, empty := runListScope(scoped, "")
	require.True(t, empty)
	require.Nil(t, projects)

	// Restricted non-empty: the store must receive the any-of list.
	scoped = controlplane.Principal{Subject: "scoped", Role: controlplane.RoleReader, Projects: []string{"proj-a"}}
	projects, empty = runListScope(scoped, "")
	require.False(t, empty)
	require.Equal(t, []string{"proj-a"}, projects)

	// A concrete project short-circuits visibility (the gate authorized it).
	projects, empty = runListScope(scoped, "proj-a")
	require.False(t, empty)
	require.Nil(t, projects)

	// All-projects principals list unconstrained.
	admin := controlplane.Principal{Subject: "admin", Role: controlplane.RoleAdmin, Projects: []string{"*"}}
	projects, empty = runListScope(admin, "")
	require.False(t, empty)
	require.Nil(t, projects)
}

// TestRunListRequiresAuth: the route is mounted behind the gate like its
// POST sibling — no token, no list.
func TestRunListRequiresAuth(t *testing.T) {
	store := &fakeRunListStore{}
	server := runListTestServer(t, store)
	code, _ := runListGet(t, server, "", "")
	require.Equal(t, http.StatusUnauthorized, code)
	require.Empty(t, store.listCalls)
}
