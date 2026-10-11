package cost

import (
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

func TestParsePrices(t *testing.T) {
	cfg, err := ParsePrices("gpt-4o:2.50:10.00,claude-3-sonnet:3.00:15.00")
	if err != nil {
		t.Fatal(err)
	}
	prices := cfg.Prices()
	if len(prices) != 2 {
		t.Fatalf("expected 2 prices, got %d", len(prices))
	}
}

func TestParsePricesEmpty(t *testing.T) {
	cfg, err := ParsePrices("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Prices()) != 0 {
		t.Fatalf("expected 0 prices, got %d", len(cfg.Prices()))
	}
}

func TestModelCost(t *testing.T) {
	cfg, _ := ParsePrices("gpt-4o:2.50:10.00")
	// 1000 prompt tokens * $2.50/1k = $2.50
	// 500 completion tokens * $10.00/1k = $5.00
	// Total: $7.50
	cost := cfg.ModelCost("gpt-4o", 1000, 0, 500)
	if cost != 7.5 {
		t.Fatalf("expected 7.50, got %f", cost)
	}
}

func TestModelCostCachedHalfPrice(t *testing.T) {
	cfg, _ := ParsePrices("gpt-4o:2.50:10.00")
	// 800 cached of 1000 prompt: 200 fresh * $2.50/1k + 800 * $1.25/1k
	// = $0.50 + $1.00; 500 completion * $10.00/1k = $5.00 → $6.50
	cost := cfg.ModelCost("gpt-4o", 1000, 800, 500)
	if cost != 6.5 {
		t.Fatalf("expected 6.50, got %f", cost)
	}
	// Providers occasionally over-report cached vs prompt; clamp to prompt.
	if got := cfg.ModelCost("gpt-4o", 100, 500, 0); got != cfg.ModelCost("gpt-4o", 100, 100, 0) {
		t.Fatalf("cached must clamp to prompt: %f vs %f", got, cfg.ModelCost("gpt-4o", 100, 100, 0))
	}
}

func TestModelCostUnknown(t *testing.T) {
	cfg, _ := ParsePrices("gpt-4o:2.50:10.00")
	cost := cfg.ModelCost("unknown", 1000, 0, 500)
	if cost != 0 {
		t.Fatalf("expected 0 for unknown model, got %f", cost)
	}
}

func TestModelCostPartialTokens(t *testing.T) {
	cfg, _ := ParsePrices("gpt-4o:2.50:10.00")
	// 100 prompt tokens * $2.50/1k = $0.25
	// 100 completion tokens * $10.00/1k = $1.00
	cost := cfg.ModelCost("gpt-4o", 100, 0, 100)
	if cost != 1.25 {
		t.Fatalf("expected 1.25, got %f", cost)
	}
}

func TestParsePricesInvalid(t *testing.T) {
	_, err := ParsePrices("gpt-4o:2.50")
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	_, err = ParsePrices("gpt-4o:abc:10.00")
	if err == nil {
		t.Fatal("expected error for invalid price")
	}
}

func TestAggregateTask(t *testing.T) {
	cfg, _ := ParsePrices("m:1.00:2.00")
	api := NewCostAPI(cfg, "", "")
	base := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	api.now = func() time.Time { return base.Add(2 * time.Hour) }

	mk := func(typ, run string, at time.Time, data map[string]any) observation.Event {
		return observation.Event{Type: typ, OccurredAt: at, Context: observation.Context{Run: run, Task: "t1"}, Data: data}
	}
	events := []observation.Event{
		// Root run 1 (lead agent): starts, delegates to a QA child, completes.
		mk("run.started", "r1", base, map[string]any{"agent_id": "lead", "task_id": "t1"}),
		mk("model.completed", "r1", base.Add(1*time.Minute), map[string]any{"model": "m", "prompt_tokens": 1000, "cached_tokens": 200, "completion_tokens": 400}),
		mk("run.started", "r1/delegate/01", base.Add(2*time.Minute), map[string]any{"agent_id": "qa", "parent_run_id": "r1"}),
		mk("model.completed", "r1/delegate/01", base.Add(3*time.Minute), map[string]any{"model": "m", "prompt_tokens": 500, "completion_tokens": 100}),
		mk("run.completed", "r1/delegate/01", base.Add(4*time.Minute), nil),
		mk("run.completed", "r1", base.Add(5*time.Minute), nil),
		// Root run 2 (follow-up message, same task, reviewer agent) still running.
		mk("run.started", "r2", base.Add(30*time.Minute), map[string]any{"agent_id": "reviewer"}),
		mk("model.completed", "r2", base.Add(31*time.Minute), map[string]any{"model": "m", "prompt_tokens": 200, "completion_tokens": 50}),
		// A foreign-task event that must not leak into the aggregation.
		{Type: "model.completed", OccurredAt: base.Add(32 * time.Minute), Context: observation.Context{Run: "other", Task: "t2"}, Data: map[string]any{"model": "m", "prompt_tokens": 999, "completion_tokens": 999}},
	}

	got := api.aggregateTask("p", "t1", events)
	if got.RunCount != 3 {
		t.Fatalf("expected 3 runs, got %d", got.RunCount)
	}
	if got.ModelCalls != 3 {
		t.Fatalf("expected 3 model calls, got %d", got.ModelCalls)
	}
	if got.TotalPromptTokens != 1700 || got.TotalCompletionTokens != 550 || got.TotalTokens != 2250 {
		t.Fatalf("unexpected token totals: %+v", got)
	}
	// 1500 fresh + 200 cached(half) prompt, 550 completion: (1.5*1) + (0.2*0.5) + (0.55*2) = $2.70
	if got.TotalCostUSD != 2.70 {
		t.Fatalf("expected $2.45, got %f", got.TotalCostUSD)
	}
	// r1 5m + child 2m + r2 running 90m → active 97m
	if got.ActiveMS != 97*time.Minute.Milliseconds() {
		t.Fatalf("expected active 97m, got %dms", got.ActiveMS)
	}
	// elapsed = task start (r1 at base) → now (r2 still running) = 2h
	if got.ElapsedMS != 2*time.Hour.Milliseconds() {
		t.Fatalf("expected elapsed 90m, got %dms", got.ElapsedMS)
	}
	if len(got.Agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(got.Agents))
	}
	if got.Agents[0].AgentID != "lead" { // highest tokens first
		t.Fatalf("expected lead first, got %+v", got.Agents[0])
	}
	r2 := got.Runs[2]
	if r2.Status != "running" || r2.DurationMS != 90*time.Minute.Milliseconds() {
		t.Fatalf("expected r2 running for 90m, got %+v", r2)
	}
	child := got.Runs[1]
	if child.ParentRunID != "r1" || child.AgentID != "qa" || child.Status != "completed" {
		t.Fatalf("unexpected child breakdown: %+v", child)
	}
}

func TestAggregateTaskEmpty(t *testing.T) {
	api := NewCostAPI(Config{}, "", "")
	got := api.aggregateTask("p", "missing", nil)
	if got.RunCount != 0 || got.TotalTokens != 0 || got.ElapsedMS != 0 {
		t.Fatalf("expected zeroed task cost, got %+v", got)
	}
}
