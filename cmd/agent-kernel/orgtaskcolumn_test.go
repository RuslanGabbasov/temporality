package main

import (
	"testing"

	"github.com/temporality-project/temporality/workspace"
)

// orgTaskColumn drives the board layout: failed work sits at the quality gate
// («Ревью») until a human verdict moves it — accepted fails present as
// completed and the activity window washes them out.
func TestOrgTaskColumnFailedVsAccepted(t *testing.T) {
	failed := []workspace.OrgActiveRun{
		{RunID: "r1", Status: "failed"},
	}
	completed := []workspace.OrgActiveRun{
		{RunID: "r2", Status: "completed"},
	}
	mixed := []workspace.OrgActiveRun{
		{RunID: "r1", Status: "failed"},
		{RunID: "r3", Status: "completed"},
	}

	if column, _ := orgTaskColumn(failed, nil, nil, false); column != orgColumnReview {
		t.Fatalf("failed work without a verdict must sit in review, got %q", column)
	}
	// The human verdict moves the reviewed card out of the gate — even when
	// nothing completed: the work was analyzed and accepted as-is.
	if column, _ := orgTaskColumn(failed, nil, nil, true); column != orgColumnCompleted {
		t.Fatalf("accepted failed work must present as completed, got %q", column)
	}
	// A live run outranks everything: real work in flight is «В работе».
	live := []workspace.OrgActiveRun{{RunID: "r4", Status: "running"}}
	if column, _ := orgTaskColumn(live, nil, nil, false); column != orgColumnInProgress {
		t.Fatalf("a live run must be in progress, got %q", column)
	}
	liveAndFailed := append(live, failed...)
	if column, _ := orgTaskColumn(liveAndFailed, nil, nil, false); column != orgColumnInProgress {
		t.Fatalf("a live run outranks failed history, got %q", column)
	}
	// Plain completed work was never in review at all.
	if column, _ := orgTaskColumn(completed, nil, nil, false); column != orgColumnCompleted {
		t.Fatalf("completed work must be completed, got %q", column)
	}
	// Failed history plus a fresh completed run: without the verdict the gate
	// still holds (the failed flag stays visible), with it — completed.
	if column, _ := orgTaskColumn(mixed, nil, nil, false); column != orgColumnReview {
		t.Fatalf("mixed failed+completed without a verdict must hold the gate, got %q", column)
	}
	if column, _ := orgTaskColumn(mixed, nil, nil, true); column != orgColumnCompleted {
		t.Fatalf("mixed failed+completed with a verdict must complete, got %q", column)
	}
}

// Cancelled runs are terminal outcomes: a task whose only run was cancelled
// used to fall through every bucket and land back in «В работе» forever —
// the operator saw dead work as live work. Cancelled counts as completed on
// the board; a failed sibling still holds the review gate, and a live run
// still outranks cancelled history (a restart is in progress).
func TestOrgTaskColumnCancelled(t *testing.T) {
	cancelled := []workspace.OrgActiveRun{
		{RunID: "r1", Status: "cancelled"},
	}
	cancelledAndFailed := []workspace.OrgActiveRun{
		{RunID: "r1", Status: "cancelled"},
		{RunID: "r2", Status: "failed"},
	}
	cancelledAndLive := []workspace.OrgActiveRun{
		{RunID: "r1", Status: "cancelled"},
		{RunID: "r3", Status: "running"},
	}

	if column, _ := orgTaskColumn(cancelled, nil, nil, false); column != orgColumnCompleted {
		t.Fatalf("a cancelled-only task must present as completed, got %q", column)
	}
	if column, _ := orgTaskColumn(cancelledAndFailed, nil, nil, false); column != orgColumnReview {
		t.Fatalf("a cancelled run must not wash a failure out of the gate, got %q", column)
	}
	if column, _ := orgTaskColumn(cancelledAndLive, nil, nil, false); column != orgColumnInProgress {
		t.Fatalf("a live run must outrank cancelled history, got %q", column)
	}
}
