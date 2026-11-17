package cost

// The project-budget rollup scans a project's whole journal history — for a
// long-lived project that is hundreds of journal round-trips per request, and
// the dashboard's Sagas tab paid it on EVERY load. The handler now fetches
// the per-type paginations concurrently and caches per project (single-flight
// + short TTL). These tests pin both halves:
//   - the parallel fetch feeds the same aggregate as the sequential one;
//   - repeated loads inside the TTL share one journal scan, an expired one
//     re-scans.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/observation"
)

// budgetTestJournal serves every budget event type with a page of generated
// events (50 model calls over 5 runs) and counts the requests per type.
func budgetTestJournal(t *testing.T) (*httptest.Server, *map[string]int) {
	t.Helper()
	var hitsMu sync.Mutex
	hits := map[string]int{}
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	journal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eventType := r.URL.Query().Get("type")
		hitsMu.Lock()
		hits[eventType]++
		hitsMu.Unlock()
		events := []observation.Event{}
		for i := 0; i < 5; i++ {
			run := fmt.Sprintf("root/delegate/%02d", i)
			switch eventType {
			case "run.started":
				events = append(events, observation.Event{
					Type: "run.started", OccurredAt: base.Add(time.Duration(i) * time.Minute),
					Context: observation.Context{Project: "omni", Task: "t1", Run: run},
				})
			case "model.completed":
				for turn := 0; turn < 10; turn++ {
					events = append(events, observation.Event{
						Type: "model.completed", OccurredAt: base.Add(time.Duration(i) * time.Minute).Add(time.Duration(turn) * time.Second),
						Context: observation.Context{Project: "omni", Task: "t1", Run: run},
						Data: map[string]any{"prompt_tokens": 100, "completion_tokens": 10},
					})
				}
			case "run.completed":
				events = append(events, observation.Event{
					Type: "run.completed", OccurredAt: base.Add(time.Duration(i) * time.Minute).Add(time.Minute),
					Context: observation.Context{Project: "omni", Task: "t1", Run: run},
				})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "next_cursor": ""})
	}))
	t.Cleanup(journal.Close)
	return journal, &hits
}

func TestProjectBudgetParallelFetchMatchesSequential(t *testing.T) {
	journal, _ := budgetTestJournal(t)
	cfg, err := ParsePrices("m:1.00:2.00")
	require.NoError(t, err)

	ctx := t.Context()

	// The sequential reference: the classic one-type-block-at-a-time scan.
	sequential := NewCostAPI(cfg, journal.URL, "")
	seqEvents, err := sequential.queryEventsFilteredCtx(ctx, "omni", "", "", budgetEventTypes...)
	require.NoError(t, err)
	seqBudget := sequential.aggregateProjectBudget("omni", seqEvents)

	parallel := NewCostAPI(cfg, journal.URL, "")
	parallelEvents, err := parallel.queryEventsFilteredParallel(ctx, "omni", "", "", budgetEventTypes...)
	require.NoError(t, err)
	require.NotEmpty(t, parallelEvents)
	parallelBudget := parallel.aggregateProjectBudget("omni", parallelEvents)

	// 5 runs × 10 turns × 100+10 tokens — one task row, all its spend folded;
	// the fetch order must not move a single token.
	require.Len(t, parallelBudget.Tasks, 1)
	require.Equal(t, 50, parallelBudget.ModelCalls)
	require.Equal(t, 5, parallelBudget.RunCount)
	require.Equal(t, 50*100, parallelBudget.TotalPromptTokens)
	require.Equal(t, 50*10, parallelBudget.TotalCompletionTokens)
	require.Equal(t, seqBudget, parallelBudget)
}

func TestProjectBudgetHandlerCachesWithinTTL(t *testing.T) {
	journal, hits := budgetTestJournal(t)
	cfg, err := ParsePrices("m:1.00:2.00")
	require.NoError(t, err)
	api := NewCostAPI(cfg, journal.URL, "")

	load := func() ProjectBudget {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/agent/cost/project-budget?project=omni", nil)
		api.ProjectBudgetHandler(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code)
		var budget ProjectBudget
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &budget))
		return budget
	}

	first := load()
	second := load()
	// The TTL collapsed the second load: not one extra journal round-trip,
	// byte-identical rollup. Each type was scanned exactly once.
	for eventType, count := range *hits {
		require.Equal(t, 1, count, "type %s re-scanned within the TTL", eventType)
	}
	require.Equal(t, first, second)

	// An expired window re-scans.
	entry := api.budgetEntry("omni")
	entry.mu.Lock()
	entry.fetchedAt = time.Now().Add(-2 * budgetCacheTTL)
	entry.mu.Unlock()
	load()
	require.Greater(t, (*hits)["model.completed"], 1, "an expired cache must re-scan the journal")
}
