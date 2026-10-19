package observation

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// healthTestEvent builds a minimal but realistic journal event: the health
// aggregation only reads Type, OccurredAt, Context.Run and Data, but events
// keep the full envelope shape the stores return.
func healthTestEvent(id, eventType, run string, at time.Time, data map[string]any) Event {
	return Event{
		Schema: Schema, EventID: id, OccurredAt: at,
		Source:  Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
		Context: Context{Project: "lighthouse", Run: run, Actor: Actor{ID: "coder", Type: "agent"}},
		Type:    eventType, Data: data,
	}
}

func TestComputeHealthStatsInFlight(t *testing.T) {
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		events    []Event
		wantModel string
		wantCalls int
		wantIn    int
	}{
		{
			name:      "started without terminal stays in flight",
			events:    []Event{healthTestEvent("e1", "model.started", "r1", base, map[string]any{"turn": 1, "model": "gpt-5.2"})},
			wantModel: "gpt-5.2", wantCalls: 1, wantIn: 1,
		},
		{
			name: "started then completed clears",
			events: []Event{
				healthTestEvent("e1", "model.started", "r1", base, map[string]any{"turn": 1, "model": "gpt-5.2"}),
				healthTestEvent("e2", "model.completed", "r1", base.Add(time.Second), map[string]any{"turn": 1, "model": "gpt-5.2"}),
			},
			wantModel: "gpt-5.2", wantCalls: 1, wantIn: 0,
		},
		{
			name: "failed clears",
			events: []Event{
				healthTestEvent("e1", "model.started", "r1", base, map[string]any{"turn": 1, "model": "gpt-5.2"}),
				healthTestEvent("e2", "model.failed", "r1", base.Add(time.Second), map[string]any{"turn": 1, "model": "gpt-5.2"}),
			},
			wantModel: "gpt-5.2", wantCalls: 1, wantIn: 0,
		},
		{
			name: "turn as float64 matches int turn of the same call",
			events: []Event{
				healthTestEvent("e1", "model.started", "r1", base, map[string]any{"turn": 1, "model": "gpt-5.2"}),
				healthTestEvent("e2", "model.completed", "r1", base.Add(time.Second), map[string]any{"turn": float64(1), "model": "gpt-5.2"}),
			},
			wantModel: "gpt-5.2", wantCalls: 1, wantIn: 0,
		},
		{
			name: "different turns are separate calls",
			events: []Event{
				healthTestEvent("e1", "model.started", "r1", base, map[string]any{"turn": 1, "model": "gpt-5.2"}),
				healthTestEvent("e2", "model.started", "r1", base.Add(time.Second), map[string]any{"turn": 2, "model": "gpt-5.2"}),
				healthTestEvent("e3", "model.completed", "r1", base.Add(2*time.Second), map[string]any{"turn": 1, "model": "gpt-5.2"}),
			},
			wantModel: "gpt-5.2", wantCalls: 2, wantIn: 1,
		},
		{
			name: "in flight is attributed to the started event's model",
			events: []Event{
				// The terminal event carries no model at all; the slot still
				// belongs to the model that opened it.
				healthTestEvent("e1", "model.started", "r1", base, map[string]any{"turn": 1, "model": "gpt-5.2"}),
				healthTestEvent("e2", "model.completed", "r1", base.Add(time.Second), map[string]any{"turn": 1}),
				healthTestEvent("e3", "model.started", "r2", base.Add(2*time.Second), map[string]any{"turn": 1, "model": "gpt-5.2"}),
			},
			wantModel: "gpt-5.2", wantCalls: 2, wantIn: 1,
		},
		{
			name: "events before the window are ignored",
			events: []Event{
				healthTestEvent("e1", "model.started", "r1", base.Add(-time.Hour), map[string]any{"turn": 1, "model": "gpt-5.2"}),
				healthTestEvent("e2", "model.started", "r2", base, map[string]any{"turn": 1, "model": "gpt-5.2"}),
			},
			wantModel: "gpt-5.2", wantCalls: 1, wantIn: 1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stats := ComputeHealthStats(testCase.events, base.Add(-time.Minute), base.Add(time.Minute), DefaultHumanFactors)
			var model *HealthModelStats
			for i := range stats.Models {
				if stats.Models[i].Model == testCase.wantModel {
					model = &stats.Models[i]
				}
			}
			if model == nil {
				t.Fatalf("model %q missing in %v", testCase.wantModel, stats.Models)
			}
			if model.Calls != testCase.wantCalls {
				t.Fatalf("calls = %d, want %d", model.Calls, testCase.wantCalls)
			}
			if model.InFlight != testCase.wantIn {
				t.Fatalf("in_flight = %d, want %d", model.InFlight, testCase.wantIn)
			}
			if stats.Totals.InFlight != testCase.wantIn {
				t.Fatalf("totals.in_flight = %d, want %d", stats.Totals.InFlight, testCase.wantIn)
			}
		})
	}
}

func TestComputeHealthStatsPercentiles(t *testing.T) {
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		latency []any
		wantP50 int64
		wantP95 int64
	}{
		{name: "ten values", latency: []any{float64(100), float64(200), float64(300), float64(400), float64(500), float64(600), float64(700), float64(800), float64(900), float64(1000)}, wantP50: 500, wantP95: 1000},
		{name: "four values", latency: []any{float64(100), float64(200), float64(300), float64(400)}, wantP50: 200, wantP95: 400},
		{name: "single value", latency: []any{float64(321)}, wantP50: 321, wantP95: 321},
		{name: "negative and absent are skipped", latency: []any{float64(-1), nil, float64(100), float64(200), float64(300)}, wantP50: 200, wantP95: 300},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			events := make([]Event, 0, len(testCase.latency))
			for i, latency := range testCase.latency {
				data := map[string]any{"turn": i + 1, "model": "gpt-5.2"}
				if latency != nil {
					data["latency_ms"] = latency
				}
				events = append(events, healthTestEvent("e"+strconv.Itoa(i), "model.completed", "r1", base.Add(time.Duration(i)*time.Second), data))
			}
			stats := ComputeHealthStats(events, base.Add(-time.Minute), base.Add(time.Hour), DefaultHumanFactors)
			if len(stats.Models) != 1 || stats.Models[0].Model != "gpt-5.2" {
				t.Fatalf("models = %v, want exactly gpt-5.2", stats.Models)
			}
			if stats.Models[0].P50LatencyMS != testCase.wantP50 {
				t.Fatalf("p50 = %d, want %d", stats.Models[0].P50LatencyMS, testCase.wantP50)
			}
			if stats.Models[0].P95LatencyMS != testCase.wantP95 {
				t.Fatalf("p95 = %d, want %d", stats.Models[0].P95LatencyMS, testCase.wantP95)
			}
		})
	}
}

func TestComputeHealthStatsApprovals(t *testing.T) {
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	op := func(id string) map[string]any {
		return map[string]any{"operation_id": id, "arguments_hash": "sha256:" + id}
	}
	events := []Event{
		healthTestEvent("a1", "approval.requested", "r1", base, op("op-1")),
		healthTestEvent("a2", "approval.requested", "r1", base, op("op-2")),
		// A re-request of op-2 restarts its wait clock: 10s here, 30s to grant.
		healthTestEvent("a3", "approval.requested", "r1", base.Add(10*time.Second), op("op-2")),
		healthTestEvent("a4", "approval.granted", "r1", base.Add(40*time.Second), op("op-2")),
		healthTestEvent("a5", "approval.requested", "r2", base, op("op-3")),
		healthTestEvent("a6", "approval.rejected", "r2", base.Add(time.Minute), op("op-3")),
		healthTestEvent("a7", "approval.auto_granted", "r3", base.Add(2*time.Minute), op("op-4")),
		// Same operation in another run is a different key: still pending.
		healthTestEvent("a8", "approval.requested", "r4", base.Add(3*time.Minute), op("op-1")),
	}
	// Feed the events in reverse order: the aggregation must sort by OccurredAt
	// itself, matching store output that carries no ordering guarantee.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	stats := ComputeHealthStats(events, base.Add(-time.Minute), base.Add(time.Hour), DefaultHumanFactors)
	want := HealthApprovals{Requested: 5, Granted: 1, Rejected: 1, TimedOut: 0, AutoGranted: 1, Pending: 2, WaitMSAvg: 45000}
	if stats.Approvals != want {
		t.Fatalf("approvals = %+v, want %+v", stats.Approvals, want)
	}
}

func TestComputeHealthStatsRunningNow(t *testing.T) {
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	events := []Event{
		healthTestEvent("r1s", "run.started", "run-a", base, nil),
		healthTestEvent("r2s", "run.started", "run-b", base.Add(time.Second), nil),
		healthTestEvent("r2c", "run.completed", "run-b", base.Add(2*time.Second), nil),
		healthTestEvent("r3s", "run.started", "run-c", base.Add(3*time.Second), nil),
		healthTestEvent("r3f", "run.failed", "run-c", base.Add(4*time.Second), nil),
		healthTestEvent("r4s", "run.started", "run-d", base.Add(5*time.Second), nil),
		healthTestEvent("r4x", "run.cancelled", "run-d", base.Add(6*time.Second), nil),
		healthTestEvent("r5s", "run.started", "run-e", base.Add(7*time.Second), nil),
		// A terminal event arriving before its started event in slice order
		// must not resurrect the run.
		healthTestEvent("r5c", "run.completed", "run-e", base.Add(8*time.Second), nil),
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	stats := ComputeHealthStats(events, base.Add(-time.Minute), base.Add(time.Hour), DefaultHumanFactors)
	want := HealthRuns{Started: 5, Completed: 2, Failed: 1, Cancelled: 1, RunningNow: 1}
	if stats.Runs != want {
		t.Fatalf("runs = %+v, want %+v", stats.Runs, want)
	}
}

func TestComputeHealthStatsProviderAttribution(t *testing.T) {
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	events := []Event{
		// Provider on started events never counts, completed events do, the
		// latest non-empty one wins — even when the slice order says otherwise.
		healthTestEvent("e1", "model.started", "r1", base, map[string]any{"turn": 1, "model": "gpt-5.2", "provider": "bogus"}),
		healthTestEvent("e2", "model.completed", "r1", base.Add(time.Second), map[string]any{"turn": 1, "model": "gpt-5.2"}),
		healthTestEvent("e3", "model.completed", "r1", base.Add(2*time.Second), map[string]any{"turn": 2, "model": "gpt-5.2", "provider": "openai"}),
		healthTestEvent("e4", "model.completed", "r1", base.Add(3*time.Second), map[string]any{"turn": 3, "model": "gpt-5.2", "provider": "anthropic"}),
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	stats := ComputeHealthStats(events, base.Add(-time.Minute), base.Add(time.Hour), DefaultHumanFactors)
	if len(stats.Models) != 1 || stats.Models[0].Provider != "anthropic" {
		t.Fatalf("models = %+v, want one gpt-5.2 with provider anthropic", stats.Models)
	}
}

func TestComputeHealthStatsTokenTolerance(t *testing.T) {
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	events := []Event{
		// JSON round-trips numbers as float64, native producers emit int and
		// int64; anything non-numeric is ignored rather than corrupting sums.
		healthTestEvent("e1", "model.completed", "r1", base, map[string]any{"turn": 1, "model": "gpt-5.2", "total_tokens": float64(1234), "cached_tokens": float64(200)}),
		healthTestEvent("e2", "model.completed", "r1", base.Add(time.Second), map[string]any{"turn": 2, "model": "gpt-5.2", "total_tokens": int(100)}),
		healthTestEvent("e3", "model.completed", "r1", base.Add(2*time.Second), map[string]any{"turn": 3, "model": "gpt-5.2", "total_tokens": int64(50)}),
		healthTestEvent("e4", "model.completed", "r1", base.Add(3*time.Second), map[string]any{"turn": 4, "model": "gpt-5.2", "total_tokens": "700"}),
	}
	stats := ComputeHealthStats(events, base.Add(-time.Minute), base.Add(time.Hour), DefaultHumanFactors)
	if len(stats.Models) != 1 {
		t.Fatalf("models = %+v, want exactly one", stats.Models)
	}
	model := stats.Models[0]
	if model.TotalTokens != 1384 {
		t.Fatalf("total_tokens = %d, want 1384 (1234+100+50, string ignored)", model.TotalTokens)
	}
	if model.CachedTokens != 200 {
		t.Fatalf("cached_tokens = %d, want 200 (absent counts as 0)", model.CachedTokens)
	}
}

func TestComputeHealthStatsModelOrdering(t *testing.T) {
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	events := []Event{
		healthTestEvent("e1", "model.started", "r1", base, map[string]any{"turn": 1, "model": "zeta"}),
		healthTestEvent("e2", "model.started", "r1", base.Add(time.Second), map[string]any{"turn": 2, "model": "alpha"}),
		healthTestEvent("e3", "model.started", "r1", base.Add(2*time.Second), map[string]any{"turn": 3, "model": "beta"}),
		healthTestEvent("e4", "model.started", "r1", base.Add(3*time.Second), map[string]any{"turn": 4, "model": "gamma"}),
		healthTestEvent("e5", "model.started", "r1", base.Add(4*time.Second), map[string]any{"turn": 5, "model": "zeta"}),
	}
	stats := ComputeHealthStats(events, base.Add(-time.Minute), base.Add(time.Hour), DefaultHumanFactors)
	if len(stats.Models) != 4 {
		t.Fatalf("models = %+v, want four", stats.Models)
	}
	if stats.Models[0].Model != "zeta" || stats.Models[1].Model != "alpha" || stats.Models[2].Model != "beta" {
		t.Fatalf("order = %s,%s,%s,%s; want calls desc (zeta first), then model asc for ties", stats.Models[0].Model, stats.Models[1].Model, stats.Models[2].Model, stats.Models[3].Model)
	}
	if stats.Totals.ModelCalls != 5 || stats.Totals.ModelFailures != 0 || stats.Totals.Nudged != 0 {
		t.Fatalf("totals = %+v, want model_calls 4 and zero failures/nudges", stats.Totals)
	}
}

func TestComputeHealthStatsEmptyWindow(t *testing.T) {
	since := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	stats := ComputeHealthStats(nil, since, now, DefaultHumanFactors)
	if !stats.Since.Equal(since) || !stats.GeneratedAt.Equal(now) {
		t.Fatalf("window fields = %v/%v, want %v/%v", stats.Since, stats.GeneratedAt, since, now)
	}
	if stats.Models == nil || len(stats.Models) != 0 {
		t.Fatalf("models = %#v, want empty non-nil slice", stats.Models)
	}
	if stats.Totals != (HealthTotals{}) || stats.Runs != (HealthRuns{}) || stats.Approvals != (HealthApprovals{}) {
		t.Fatalf("counters not zeroed: %+v %+v %+v", stats.Totals, stats.Runs, stats.Approvals)
	}
}

func TestComputeHealthStatsProviderFallback(t *testing.T) {
	// Legacy completed events carried provider but no model: their tokens and
	// latency must land on the provider key (kernel/cost pricing attribution),
	// not on an opaque "unknown" row.
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	events := []Event{
		healthTestEvent("e1", "model.started", "r1", base, map[string]any{"turn": 1, "model": "glm-5.3"}),
		healthTestEvent("e2", "model.completed", "r1", base.Add(time.Second), map[string]any{
			"turn": 1, "provider": "api.z.ai", "latency_ms": 1500, "total_tokens": 5000,
		}),
	}
	stats := ComputeHealthStats(events, base.Add(-time.Minute), base.Add(time.Minute), DefaultHumanFactors)
	var legacy *HealthModelStats
	for i := range stats.Models {
		if stats.Models[i].Model == "api.z.ai" {
			legacy = &stats.Models[i]
		}
		if stats.Models[i].Model == "unknown" {
			t.Fatalf("unknown row must stay empty for provider-only events: %+v", stats.Models[i])
		}
	}
	if legacy == nil {
		t.Fatalf("provider row missing in %v", stats.Models)
	}
	if legacy.Completed != 1 || legacy.TotalTokens != 5000 || legacy.P50LatencyMS != 1500 {
		t.Fatalf("provider row = %+v, want 1 completed / 5000 tokens / 1500ms", legacy)
	}
}

// HAB/HWT (docs/supervisor-model.md §5): attention estimates from resolved
// approvals, ask_human answers and generated volume; wait time from the
// approval queue; flow efficiency ties them together.
func TestComputeHealthStatsHumanFactors(t *testing.T) {
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	approval := func(id string, at time.Time, eventType string) Event {
		return Event{EventID: id + "/" + eventType, OccurredAt: at, Type: eventType,
			Context: Context{Run: "r-1"}, Data: map[string]any{"operation_id": id, "arguments_hash": "h"}}
	}
	events := []Event{
		approval("op-1", base, "approval.requested"),
		approval("op-1", base.Add(10*time.Minute), "approval.granted"), // 10 min queue wait
		{EventID: "answer/1", OccurredAt: base.Add(time.Minute), Type: "human.answered",
			Context: Context{Run: "r-1"}, Data: map[string]any{"response": "делай так"}},
		// 900 completion tokens of generated text (100 words of it is the final
		// answer read in full, the rest is skimmed at 10%).
		{EventID: "mc/1", OccurredAt: base, Type: "model.completed",
			Context: Context{Run: "r-1"}, Data: map[string]any{"model": "m", "total_tokens": 5000, "completion_tokens": 900}},
		{EventID: "summary/1", OccurredAt: base, Type: "agent.summary",
			Context: Context{Run: "r-1"}, Data: map[string]any{"answer": strings.Repeat("word ", 100)}},
	}
	factors := HumanFactors{ApprovalSeconds: 45, AnswerBaseSeconds: 20, ReadingWPM: 180}
	stats := ComputeHealthStats(events, base.Add(-time.Minute), base.Add(time.Hour), factors)

	// HAB: approval 45s + answer base 20s + typing 7 runes/5 → 1 word → 0s +
	// reading: answer 100 words + skim (900−133 tokens)*3/4*10% ≈ 57 words →
	// 157 words / 180 wpm → 52s. Total 117s. The old formula counted all 900
	// tokens (and even total_tokens with prompts) — hours of phantom reading.
	if stats.Human.HABSeconds != 117 {
		t.Errorf("HAB = %d, want 117", stats.Human.HABSeconds)
	}
	// HWT: one resolved approval waited 10 minutes.
	if stats.Human.HWTSeconds != 600 {
		t.Errorf("HWT = %d, want 600", stats.Human.HWTSeconds)
	}
	// Flow efficiency = 117 / (117 + 600) ≈ 16%.
	if stats.Human.FlowEfficiency != 16 {
		t.Errorf("flow efficiency = %d, want 16", stats.Human.FlowEfficiency)
	}
	if stats.Human.ApprovalDecisions != 1 || stats.Human.AskHumanAnswered != 1 {
		t.Errorf("decisions/answers = %d/%d, want 1/1", stats.Human.ApprovalDecisions, stats.Human.AskHumanAnswered)
	}
}
