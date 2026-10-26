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

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/workspace"
)

// sweepStore fakes the store face the sweep needs.
type sweepStore struct {
	runs     []workspace.Run
	terminal map[string]string
	sources  map[string]string
	updated  map[string]string
	probed   []string
}

func (s *sweepStore) ListStartedRuns(ctx context.Context, limit int) ([]workspace.Run, error) {
	return s.runs, nil
}

func (s *sweepStore) RunTerminalStatuses(ctx context.Context, runIDs []string) (map[string]string, error) {
	return s.terminal, nil
}

func (s *sweepStore) RunSourceID(ctx context.Context, project, runID string) (string, error) {
	source, ok := s.sources[runID]
	if !ok {
		return "", errors.New("no journal events")
	}
	s.probed = append(s.probed, runID)
	return source, nil
}

func (s *sweepStore) UpdateRunStatus(ctx context.Context, id, status, answer string, turns int, runErr string) error {
	s.updated[id] = status
	return nil
}

// sweepLiveness fakes the Temporal liveness probe.
type sweepLiveness struct {
	running map[string]bool
	errOn   map[string]bool
}

func (l *sweepLiveness) IsWorkflowRunning(ctx context.Context, sourceID, project, runID string) (bool, error) {
	if l.errOn[runID] {
		return false, errors.New("temporal rpc deadline")
	}
	return l.running[runID], nil
}

func TestSweepOrphanedRuns(t *testing.T) {
	var mu sync.Mutex
	var failedEvents []map[string]any
	journal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		failedEvents = append(failedEvents, payload.Events...)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer journal.Close()

	store := &sweepStore{
		runs: []workspace.Run{
			{ID: "lost-run", ProjectID: "proj", Status: "started"},
			{ID: "live-run", ProjectID: "proj", Status: "started"},
			{ID: "already-ended", ProjectID: "proj", Status: "started"},
			{ID: "probe-error", ProjectID: "proj", Status: "started"},
			{ID: "no-journal", ProjectID: "proj", Status: "started"},
		},
		terminal: map[string]string{"already-ended": "completed"},
		sources: map[string]string{
			"lost-run":    "kernel-source",
			"live-run":    "kernel-source",
			"probe-error": "kernel-source",
		},
		updated: map[string]string{},
	}
	liveness := &sweepLiveness{running: map[string]bool{"live-run": true}, errOn: map[string]bool{"probe-error": true}}

	sweepOrphanedRuns(context.Background(), store, liveness, journal.URL, "token", "kernel-source", slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Only the truly lost run closes: live ones stay, terminal-journaled ones
	// stay, probe errors stay (next restart decides), journal-less runs are
	// never probed.
	require.Equal(t, map[string]string{"lost-run": "failed"}, store.updated)
	require.NotContains(t, store.probed, "no-journal")
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, failedEvents, 1)
	event := failedEvents[0]
	require.Equal(t, "run.failed", event["type"])
	data := event["data"].(map[string]any)
	require.Equal(t, true, data["orphaned"])
	require.Contains(t, data["error"], "restart")
	require.Equal(t, "lost-run", event["context"].(map[string]any)["run"])
}

// A sweep round with zero started runs must not touch the journal or liveness.
func TestSweepOrphanedRunsNoCandidates(t *testing.T) {
	store := &sweepStore{updated: map[string]string{}}
	liveness := &sweepLiveness{}
	sweepOrphanedRuns(context.Background(), store, liveness, "", "", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Empty(t, store.updated)
	require.Empty(t, liveness.running)
}
