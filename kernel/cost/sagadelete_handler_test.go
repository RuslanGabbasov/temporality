package cost

// Regression for the saga delete that never disappeared from the UI: the
// projection fold knew how to forget a deleted saga, but ProjectBudgetHandler
// never asked the journal FOR saga.deleted events — so every deleted saga
// stayed in the budget rollup (the UI saga list) forever.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/observation"
)

func TestProjectBudgetHandlerFetchesSagaDeleted(t *testing.T) {
	base := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	requested := map[string]bool{}
	journal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eventType := r.URL.Query().Get("type")
		requested[eventType] = true
		events := []observation.Event{}
		switch eventType {
		case "saga.registered":
			events = append(events, observation.Event{
				Type: "saga.registered", OccurredAt: base,
				Context: observation.Context{Project: "omni", Task: "t1", Run: "r1"},
				Data:    map[string]any{"saga_id": "saga-x", "title": "Empty effort"},
			})
		case "saga.deleted":
			events = append(events, observation.Event{
				Type: "saga.deleted", OccurredAt: base.Add(10 * time.Minute),
				Context: observation.Context{Project: "omni"},
				Data:    map[string]any{"saga_id": "saga-x"},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "next_cursor": ""})
	}))
	defer journal.Close()

	cfg, err := ParsePrices("m:1.00:2.00")
	require.NoError(t, err)
	api := NewCostAPI(cfg, journal.URL, "")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/agent/cost/project-budget?project=omni", nil)
	api.ProjectBudgetHandler(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)

	require.True(t, requested["saga.registered"])
	require.True(t, requested["saga.deleted"], "the journal query must ask for saga.deleted — the fold can only forget what it receives")

	var budget ProjectBudget
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &budget))
	require.Empty(t, budget.Sagas, "a deleted saga must leave the budget rollup (the UI saga list)")
}
