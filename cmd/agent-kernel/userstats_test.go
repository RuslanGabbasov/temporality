package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/observation"
)

func TestUserStatsFrom(t *testing.T) {
	// Default: the week window, normalized key echoed back to the client.
	key, from, err := userStatsFrom("")
	if err != nil || key != "168h" {
		t.Fatalf("default: key=%q err=%v", key, err)
	}
	if from.IsZero() || time.Since(from) < 6*24*time.Hour || time.Since(from) > 8*24*time.Hour {
		t.Fatalf("default bound not a week ago: %v", from)
	}

	key, from, err = userStatsFrom("24h")
	if err != nil || key != "24h" {
		t.Fatalf("24h: key=%q err=%v", key, err)
	}
	if d := time.Since(from); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("24h bound off: %v", d)
	}

	// "all" drops the bound: zero time means no lower filter.
	key, from, err = userStatsFrom("all")
	if err != nil || key != "all" || !from.IsZero() {
		t.Fatalf("all: key=%q from=%v err=%v", key, from, err)
	}

	if _, _, err = userStatsFrom("10d"); err == nil {
		t.Fatal("unknown window must be a 400, not a silent default")
	}
}

func TestFoldUserUsageAttributesOnlyOwnRuns(t *testing.T) {
	prices, err := cost.ParsePrices("test-model:2:10")
	if err != nil {
		t.Fatalf("prices: %v", err)
	}
	own := &userRunAgg{project: "p1", agent: "a1"}
	// The index holds ONLY the user's runs (built from workspace_run by the
	// caller) — a foreign run id is simply absent from it.
	index := map[string]*userRunAgg{"run-own": own}

	events := []observation.Event{
		{Context: observation.Context{Run: "run-own"}, Data: map[string]any{
			"model": "test-model", "prompt_tokens": 1_000_000.0, "completion_tokens": 100_000.0,
		}},
		{Context: observation.Context{Run: "run-own"}, Data: map[string]any{
			"model": "test-model", "prompt_tokens": 500_000.0, "completion_tokens": 0.0,
		}},
		// Someone else's run: priced nowhere.
		{Context: observation.Context{Run: "run-other"}, Data: map[string]any{
			"model": "test-model", "prompt_tokens": 999_000.0, "completion_tokens": 999_000.0,
		}},
		// No run context: skipped.
		{Data: map[string]any{"model": "test-model", "prompt_tokens": 100.0}},
	}

	tokens, usd, calls := foldUserUsage(events, index, prices)
	if tokens != 1_600_000 || calls != 2 {
		t.Fatalf("totals: tokens=%d calls=%d", tokens, calls)
	}
	wantUSD := prices.ModelCost("test-model", 1_000_000, 0, 100_000) +
		prices.ModelCost("test-model", 500_000, 0, 0)
	if usd != wantUSD || own.cost != wantUSD {
		t.Fatalf("cost: got %v want %v (own %v)", usd, wantUSD, own.cost)
	}
	if own.tokens != 1_600_000 || own.calls != 2 {
		t.Fatalf("own agg: %+v", own)
	}
}

func sagaEvent(kind, sagaID, actor, taskID string, data map[string]any) observation.Event {
	payload := map[string]any{"saga_id": sagaID, "actor_id": actor}
	if taskID != "" {
		payload["task_id"] = taskID
	}
	for k, v := range data {
		payload[k] = v
	}
	return observation.Event{
		Type: kind,
		Data: payload,
		Context: observation.Context{
			Actor: observation.Actor{ID: actor, Type: "human"},
		},
	}
}

func TestFoldUserSagas(t *testing.T) {
	const me = "u1"
	taskRuns := map[string]int{"task-a": 3, "task-b": 1, "task-loose": 2}

	events := []observation.Event{
		sagaEvent("saga.registered", "saga-one", me, "task-a", map[string]any{"title": "Первая"}),
		sagaEvent("saga.attached", "saga-one", "someone-else", "task-b", nil),
		// A saga the user never touched, but their runs execute its task.
		sagaEvent("saga.registered", "saga-two", "mentor", "", map[string]any{"title": "Чужая"}),
		sagaEvent("saga.attached", "saga-two", "mentor", "task-loose", nil),
		// A deleted saga's tasks stop counting.
		sagaEvent("saga.registered", "saga-gone", me, "", map[string]any{"title": "Удаляемая"}),
		sagaEvent("saga.deleted", "saga-gone", me, "", nil),
		// Merge: the source's role and bindings re-project onto the target.
		sagaEvent("saga.registered", "saga-src", me, "", map[string]any{"title": "Источник"}),
		sagaEvent("saga.merged", "saga-tgt", "mentor", "", map[string]any{"source_saga_ids": []any{"saga-src"}}),
	}

	byID := map[string]UserSagaStat{}
	for _, stat := range foldUserSagas(events, me, taskRuns) {
		byID[stat.SagaID] = stat
	}

	if got := byID["saga-one"]; got.Runs != 4 || got.Role != "registered" || got.Title != "Первая" {
		t.Fatalf("saga-one: %+v", got)
	}
	if got := byID["saga-two"]; got.Runs != 2 || got.Role != "work" || got.Title != "Чужая" {
		t.Fatalf("saga-two: %+v", got)
	}
	if _, ok := byID["saga-gone"]; ok {
		t.Fatal("deleted saga must not be listed")
	}
	// The source folded into the target with the user's registered role.
	if _, ok := byID["saga-src"]; ok {
		t.Fatal("merged source must not be listed under its own id")
	}
	if got := byID["saga-tgt"]; got.Role != "registered" {
		t.Fatalf("merged target role: %+v", got)
	}
	// Loose tasks bind nowhere — they never invent a saga.
	for _, stat := range foldUserSagas(events, me, map[string]int{"task-loose": 5}) {
		if stat.SagaID == "" {
			t.Fatal("empty saga id")
		}
	}
}

// The dossier mounts next to the row metrics on the same mux; a pattern
// collision between /users/metrics, /users/{id}/stats and /users/{id}/facts
// panics at HandleFunc time, so registration itself is under test.
func TestUserStatsRoutesRegister(t *testing.T) {
	gate, err := controlplane.NewGate("")
	if err != nil {
		t.Skipf("gate: %v", err)
	}
	prices, err := cost.ParsePrices("")
	if err != nil {
		t.Fatalf("prices: %v", err)
	}
	mux := http.NewServeMux()
	registerUserMetricsRoutes(mux, gate, nil, cost.DefaultHumanFactors)
	registerUserStatsRoutes(mux, gate, nil, cost.DefaultHumanFactors, "http://localhost:8080", "", prices)
}

func TestUserStatsCaseMatches(t *testing.T) {
	ids := map[string]bool{"u1": true, "run-42": true, "task-7": true}

	if !userStatsCaseMatches("u1", nil, ids) {
		t.Fatal("subject naming the user must match")
	}
	if !userStatsCaseMatches("run-42", nil, ids) {
		t.Fatal("subject naming one of the user's runs must match")
	}
	if userStatsCaseMatches("someone-else", nil, ids) {
		t.Fatal("foreign subject must not match")
	}
	if !userStatsCaseMatches("knowledge_hygiene", []byte(`{"run_id":"run-42"}`), ids) {
		t.Fatal("payload quoting a run id must match")
	}
	if userStatsCaseMatches("knowledge_hygiene", []byte(`{"run_id":"run-99"}`), ids) {
		t.Fatal("payload quoting a foreign run must not match")
	}
	// Short ids must not quote-match: "u1" appears inside countless words.
	if userStatsCaseMatches("x", []byte(`{"note":"busy"}`), ids) {
		t.Fatal("short ids must not substring-match")
	}
}
