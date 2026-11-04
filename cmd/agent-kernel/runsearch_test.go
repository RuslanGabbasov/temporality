package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// journalEventFields must read the NESTED observation envelope
// (temporality.event/1). The original code read flat keys (event["run"],
// event["actor"], …) that never existed: every field came out empty, so
// run_search listed nothing and trace_read rejected every call with 403
// «run was started outside your org scope» — the empty actor is in nobody's
// org scope. This test pins the shape against the real journal payload.
func TestJournalEventFields(t *testing.T) {
	event := map[string]any{
		"schema":      "temporality.event/1",
		"event_id":    "src/event/000001",
		"occurred_at": "2026-11-02T14:22:28Z",
		"type":        "run.started",
		"context": map[string]any{
			"project": "intake",
			"run":     "nuzhno-napisat-tekhproekt-20261102-142228/delegate/03",
			"task":    "nuzhno-napisat-tekhproekt-20261102-142228",
			"actor":   map[string]any{"id": "user-3e3146365cfdc415", "type": "agent"},
		},
		"data": map[string]any{"title": "Ревью техпроекта", "agent_id": "reviewer"},
	}
	runID, project, actorID, title := journalEventFields(event)
	require.Equal(t, "nuzhno-napisat-tekhproekt-20261102-142228/delegate/03", runID)
	require.Equal(t, "intake", project)
	require.Equal(t, "user-3e3146365cfdc415", actorID, "the run's initiator drives the org-scope check")
	require.Equal(t, "Ревью техпроекта", title)

	// A partial envelope degrades to empty strings, never panics.
	runID, project, actorID, title = journalEventFields(map[string]any{"type": "run.completed"})
	require.Empty(t, runID)
	require.Empty(t, project)
	require.Empty(t, actorID)
	require.Empty(t, title)
}
