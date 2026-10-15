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

func TestAggregateTaskApprovals(t *testing.T) {
	cfg, _ := ParsePrices("m:1.00:2.00")
	api := NewCostAPI(cfg, "", "")
	base := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	mk := func(typ, run, task string, at time.Time, data map[string]any) observation.Event {
		return observation.Event{Type: typ, OccurredAt: at, Context: observation.Context{Run: run, Task: task}, Data: data}
	}
	events := []observation.Event{
		mk("run.started", "r1", "t1", base, map[string]any{"agent_id": "lead", "title": "API migration"}),
		mk("approval.requested", "r1", "t1", base.Add(1*time.Minute), map[string]any{"operation_id": "op-1", "arguments_hash": "h1"}),
		mk("approval.granted", "r1", "t1", base.Add(6*time.Minute), map[string]any{"operation_id": "op-1", "arguments_hash": "h1"}),
		mk("approval.requested", "r1", "t1", base.Add(10*time.Minute), map[string]any{"operation_id": "op-2", "arguments_hash": "h2"}),
		mk("approval.requested", "r1", "t1", base.Add(20*time.Minute), map[string]any{"operation_id": "op-3", "arguments_hash": "h3"}),
		mk("approval.rejected", "r1", "t1", base.Add(23*time.Minute), map[string]any{"operation_id": "op-3", "arguments_hash": "h3"}),
		mk("approval.auto_granted", "r1", "t1", base.Add(25*time.Minute), nil),
		mk("approval.requested", "r1", "t1", base.Add(30*time.Minute), map[string]any{"operation_id": "op-4", "arguments_hash": "h4"}),
		mk("approval.timed_out", "r1", "t1", base.Add(50*time.Minute), map[string]any{"operation_id": "op-4", "arguments_hash": "h4"}),
	}

	got := api.aggregateTask("p", "t1", events)
	ap := got.Approvals
	if ap.Requested != 4 || ap.Granted != 1 || ap.Rejected != 1 || ap.TimedOut != 1 || ap.AutoGranted != 1 {
		t.Fatalf("unexpected approval counts: %+v", ap)
	}
	if ap.Pending != 1 {
		t.Fatalf("expected 1 pending approval (op-2), got %d", ap.Pending)
	}
	// granted 5m + rejected 3m + timed out 20m = 28m total, max 20m
	if ap.WaitMS != 28*time.Minute.Milliseconds() {
		t.Fatalf("expected 28m total wait, got %dms", ap.WaitMS)
	}
	if ap.MaxWaitMS != 20*time.Minute.Milliseconds() {
		t.Fatalf("expected max wait 20m, got %dms", ap.MaxWaitMS)
	}
}

func TestAggregateProjectBudget(t *testing.T) {
	cfg, _ := ParsePrices("m:1.00:2.00")
	api := NewCostAPI(cfg, "", "")
	base := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	api.now = func() time.Time { return base.Add(3 * time.Hour) }
	mk := func(typ, run, task string, at time.Time, data map[string]any) observation.Event {
		return observation.Event{Type: typ, OccurredAt: at, Context: observation.Context{Run: run, Task: task}, Data: data}
	}
	events := []observation.Event{
		// Saga A: root with title + delegated child, both finished.
		mk("run.started", "a1", "A", base, map[string]any{"agent_id": "lead", "title": "API migration"}),
		mk("model.completed", "a1", "A", base.Add(1*time.Minute), map[string]any{"model": "m", "prompt_tokens": 1000, "completion_tokens": 400}),
		mk("run.started", "a1/plan/01", "A", base.Add(2*time.Minute), map[string]any{"agent_id": "dev", "parent_run_id": "a1"}),
		mk("model.completed", "a1/plan/01", "A", base.Add(3*time.Minute), map[string]any{"model": "m", "prompt_tokens": 500, "completion_tokens": 100}),
		mk("approval.requested", "a1/plan/01", "A", base.Add(4*time.Minute), map[string]any{"operation_id": "op", "arguments_hash": "h"}),
		mk("run.completed", "a1/plan/01", "A", base.Add(5*time.Minute), nil),
		mk("approval.granted", "a1/plan/01", "A", base.Add(9*time.Minute), map[string]any{"operation_id": "op", "arguments_hash": "h"}),
		mk("run.completed", "a1", "A", base.Add(10*time.Minute), nil),
		// Saga B: newer, still running, one pending approval.
		mk("run.started", "b1", "B", base.Add(1*time.Hour), map[string]any{"agent_id": "lead", "title": "Docs refresh"}),
		mk("model.completed", "b1", "B", base.Add(61*time.Minute), map[string]any{"model": "m", "prompt_tokens": 200, "completion_tokens": 50}),
		mk("approval.requested", "b1", "B", base.Add(62*time.Minute), map[string]any{"operation_id": "op", "arguments_hash": "h"}),
		// Taskless event: must not create a phantom saga.
		{Type: "model.completed", OccurredAt: base.Add(90 * time.Minute), Context: observation.Context{Run: "loose"}, Data: map[string]any{"model": "m", "prompt_tokens": 999, "completion_tokens": 999}},
	}

	got := api.aggregateProjectBudget("p", events)
	if got.SagaCount != 0 || got.TaskCount != 2 || got.RunCount != 3 || got.ModelCalls != 3 {
		t.Fatalf("unexpected top-level counts: %+v", got)
	}
	if got.TotalTokens != 2250 || got.TotalCostUSD != 2.80 {
		t.Fatalf("unexpected totals: tokens=%d cost=%f", got.TotalTokens, got.TotalCostUSD)
	}
	if got.Approvals.Requested != 2 || got.Approvals.Granted != 1 || got.Approvals.Pending != 1 {
		t.Fatalf("unexpected approval totals: %+v", got.Approvals)
	}
	if got.Approvals.WaitMS != 5*time.Minute.Milliseconds() {
		t.Fatalf("expected 5m approval wait, got %dms", got.Approvals.WaitMS)
	}
	// Without explicit saga events every task is unbound: sagas stay empty,
	// tasks carry the same rows the old saga list carried.
	if len(got.Sagas) != 0 || len(got.Tasks) != 2 {
		t.Fatalf("expected 0 sagas and 2 tasks, got %d/%d", len(got.Sagas), len(got.Tasks))
	}
	// Newest task first.
	b := got.Tasks[0]
	if b.TaskID != "B" || b.Title != "Docs refresh" || b.RootRunID != "b1" || b.SagaID != "" {
		t.Fatalf("unexpected task B: %+v", b)
	}
	if b.Running != 1 || b.LastEndedAt != nil {
		t.Fatalf("task B must be running: %+v", b)
	}
	a := got.Tasks[1]
	if a.TaskID != "A" || a.Title != "API migration" || a.RootRunID != "a1" || a.SagaID != "" {
		t.Fatalf("unexpected task A: %+v", a)
	}
	if a.RunCount != 2 || a.AgentCount != 2 || a.Running != 0 {
		t.Fatalf("unexpected task A counts: %+v", a)
	}
	if a.Approvals.Granted != 1 || a.Approvals.WaitMS != 5*time.Minute.Milliseconds() {
		t.Fatalf("unexpected task A approvals: %+v", a.Approvals)
	}
}

func TestAggregateProjectBudgetSagas(t *testing.T) {
	cfg, _ := ParsePrices("m:1.00:2.00")
	api := NewCostAPI(cfg, "", "")
	base := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	api.now = func() time.Time { return base.Add(2 * time.Hour) }
	mk := func(typ, run, task string, at time.Time, data map[string]any) observation.Event {
		return observation.Event{Type: typ, OccurredAt: at, Context: observation.Context{Run: run, Task: task}, Data: data}
	}
	events := []observation.Event{
		// Analyst's task registers a saga before any spend; the binding event
		// may also arrive after the task's events (out-of-order journals).
		mk("run.started", "r1", "T1", base, map[string]any{"agent_id": "analyst", "title": "Analyst research"}),
		mk("model.completed", "r1", "T1", base.Add(1*time.Minute), map[string]any{"model": "m", "prompt_tokens": 100, "completion_tokens": 20}),
		mk("run.completed", "r1", "T1", base.Add(2*time.Minute), nil),
		// Second task of the same saga (the lead's takeover): saga_attach.
		mk("run.started", "r2", "T2", base.Add(30*time.Minute), map[string]any{"agent_id": "lead", "title": "Lead takeover"}),
		mk("model.completed", "r2", "T2", base.Add(31*time.Minute), map[string]any{"model": "m", "prompt_tokens": 200, "completion_tokens": 40}),
		// Unbound task: rolls up on its own, no saga row.
		mk("run.started", "r3", "T3", base.Add(45*time.Minute), map[string]any{"agent_id": "dev", "title": "Unrelated fix"}),
		mk("model.completed", "r3", "T3", base.Add(46*time.Minute), map[string]any{"model": "m", "prompt_tokens": 50, "completion_tokens": 10}),
		// Saga events arrive last on purpose: bindings must be order-independent.
		mk("saga.registered", "r1", "T1", base.Add(3*time.Minute), map[string]any{"saga_id": "saga-research-abc123", "title": "Competitor research", "goal": "deliver a report"}),
		mk("saga.attached", "r2", "T2", base.Add(32*time.Minute), map[string]any{"saga_id": "saga-research-abc123", "note": "takeover"}),
	}

	got := api.aggregateProjectBudget("p", events)
	if got.SagaCount != 1 || got.TaskCount != 3 || got.RunCount != 3 {
		t.Fatalf("unexpected counts: saga=%d tasks=%d runs=%d", got.SagaCount, got.TaskCount, got.RunCount)
	}
	if len(got.Sagas) != 1 || len(got.Tasks) != 3 {
		t.Fatalf("expected 1 saga row and 3 task rows, got %d/%d", len(got.Sagas), len(got.Tasks))
	}
	saga := got.Sagas[0]
	if saga.SagaID != "saga-research-abc123" || saga.Title != "Competitor research" || saga.Goal != "deliver a report" {
		t.Fatalf("unexpected saga meta: %+v", saga)
	}
	if len(saga.TaskIDs) != 2 || saga.TaskIDs[0] != "T1" || saga.TaskIDs[1] != "T2" {
		t.Fatalf("saga must bind T1+T2 in task order, got %v", saga.TaskIDs)
	}
	// Both tasks' spend sums into the one saga row.
	if saga.RunCount != 2 || saga.TotalTokens != (100+20)+(200+40) {
		t.Fatalf("unexpected saga totals: %+v", saga)
	}
	if saga.RootRunID != "r1" || saga.AgentCount != 2 {
		t.Fatalf("saga root/agents: %+v", saga)
	}
	// Tasks carry their binding; the unbound task has none.
	bound := 0
	for _, task := range got.Tasks {
		if task.TaskID == "T1" || task.TaskID == "T2" {
			bound++
			if task.SagaID != "saga-research-abc123" {
				t.Fatalf("task %s must be bound, got %q", task.TaskID, task.SagaID)
			}
		}
		if task.TaskID == "T3" && task.SagaID != "" {
			t.Fatalf("task T3 must stay unbound, got %q", task.SagaID)
		}
	}
	if bound != 2 {
		t.Fatalf("expected both saga tasks bound, got %d", bound)
	}
	// Project totals still cover every task, bound or not.
	if got.TotalTokens != (100+20)+(200+40)+(50+10) {
		t.Fatalf("unexpected project totals: %d", got.TotalTokens)
	}
}

func TestSagaRebindLastWins(t *testing.T) {
	cfg, _ := ParsePrices("")
	api := NewCostAPI(cfg, "", "")
	base := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	api.now = func() time.Time { return base.Add(time.Hour) }
	mk := func(typ, run, task string, at time.Time, data map[string]any) observation.Event {
		return observation.Event{Type: typ, OccurredAt: at, Context: observation.Context{Run: run, Task: task}, Data: data}
	}
	events := []observation.Event{
		mk("run.started", "r1", "T", base, map[string]any{"agent_id": "lead", "title": "Work"}),
		mk("model.completed", "r1", "T", base.Add(time.Minute), map[string]any{"model": "m", "prompt_tokens": 10, "completion_tokens": 5}),
		mk("saga.registered", "r1", "T", base.Add(2*time.Minute), map[string]any{"saga_id": "saga-one-aaaaaa", "title": "One"}),
		// Later re-bind to another saga: the spend follows the latest binding,
		// the abandoned saga keeps its registration row with zero tasks.
		mk("saga.registered", "r9", "T9", base.Add(3*time.Minute), map[string]any{"saga_id": "saga-two-bbbbbb", "title": "Two"}),
		mk("saga.attached", "r1", "T", base.Add(4*time.Minute), map[string]any{"saga_id": "saga-two-bbbbbb"}),
	}
	got := api.aggregateProjectBudget("p", events)
	if got.SagaCount != 2 || got.TaskCount != 1 {
		t.Fatalf("unexpected counts: %+v", got)
	}
	task := got.Tasks[0]
	if task.SagaID != "saga-two-bbbbbb" {
		t.Fatalf("latest binding must win, got %q", task.SagaID)
	}
	for _, saga := range got.Sagas {
		if saga.SagaID == "saga-one-aaaaaa" && (len(saga.TaskIDs) != 0 || saga.TotalTokens != 0) {
			t.Fatalf("abandoned saga must keep zero totals, got %+v", saga)
		}
		if saga.SagaID == "saga-two-bbbbbb" && (saga.TotalTokens != 15 || len(saga.TaskIDs) != 1) {
			t.Fatalf("re-bound saga must own the spend, got %+v", saga)
		}
	}
}

func TestAggregateTaskEmpty(t *testing.T) {
	api := NewCostAPI(Config{}, "", "")
	got := api.aggregateTask("p", "missing", nil)
	if got.RunCount != 0 || got.TotalTokens != 0 || got.ElapsedMS != 0 {
		t.Fatalf("expected zeroed task cost, got %+v", got)
	}
}
