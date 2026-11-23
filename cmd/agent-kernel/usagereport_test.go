package main

import (
	"testing"
	"time"
)

// The month window math drives a journal watermark — the 2026-11 janitor
// lesson (52e2f973): template/format logic that CI cannot type-check gets a
// parse-level test.
func monthKey(t time.Time) string { return t.Format("2006-01") }

func TestUsageReportMonthsFreshInstall(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	months := usageReportMonths(now, time.Time{}, false)
	if len(months) != 1 || monthKey(months[0]) != "2026-10" {
		t.Fatalf("fresh install reports exactly the previous month, got %v", months)
	}
}

func TestUsageReportMonthsFollowsWatermark(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	latest := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	months := usageReportMonths(now, latest, true)
	if len(months) != 1 || monthKey(months[0]) != "2026-10" {
		// Latest is 2026-09 → next uncovered is 2026-10; the current month is
		// still open and never published.
		t.Fatalf("expected [2026-10], got %v", months)
	}
}

func TestUsageReportMonthsCurrentMonthOpen(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	latest := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	months := usageReportMonths(now, latest, true)
	if len(months) != 0 {
		t.Fatalf("previous month covered and current month open → nothing to publish, got %v", months)
	}
}

func TestUsageReportMonthsBackfillCap(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	latest := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) // ancient watermark
	months := usageReportMonths(now, latest, true)
	if len(months) != usageReportMaxBackfill {
		t.Fatalf("gap beyond the cap must clamp to %d months, got %d", usageReportMaxBackfill, len(months))
	}
	first := monthKey(months[0])
	if first != "2026-05" { // prevMonth (2026-10) minus 5
		t.Fatalf("expected the window to start at 2026-05, got %s", first)
	}
	if monthKey(months[len(months)-1]) != "2026-10" {
		t.Fatalf("expected the window to end at the previous month, got %v", months)
	}
}
