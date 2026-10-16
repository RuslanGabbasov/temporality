package cost

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// TestAggregateProjectsOverview folds a mixed window: one finished run, one
// still running, a model call each, and a pending approval older than the
// window that must still be counted (it arrives from the 7-day lookback).
func TestAggregateProjectsOverview(t *testing.T) {
	cfg, _ := ParsePrices("m:1.00:2.00")
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	window := 24 * time.Hour

	mk := func(typ, project, run string, at time.Time, data map[string]any) observation.Event {
		return observation.Event{Type: typ, OccurredAt: at, Context: observation.Context{Project: project, Run: run}, Data: data}
	}
	events := []observation.Event{
		// Project omni: a completed root run with one model call.
		mk("run.started", "omni", "r1", now.Add(-2*time.Hour), map[string]any{"agent_id": "lead"}),
		mk("model.completed", "omni", "r1", now.Add(-90*time.Minute), map[string]any{"model": "m", "prompt_tokens": 1000, "cached_tokens": 200, "completion_tokens": 500, "total_tokens": 1500}),
		mk("run.completed", "omni", "r1", now.Add(-1*time.Hour), nil),
		// Project omni: a still-running follow-up.
		mk("run.started", "omni", "r2", now.Add(-30*time.Minute), map[string]any{"agent_id": "reviewer"}),
		// Project legacy: a failed run started before the window, failed inside it.
		mk("run.failed", "legacy", "r9", now.Add(-20*time.Minute), nil),
		// A pending approval requested 3 days ago (approval lookback, not window).
		mk("approval.requested", "omni", "r1", now.Add(-72*time.Hour), map[string]any{"operation_id": "op-1", "arguments_hash": "h1"}),
		// An answered approval must not stay pending.
		mk("approval.requested", "omni", "r1", now.Add(-80*time.Hour), map[string]any{"operation_id": "op-2", "arguments_hash": "h2"}),
		mk("approval.granted", "omni", "r1", now.Add(-79*time.Hour), map[string]any{"operation_id": "op-2", "arguments_hash": "h2"}),
		// A lifecycle event older than the window is ignored (defense in depth).
		mk("run.started", "old", "r0", now.Add(-48*time.Hour), nil),
	}

	rows := aggregateProjectsOverview(events, window, now, cfg)
	omni, ok := rows["omni"]
	if !ok {
		t.Fatalf("expected row for omni, got %v", rows)
	}
	if omni.RunsStarted != 2 || omni.RunsRunning != 1 || omni.RunsFailed != 0 {
		t.Fatalf("unexpected run counters: %+v", omni)
	}
	if omni.ModelCalls != 1 || omni.TotalTokens != 1500 {
		t.Fatalf("unexpected model counters: %+v", omni)
	}
	// 800 fresh + 200 cached(half) prompt, 500 completion: 0.8 + 0.1 + 1.0 = $1.90
	if omni.TotalCostUSD != 1.90 {
		t.Fatalf("expected $1.90, got %f", omni.TotalCostUSD)
	}
	if omni.PendingApprovals != 1 {
		t.Fatalf("expected 1 pending approval older than the window, got %d", omni.PendingApprovals)
	}
	if omni.LastActivityAt == nil || !omni.LastActivityAt.Equal(now.Add(-30*time.Minute)) {
		t.Fatalf("expected last activity at r2 start, got %+v", omni.LastActivityAt)
	}

	legacy, ok := rows["legacy"]
	if !ok {
		t.Fatalf("expected row for legacy, got %v", rows)
	}
	if legacy.RunsStarted != 0 || legacy.RunsRunning != 0 || legacy.RunsFailed != 1 {
		t.Fatalf("unexpected legacy counters: %+v", legacy)
	}
	if _, ok := rows["old"]; ok {
		t.Fatal("a run started before the window must not create a row")
	}
}

// TestAggregateProjectsOverviewRunStatuses checks the terminal race handling:
// a cancelled-then-completed run is not running, and failures count per event.
func TestAggregateProjectsOverviewRunStatuses(t *testing.T) {
	cfg, _ := ParsePrices("m:1.00:2.00")
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	window := time.Hour
	mk := func(typ, run string, at time.Time) observation.Event {
		return observation.Event{Type: typ, OccurredAt: at, Context: observation.Context{Project: "p", Run: run}}
	}
	events := []observation.Event{
		mk("run.started", "a", now.Add(-50*time.Minute)),
		mk("run.cancelled", "a", now.Add(-40*time.Minute)),
		mk("run.completed", "a", now.Add(-30*time.Minute)), // latest terminal wins
		mk("run.started", "b", now.Add(-20*time.Minute)),
		mk("run.failed", "b", now.Add(-10*time.Minute)),
		mk("run.failed", "b", now.Add(-5*time.Minute)), // retries emit another failure
	}
	rows := aggregateProjectsOverview(events, window, now, cfg)
	row := rows["p"]
	if row.RunsStarted != 2 || row.RunsRunning != 0 || row.RunsFailed != 2 {
		t.Fatalf("unexpected counters: %+v", row)
	}
}

// TestFilterOverviewRows covers visibility filtering, org-unit attachment and
// the response order: most recent activity first, nils last, id tiebreak.
func TestFilterOverviewRows(t *testing.T) {
	recent := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	older := recent.Add(-time.Hour)
	rows := map[string]*ProjectOverviewRow{
		"hidden": {Project: "hidden", LastActivityAt: &recent},
		"stale":  {Project: "stale"},
		"active": {Project: "active", LastActivityAt: &older},
		"fresh":  {Project: "fresh", LastActivityAt: &recent},
	}
	visible := map[string][]string{"stale": nil, "active": {}, "fresh": {"unit-research", "unit-eng"}}
	got := filterOverviewRows(rows, visible)
	if len(got) != 3 {
		t.Fatalf("expected 3 visible rows, got %d: %+v", len(got), got)
	}
	// fresh and active tie on activity with hidden removed; fresh is more
	// recent than active, stale (nil activity) is last.
	wantOrder := []string{"fresh", "active", "stale"}
	for i, id := range wantOrder {
		if got[i].Project != id {
			t.Fatalf("expected %s at position %d, got %+v", id, i, got[i])
		}
	}
	if len(got[0].OrgUnits) != 2 || got[0].OrgUnits[0] != "unit-research" {
		t.Fatalf("expected org units attached to fresh, got %+v", got[0].OrgUnits)
	}

	// A nil map is no restriction; an empty map restricts to nothing.
	all := filterOverviewRows(rows, nil)
	if len(all) != 4 {
		t.Fatalf("expected 4 rows without restriction, got %d", len(all))
	}
	if none := filterOverviewRows(rows, map[string][]string{}); len(none) != 0 {
		t.Fatalf("expected 0 rows for an empty visible map, got %d", len(none))
	}
}

// TestProjectsOverviewHandlerWindowValidation: the window parameter rejects
// unparsable, non-positive and over-cap values with 400; the 168h cap itself
// is accepted (past validation the journal call fails with 500 on the empty
// test URL, which is enough to prove it got past the parser).
func TestProjectsOverviewHandlerWindowValidation(t *testing.T) {
	cfg, _ := ParsePrices("m:1.00:2.00")
	api := NewCostAPI(cfg, "", "")
	for _, query := range []string{"window=abc", "window=0s", "window=-1h", "window=169h"} {
		req := httptest.NewRequest("GET", "/v1/agent/cost/projects-overview?"+query, nil)
		res := httptest.NewRecorder()
		api.ProjectsOverviewHandler(res, req)
		if res.Code != 400 {
			t.Fatalf("expected 400 for %s, got %d", query, res.Code)
		}
	}
	req := httptest.NewRequest("GET", "/v1/agent/cost/projects-overview?window=168h", nil)
	res := httptest.NewRecorder()
	api.ProjectsOverviewHandler(res, req)
	if res.Code == 400 {
		t.Fatal("the 168h cap itself must be accepted")
	}
}
