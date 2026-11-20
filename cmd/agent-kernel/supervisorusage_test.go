package main

import (
	"context"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

func supervisorUsageEvent(purpose, model string, prompt, cached, completion int64) observation.Event {
	return observation.Event{
		Type: supervisorUsageEventType,
		Data: map[string]any{
			"model":             model,
			"purpose":           purpose,
			"prompt_tokens":     float64(prompt),
			"cached_tokens":     float64(cached),
			"completion_tokens": float64(completion),
		},
	}
}

func TestFoldSupervisorUsage(t *testing.T) {
	prices, err := cost.ParsePrices("model-a:2:10,model-b:4:20")
	if err != nil {
		t.Fatalf("prices: %v", err)
	}
	events := []observation.Event{
		supervisorUsageEvent("triage", "model-a", 1000, 100, 200),
		supervisorUsageEvent("insights", "model-a", 500, 0, 100),
		supervisorUsageEvent("cases", "model-b", 300, 0, 50),
		// A run's own model.completed must never leak into the aggregate.
		{Type: "model.completed", Data: map[string]any{"model": "model-a", "prompt_tokens": float64(999999)}},
	}

	usage := foldSupervisorUsage(events, prices)
	if usage.Calls != 3 {
		t.Fatalf("calls = %d, want 3", usage.Calls)
	}
	if usage.PromptTokens != 1800 || usage.CompletionTokens != 350 || usage.CachedTokens != 100 {
		t.Fatalf("token sums: %+v", usage)
	}
	if usage.Tokens != 2150 {
		t.Fatalf("tokens = %d, want 2150", usage.Tokens)
	}
	wantUSD := prices.ModelCost("model-a", 1000, 100, 200) +
		prices.ModelCost("model-a", 500, 0, 100) +
		prices.ModelCost("model-b", 300, 0, 50)
	if usage.CostUSD != wantUSD {
		t.Fatalf("cost = %v, want %v", usage.CostUSD, wantUSD)
	}

	// Breakdowns sort by spend (tokens desc) — the biggest consumer first.
	if len(usage.ByPurpose) != 3 || usage.ByPurpose[0].Key != "triage" || usage.ByPurpose[0].Tokens != 1200 {
		t.Fatalf("by_purpose: %+v", usage.ByPurpose)
	}
	if len(usage.ByModel) != 2 || usage.ByModel[0].Key != "model-a" || usage.ByModel[0].Tokens != 1800 {
		t.Fatalf("by_model: %+v", usage.ByModel)
	}
	// Empty windows still serialise as [] (the dashboard reads .length).
	empty := foldSupervisorUsage(nil, prices)
	if empty.ByPurpose == nil || empty.ByModel == nil || empty.Calls != 0 {
		t.Fatalf("empty fold: %+v", empty)
	}
}

func TestSupervisorPurposeContext(t *testing.T) {
	// Unstamped chains degrade to the generic bucket, not a crash.
	if got := supervisorPurposeOf(context.Background()); got != "other" {
		t.Fatalf("default purpose = %q", got)
	}
	ctx := withSupervisorPurpose(context.Background(), "triage")
	if got := supervisorPurposeOf(ctx); got != "triage" {
		t.Fatalf("purpose = %q", got)
	}
	// The purpose must survive the deadline wrappers real passes add on top.
	derived, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if got := supervisorPurposeOf(derived); got != "triage" {
		t.Fatalf("purpose after WithTimeout = %q", got)
	}
}

func TestJournalSupervisorUsage(t *testing.T) {
	var captured []observation.Event
	outbox := func(_ context.Context, event observation.Event) error {
		captured = append(captured, event)
		return nil
	}
	event := llm.UsageEvent{
		Model: "judge-model", Provider: "api.example.com",
		Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		LatencyMs: 42,
	}
	if err := journalSupervisorUsage(context.Background(), outbox, event, "triage"); err != nil {
		t.Fatalf("journal: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("captured %d events", len(captured))
	}
	got := captured[0]
	if got.Type != supervisorUsageEventType {
		t.Fatalf("type = %q", got.Type)
	}
	if got.Context.Actor.ID != "supervisor" || got.Context.Actor.Type != "system" {
		t.Fatalf("actor = %+v", got.Context.Actor)
	}
	if got.Data["purpose"] != "triage" || got.Data["model"] != "judge-model" {
		t.Fatalf("data = %+v", got.Data)
	}
	if got.OccurredAt.IsZero() || got.EventID == "" {
		t.Fatalf("envelope incomplete: %+v", got)
	}
	// A nil outbox (early boot) is a no-op, not a panic.
	if err := journalSupervisorUsage(context.Background(), nil, event, "triage"); err != nil {
		t.Fatalf("nil outbox: %v", err)
	}
}
