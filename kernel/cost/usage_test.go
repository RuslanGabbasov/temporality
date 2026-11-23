package cost

import (
	"context"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// usageFold builds a CostAPI with priced config, fixed clock and resolvers
// for the fold tests: the fold must stay pure enough to drive directly.
func usageFold(t *testing.T, prices string, resolvers UsageResolvers) *CostAPI {
	t.Helper()
	cfg, err := ParsePrices(prices)
	if err != nil {
		t.Fatal(err)
	}
	api := NewCostAPI(cfg, "", "")
	api.SetUsageResolvers(resolvers)
	return api
}

func usageEvent(at time.Time, eventType, project, runID, taskID, actor string, data map[string]any) observation.Event {
	return observation.Event{
		Schema:     observation.Schema,
		EventID:    eventType + "/" + runID + "/" + data["op"].(string) + at.Format(time.RFC3339Nano),
		OccurredAt: at,
		Source:     observation.Source{ID: "test", Integration: "test"},
		Context:    observation.Context{Project: project, Run: runID, Task: taskID, Actor: observation.Actor{ID: actor, Type: "human"}},
		Type:       eventType,
		Data:       data,
	}
}

// foldWindow folds a fixed window for the tests: 2026-11 month to date,
// now pinned at 2026-11-23T12:00:00Z.
var (
	usageNow   = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	usageStart = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	usageUntil = usageNow
)

func TestUsageWindowDefaultsToMonthToDate(t *testing.T) {
	since, until := usageWindow(UsageQuery{}, usageNow)
	if !since.Equal(usageStart) || !until.Equal(usageNow) {
		t.Fatalf("expected [%s..%s], got [%s..%s]", usageStart, usageNow, since, until)
	}
	since, until = usageWindow(UsageQuery{Months: 3}, usageNow)
	wantStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if !since.Equal(wantStart) || !until.Equal(usageNow) {
		t.Fatalf("months=3: expected [%s..%s], got [%s..%s]", wantStart, usageNow, since, until)
	}
	since, _ = usageWindow(UsageQuery{Since: &usageStart, Until: &usageUntil}, usageNow)
	if !since.Equal(usageStart) {
		t.Fatalf("explicit since must win, got %s", since)
	}
}

func TestUsageWindowRejectsSinceAfterUntil(t *testing.T) {
	since := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	gotSince, gotUntil := usageWindow(UsageQuery{Since: &since, Until: &until}, usageNow)
	if !gotUntil.After(gotSince) {
		t.Fatalf("until %s must never land before since %s", gotUntil, gotSince)
	}
}

// TestUsageFoldHumanWait pins the HWT wait math and the HAB decision cost:
// one requested→granted pair 90s apart → HWT 90s, decisions 1 → HAB includes
// the 45s decision seconds (DefaultHumanFactors).
func TestUsageFoldHumanWait(t *testing.T) {
	api := usageFold(t, "gpt-4o:2.50:10.00", UsageResolvers{})
	requested := usageEvent(usageStart.Add(time.Hour), "approval.requested", "p", "run-1", "task-1", "alice", map[string]any{"op": "x", "operation_id": "op1", "arguments_hash": "h"})
	granted := usageEvent(usageStart.Add(time.Hour).Add(90*time.Second), "approval.granted", "p", "run-1", "task-1", "alice", map[string]any{"op": "x", "operation_id": "op1", "arguments_hash": "h"})
	report := api.foldUsage(context.Background(),
		UsageQuery{GroupBy: UsageGroupEmployee}, usageStart, usageUntil, usageNow,
		[]observation.Event{requested, granted}, nil)
	row, ok := usageRowByKey(report, "alice")
	if !ok {
		t.Fatalf("expected an alice row, got %+v", report.Rows)
	}
	if row.HWTSeconds != 90 {
		t.Fatalf("expected 90s HWT, got %d", row.HWTSeconds)
	}
	if row.Decisions != 1 {
		t.Fatalf("expected 1 decision, got %d", row.Decisions)
	}
	if row.HABSeconds < 45 {
		t.Fatalf("HAB must include the 45s decision cost, got %d", row.HABSeconds)
	}
	if report.Total.HWTSeconds != 90 {
		t.Fatalf("total HWT expected 90, got %d", report.Total.HWTSeconds)
	}
}

// TestUsageFoldTokensAndCost pins attribution of a model call to the run
// starter and the price config: alice starts the run, bob answers a question
// in the same run — the tokens stay alice's.
func TestUsageFoldTokensAndCost(t *testing.T) {
	api := usageFold(t, "gpt-4o:2.50:10.00", UsageResolvers{})
	events := []observation.Event{
		usageEvent(usageStart.Add(time.Minute), "run.started", "p", "run-1", "task-1", "alice", map[string]any{"op": "x"}),
		usageEvent(usageStart.Add(2 * time.Minute), "model.completed", "p", "run-1", "task-1", "alice", map[string]any{"op": "x", "model": "gpt-4o", "provider": "openai", "prompt_tokens": 1000000, "cached_tokens": 0, "completion_tokens": 500000}),
		usageEvent(usageStart.Add(3 * time.Minute), "human.answered", "p", "run-1", "task-1", "bob", map[string]any{"op": "x", "response": "да"}),
	}
	report := api.foldUsage(context.Background(),
		UsageQuery{GroupBy: UsageGroupEmployee}, usageStart, usageUntil, usageNow,
		events, nil)
	row, ok := usageRowByKey(report, "alice")
	if !ok {
		t.Fatalf("expected alice to own the run's tokens, rows: %+v", report.Rows)
	}
	if row.ModelCalls != 1 || row.PromptTokens != 1000000 || row.CompletionTokens != 500000 || row.TotalTokens != 1500000 {
		t.Fatalf("unexpected token totals: %+v", row)
	}
	wantCost(t, "alice cost", row.CostUSD, 7.5)
	if row.Runs != 1 {
		t.Fatalf("expected 1 run, got %d", row.Runs)
	}
	if row.Answers != 1 {
		// The answer rides the run: attributed to the same slice, decisions/answers stay visible.
		t.Fatalf("expected the run's answer to be counted, got %+v", row)
	}
	if _, ok := usageRowByKey(report, "bob"); ok {
		t.Fatal("the answering human must not fork the run's tokens into their own row")
	}
	if report.Total.CostUSD != 7.5 || report.Total.HWTSeconds != 0 {
		t.Fatalf("unexpected total: %+v", report.Total)
	}
}

// TestUsageFoldSagaDimension pins the task→saga attribution: a bound task's
// runs land on the saga row labeled by title, an unbound run lands on
// "unbound" — one fold, both buckets.
func TestUsageFoldSagaDimension(t *testing.T) {
	api := usageFold(t, "gpt-4o:2.50:10.00", UsageResolvers{})
	sagaEvents := []observation.Event{
		usageEvent(usageStart.Add(-72*time.Hour), "saga.registered", "p", "", "", "alice", map[string]any{"op": "x", "saga_id": "s1", "title": "Миграция биллинга"}),
		usageEvent(usageStart.Add(-71*time.Hour), "saga.attached", "p", "", "task-1", "alice", map[string]any{"op": "x", "saga_id": "s1", "task_id": "task-1"}),
	}
	events := []observation.Event{
		usageEvent(usageStart.Add(time.Minute), "run.started", "p", "run-1", "task-1", "alice", map[string]any{"op": "x"}),
		usageEvent(usageStart.Add(2 * time.Minute), "model.completed", "p", "run-1", "task-1", "alice", map[string]any{"op": "x", "model": "gpt-4o", "prompt_tokens": 1000, "completion_tokens": 100}),
		usageEvent(usageStart.Add(3 * time.Minute), "run.started", "p", "run-2", "task-2", "bob", map[string]any{"op": "x"}),
		usageEvent(usageStart.Add(4 * time.Minute), "model.completed", "p", "run-2", "task-2", "bob", map[string]any{"op": "x", "model": "gpt-4o", "prompt_tokens": 2000, "completion_tokens": 200}),
	}
	report := api.foldUsage(context.Background(),
		UsageQuery{GroupBy: UsageGroupSaga}, usageStart, usageUntil, usageNow,
		events, sagaEvents)
	s1, ok := usageRowByKey(report, "s1")
	if !ok {
		t.Fatalf("expected the bound saga row, got %+v", report.Rows)
	}
	if s1.Label != "Миграция биллинга" {
		t.Fatalf("expected the saga title as label, got %q", s1.Label)
	}
	if s1.ModelCalls != 1 || s1.PromptTokens != 1000 {
		t.Fatalf("unexpected saga totals: %+v", s1)
	}
	unbound, ok := usageRowByKey(report, "unbound")
	if !ok {
		t.Fatalf("expected the unbound bucket, got %+v", report.Rows)
	}
	if unbound.ModelCalls != 1 || unbound.PromptTokens != 2000 {
		t.Fatalf("unexpected unbound totals: %+v", unbound)
	}
}

// TestUsageFoldModelDimension pins the call-level cut: per-model rows carry
// tokens and cost, but no HAB (a model call has no approval of its own) —
// while Total keeps the window's human attention.
func TestUsageFoldModelDimension(t *testing.T) {
	api := usageFold(t, "cheap:1.00:2.00,expensive:10.00:20.00", UsageResolvers{})
	events := []observation.Event{
		usageEvent(usageStart.Add(time.Minute), "run.started", "p", "run-1", "task-1", "alice", map[string]any{"op": "x"}),
		usageEvent(usageStart.Add(2 * time.Minute), "model.completed", "p", "run-1", "task-1", "alice", map[string]any{"op": "x", "model": "cheap", "prompt_tokens": 1000000, "completion_tokens": 1000000}),
		usageEvent(usageStart.Add(3 * time.Minute), "model.completed", "p", "run-1", "task-1", "alice", map[string]any{"op": "x", "model": "expensive", "prompt_tokens": 1000000, "completion_tokens": 1000000}),
		usageEvent(usageStart.Add(4 * time.Minute), "approval.granted", "p", "run-1", "task-1", "alice", map[string]any{"op": "x", "operation_id": "op1", "arguments_hash": "h"}),
	}
	report := api.foldUsage(context.Background(),
		UsageQuery{GroupBy: UsageGroupModel}, usageStart, usageUntil, usageNow,
		events, nil)
	if len(report.Rows) != 2 {
		t.Fatalf("expected 2 model rows, got %+v", report.Rows)
	}
	if report.Rows[0].Key != "expensive" || report.Rows[0].CostUSD != 30 {
		t.Fatalf("expected expensive first at $30, got %+v", report.Rows[0])
	}
	if report.Rows[1].Key != "cheap" || report.Rows[1].CostUSD != 3 {
		t.Fatalf("expected cheap second at $3, got %+v", report.Rows[1])
	}
	for _, row := range report.Rows {
		if row.HABSeconds != 0 || row.HWTSeconds != 0 || row.Decisions != 0 {
			t.Fatalf("model rows must not carry human attention: %+v", row)
		}
		if row.Runs != 1 {
			t.Fatalf("expected the call's run counted, got %d", row.Runs)
		}
	}
	if report.Total.HABSeconds < 45 || report.Total.CostUSD != 33 {
		t.Fatalf("total must keep the human attention and both calls: %+v", report.Total)
	}
}

// TestUsageFoldMonthWindow pins the window: last month's event is excluded,
// this month's included, Until cuts mid-month.
func TestUsageFoldMonthWindow(t *testing.T) {
	api := usageFold(t, "gpt-4o:2.50:10.00", UsageResolvers{})
	lastMonth := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	thisMonth := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	events := []observation.Event{
		usageEvent(lastMonth, "model.completed", "p", "run-old", "task-old", "alice", map[string]any{"op": "x", "model": "gpt-4o", "prompt_tokens": 1000, "completion_tokens": 100}),
		usageEvent(thisMonth, "model.completed", "p", "run-new", "task-new", "alice", map[string]any{"op": "x", "model": "gpt-4o", "prompt_tokens": 1000, "completion_tokens": 100}),
	}
	report := api.foldUsage(context.Background(),
		UsageQuery{GroupBy: UsageGroupEmployee}, usageStart, usageUntil, usageNow,
		events, nil)
	row, _ := usageRowByKey(report, "alice")
	if row.ModelCalls != 1 || row.PromptTokens != 1000 {
		t.Fatalf("the previous month must not leak into the month-to-date fold: %+v", row)
	}
	if row.Runs != 1 {
		t.Fatalf("expected only the window's run counted, got %d", row.Runs)
	}
	until := thisMonth
	report = api.foldUsage(context.Background(),
		UsageQuery{GroupBy: UsageGroupEmployee, Until: &until}, thisMonth, until, usageNow,
		[]observation.Event{usageEvent(thisMonth.Add(time.Hour), "model.completed", "p", "run-late", "task-late", "alice", map[string]any{"op": "x", "model": "gpt-4o", "prompt_tokens": 5, "completion_tokens": 5})}, nil)
	row, _ = usageRowByKey(report, "alice")
	if row.ModelCalls != 0 {
		t.Fatalf("until must exclude later events, got %+v", row)
	}
}

// TestUsageFoldResolverLabels pins the label path: resolver names win,
// unknown ids degrade to prefixed raw keys — never a lost row.
func TestUsageFoldResolverLabels(t *testing.T) {
	resolvers := UsageResolvers{
		UserName: func(ctx context.Context, userID string) (string, error) {
			if userID == "u-alice" {
				return "Алиса", nil
			}
			return "", context.DeadlineExceeded
		},
		OrgNodeName: func(ctx context.Context, orgNodeID string) (string, error) {
			if orgNodeID == "n-qa" {
				return "QA", nil
			}
			return "", context.DeadlineExceeded
		},
		OrgNodeForRun: func(ctx context.Context, project, runID string) (string, error) {
			if runID == "run-1" {
				return "n-qa", nil
			}
			return "", nil
		},
	}
	api := usageFold(t, "gpt-4o:2.50:10.00", resolvers)
	events := []observation.Event{
		usageEvent(usageStart.Add(time.Minute), "run.started", "p", "run-1", "task-1", "u-alice", map[string]any{"op": "x"}),
		usageEvent(usageStart.Add(2 * time.Minute), "model.completed", "p", "run-1", "task-1", "u-alice", map[string]any{"op": "x", "model": "gpt-4o", "prompt_tokens": 10, "completion_tokens": 10}),
		usageEvent(usageStart.Add(3 * time.Minute), "run.started", "p", "run-2", "task-2", "u-ghost", map[string]any{"op": "x"}),
		usageEvent(usageStart.Add(4 * time.Minute), "model.completed", "p", "run-2", "task-2", "u-ghost", map[string]any{"op": "x", "model": "gpt-4o", "prompt_tokens": 10, "completion_tokens": 10}),
	}
	employee := api.foldUsage(context.Background(),
		UsageQuery{GroupBy: UsageGroupEmployee}, usageStart, usageUntil, usageNow, events, nil)
	if row, ok := usageRowByKey(employee, "u-alice"); !ok || row.Label != "Алиса" {
		t.Fatalf("expected Алиса, got %+v", row)
	}
	if row, ok := usageRowByKey(employee, "u-ghost"); !ok || row.Label != "user:u-ghost" {
		t.Fatalf("resolver failure must degrade to the prefixed key, got %+v", row)
	}
	org := api.foldUsage(context.Background(),
		UsageQuery{GroupBy: UsageGroupOrgUnit}, usageStart, usageUntil, usageNow, events, nil)
	if row, ok := usageRowByKey(org, "n-qa"); !ok || row.Label != "QA" || row.ModelCalls != 1 {
		t.Fatalf("expected the QA row, got %+v", row)
	}
	if row, ok := usageRowByKey(org, ""); !ok || row.Label != "unattributed" || row.ModelCalls != 1 {
		t.Fatalf("expected the unattributed bucket, got %+v", row)
	}
}

func usageRowByKey(report UsageReport, key string) (UsageRow, bool) {
	for _, row := range report.Rows {
		if row.Key == key {
			return row, true
		}
	}
	return UsageRow{}, false
}
