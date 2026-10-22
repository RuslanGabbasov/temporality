package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
	"go.temporal.io/api/serviceerror"
)

// fakeNudgeStore answers the run's authoritative source resolution, the
// terminal-state lookup of the heal path and records every guarded status
// settle.
type fakeNudgeStore struct {
	mu sync.Mutex

	source    string
	sourceErr error

	// terminal mirrors RunTerminalStatuses: run id → terminal status.
	terminal map[string]string

	sourceCalls []nudgeSourceCall
	settleCalls []nudgeSettleCall
}

type nudgeSourceCall struct {
	project string
	runID   string
}

type nudgeSettleCall struct {
	id string
}

func (f *fakeNudgeStore) RunSourceID(_ context.Context, project, runID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sourceCalls = append(f.sourceCalls, nudgeSourceCall{project: project, runID: runID})
	if f.sourceErr != nil {
		return "", f.sourceErr
	}
	return f.source, nil
}

func (f *fakeNudgeStore) RunTerminalStatuses(_ context.Context, runIDs []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(runIDs))
	for _, id := range runIDs {
		if status, ok := f.terminal[id]; ok && status != "" {
			out[id] = status
		}
	}
	return out, nil
}

func (f *fakeNudgeStore) SettleRunCancelled(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settleCalls = append(f.settleCalls, nudgeSettleCall{id: id})
	return nil
}

// fakeNudgeSignaler returns the configured error and records every signal it
// was asked to deliver.
type fakeNudgeSignaler struct {
	mu    sync.Mutex
	err   error
	calls []nudgeSignalCall
}

type nudgeSignalCall struct {
	workflowID string
	runID      string
	name       string
	turn       int
}

func (f *fakeNudgeSignaler) SignalWorkflow(_ context.Context, workflowID, runID, signalName string, arg any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	nudge, _ := arg.(agent.Nudge)
	f.calls = append(f.calls, nudgeSignalCall{workflowID: workflowID, runID: runID, name: signalName, turn: nudge.Turn})
	return f.err
}

// fakeNudgeJournal records the terminal event the heal path enqueues.
type fakeNudgeJournal struct {
	mu     sync.Mutex
	err    error
	events []observation.Event
}

func (f *fakeNudgeJournal) Enqueue(_ context.Context, event observation.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, event)
	return nil
}

// nudgeTestServer mounts the route the way main wires it: real RBAC gate,
// fakes behind the interfaces.
func nudgeTestServer(t *testing.T, store *fakeNudgeStore, journal *fakeNudgeJournal, signaler *fakeNudgeSignaler) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate(
		"admintoken:admin:admin:*\n" +
			"readertoken:reader:reader:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	registerNudgeRoute(mux, nudgeDeps{
		gate:     gate,
		store:    store,
		journal:  journal,
		signaler: signaler,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	server := httptest.NewServer(gate.Authenticate(mux))
	t.Cleanup(server.Close)
	return server
}

// nudgePost nudges run-1 and returns the status, the decoded JSON body and the
// raw text (the raw text is what asserts nothing verbatim leaks to the client).
func nudgePost(t *testing.T, server *httptest.Server, token, query string) (int, map[string]any, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent/runs/run-1/nudge"+query,
		strings.NewReader(`{"turn":2,"actor_id":"human-ui"}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
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
	return response.StatusCode, body, string(raw)
}

// TestNudgeSignalsResolvedWorkflow is the success path: the workflow identity
// comes from the journal's source (the store), the signal carries the nudge
// and nothing about the run is settled or journaled.
func TestNudgeSignalsResolvedWorkflow(t *testing.T) {
	store := &fakeNudgeStore{source: "journal-source"}
	journal := &fakeNudgeJournal{}
	signaler := &fakeNudgeSignaler{}
	server := nudgeTestServer(t, store, journal, signaler)

	code, body, raw := nudgePost(t, server, "admintoken", "?project=proj-a")
	require.Equal(t, http.StatusAccepted, code, raw)
	require.Equal(t, true, body["accepted"])

	require.Equal(t, []nudgeSourceCall{{project: "proj-a", runID: "run-1"}}, store.sourceCalls)
	require.Len(t, signaler.calls, 1)
	require.Equal(t, agent.WorkflowID("journal-source", "proj-a", "run-1"), signaler.calls[0].workflowID)
	require.Equal(t, agent.NudgeSignal, signaler.calls[0].name)
	require.Equal(t, 2, signaler.calls[0].turn)

	require.Empty(t, journal.events, "a delivered nudge journals nothing")
	require.Empty(t, store.settleCalls, "a delivered nudge never settles the status")
}

// TestNudgeNotFoundHealsGoneRun pins the heal branch: a workflow temporal no
// longer knows (typed NotFound, or the message fallback like /terminate)
// journals run.cancelled, settles the status and answers the structured
// already_gone payload — never a 409 carrying the raw temporal text.
func TestNudgeNotFoundHealsGoneRun(t *testing.T) {
	cases := map[string]error{
		"typed_not_found":  serviceerror.NewNotFound("workflow not found for ID: agent-run/deadbeef"),
		"message_fallback": errors.New("workflow not found for ID: agent-run/deadbeef"),
	}
	for name, signalErr := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeNudgeStore{source: "journal-source"}
			journal := &fakeNudgeJournal{}
			signaler := &fakeNudgeSignaler{err: signalErr}
			server := nudgeTestServer(t, store, journal, signaler)

			code, body, raw := nudgePost(t, server, "admintoken", "?project=proj-a")
			require.Equal(t, http.StatusOK, code, "a gone workflow heals instead of 409ing: %s", raw)
			require.Equal(t, "run-1", body["run_id"])
			require.Equal(t, "already_gone", body["status"])
			require.NotContains(t, raw, "workflow not found for ID", "raw temporal text must never reach the client")
			require.NotContains(t, raw, `"error"`)

			require.Len(t, journal.events, 1)
			event := journal.events[0]
			require.Equal(t, "run.cancelled", event.Type)
			require.Equal(t, "run-1", event.Context.Run)
			require.Equal(t, "proj-a", event.Context.Project)
			require.Equal(t, "journal-source", event.Source.ID)
			require.Equal(t, "workflow not found in the orchestrator (already gone)", event.Data["detail"])
			// The audit actor is the authenticated principal (requestUserID),
			// exactly like the /terminate heal branch — not the body's actor_id.
			require.Equal(t, "admin", event.Context.Actor.ID)
			// Deterministic event id: a repeat (even one racing the outbox
			// flush) dedupes on the journal's (source_id, event_id) key.
			require.Equal(t, "run-nudge/run-1/gone", event.EventID)

			require.Equal(t, []nudgeSettleCall{{id: "run-1"}}, store.settleCalls)
		})
	}
}

// TestNudgeHealIsIdempotent pins the repeat of an already-healed run: the
// first nudge journals run.cancelled and settles the status; the second —
// with the journal's terminal state now visible — short-circuits before any
// write: the same structured 200 already_gone answer, no second terminal
// event and no second settle. The deterministic event id pinned above
// (run-nudge/run-1/gone) is what makes even a race against the outbox flush
// dedupe on (source_id, event_id).
func TestNudgeHealIsIdempotent(t *testing.T) {
	store := &fakeNudgeStore{source: "journal-source"}
	journal := &fakeNudgeJournal{}
	signaler := &fakeNudgeSignaler{err: serviceerror.NewNotFound("workflow not found for ID: agent-run/deadbeef")}
	server := nudgeTestServer(t, store, journal, signaler)

	// First press: the workflow is gone, so the run heals.
	code, body, raw := nudgePost(t, server, "admintoken", "?project=proj-a")
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, "already_gone", body["status"])
	require.Len(t, journal.events, 1)
	require.Equal(t, "run-nudge/run-1/gone", journal.events[0].EventID)
	require.Equal(t, []nudgeSettleCall{{id: "run-1"}}, store.settleCalls)

	// The heal landed: the journal now reports the run terminal.
	store.mu.Lock()
	store.terminal = map[string]string{"run-1": "cancelled"}
	store.mu.Unlock()

	// Second press: same structured answer, no repeated writes.
	code, body, raw = nudgePost(t, server, "admintoken", "?project=proj-a")
	require.Equal(t, http.StatusOK, code, "a repeat must stay a 200, not a 409: %s", raw)
	require.Equal(t, "run-1", body["run_id"])
	require.Equal(t, "already_gone", body["status"])
	require.Len(t, journal.events, 1, "a run the journal already closed is never journalled again")
	require.Equal(t, []nudgeSettleCall{{id: "run-1"}}, store.settleCalls, "a terminal run is never settled again")
}

// TestNudgeSourceHintIsOnlyAHint pins D1: identity always comes from the
// store — neither an absent source_id (the child-run case the bug report
// showed) nor a stale one overrides the journal's source.
func TestNudgeSourceHintIsOnlyAHint(t *testing.T) {
	store := &fakeNudgeStore{source: "journal-source"}
	journal := &fakeNudgeJournal{}
	signaler := &fakeNudgeSignaler{}
	server := nudgeTestServer(t, store, journal, signaler)

	// No source_id parameter at all: the run still resolves and signals.
	code, _, raw := nudgePost(t, server, "admintoken", "?project=proj-a")
	require.Equal(t, http.StatusAccepted, code, raw)
	require.Equal(t, agent.WorkflowID("journal-source", "proj-a", "run-1"), signaler.calls[0].workflowID)

	// A stale hint (the digest mismatch that produced the 409) is ignored:
	// the journal's source decides the workflow identity.
	code, _, raw = nudgePost(t, server, "admintoken", "?project=proj-a&source_id=stale-hint")
	require.Equal(t, http.StatusAccepted, code, raw)
	require.Len(t, signaler.calls, 2)
	require.Equal(t, agent.WorkflowID("journal-source", "proj-a", "run-1"), signaler.calls[1].workflowID,
		"the query hint must never decide identity")
	require.Equal(t, []nudgeSourceCall{
		{project: "proj-a", runID: "run-1"},
		{project: "proj-a", runID: "run-1"},
	}, store.sourceCalls)
}

// TestNudgeUnknownRunIsNotFound: a run the journal does not know is a plain
// 404 with a readable message — no temporal call, no heal.
func TestNudgeUnknownRunIsNotFound(t *testing.T) {
	store := &fakeNudgeStore{sourceErr: workspace.ErrNotFound}
	journal := &fakeNudgeJournal{}
	signaler := &fakeNudgeSignaler{}
	server := nudgeTestServer(t, store, journal, signaler)

	code, _, raw := nudgePost(t, server, "admintoken", "?project=proj-a")
	require.Equal(t, http.StatusNotFound, code, raw)
	require.Contains(t, raw, "run-1")
	require.Contains(t, raw, "nothing to nudge")
	require.Empty(t, signaler.calls, "an unknown run never reaches the orchestrator")
	require.Empty(t, journal.events)
	require.Empty(t, store.settleCalls)
}

// TestNudgeSignalFailureIsStructured: every other signal failure answers a
// stable human-readable 409 — the raw temporal/transport text stays server-side.
func TestNudgeSignalFailureIsStructured(t *testing.T) {
	store := &fakeNudgeStore{source: "journal-source"}
	journal := &fakeNudgeJournal{}
	signaler := &fakeNudgeSignaler{err: errors.New("connection refused: temporal-frontend:7233")}
	server := nudgeTestServer(t, store, journal, signaler)

	code, body, raw := nudgePost(t, server, "admintoken", "?project=proj-a")
	require.Equal(t, http.StatusConflict, code, raw)
	require.Equal(t, "run-1", body["run_id"])
	require.NotContains(t, raw, "connection refused", "verbatim error text must not leak to the client")
	require.Contains(t, raw, "could not reach")
	require.Empty(t, journal.events, "a live-but-failing signal must not heal")
	require.Empty(t, store.settleCalls)
}
