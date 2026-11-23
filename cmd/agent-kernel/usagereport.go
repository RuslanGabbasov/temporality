package main

// Automatic monthly usage reports (docs/usage-report.md): once an hour the
// loop checks which calendar months the journal already covers
// (usage.monthly_report events) and folds every still-uncovered closed month
// through the usage aggregates — one journal event per month carrying all
// six dimensions. The report rides the same price config and label resolvers
// as the live usage_report tool, so a published month never re-derives to
// different numbers. Before this loop the monthly report was a manual copy
// out of the UI.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/observation"
)

const (
	usageReportFirstDelay  = 10 * time.Minute
	usageReportEvery       = time.Hour
	usageReportRowLimit    = 50
	usageReportMaxBackfill = 6 // never chase more than half a year of gaps
	usageReportEventType   = "usage.monthly_report"
)

// StartUsageReportLoop runs the monthly usage report sweep.
func StartUsageReportLoop(ctx context.Context, log *slog.Logger, costAPI *cost.CostAPI, observationURL string, enqueue func(context.Context, observation.Event) error) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(usageReportFirstDelay):
		}
		ticker := time.NewTicker(usageReportEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runPassGuarded(log, "usage-report", ctx, func(pctx context.Context) {
					runUsageReportPass(pctx, log, costAPI, observationURL, enqueue)
				})
			}
		}
	}()
}

func runUsageReportPass(ctx context.Context, log *slog.Logger, costAPI *cost.CostAPI, observationURL string, enqueue func(context.Context, observation.Event) error) {
	now := time.Now().UTC()
	latest, covered, err := lastReportedUsageMonth(ctx, observationURL)
	if err != nil {
		log.Warn("usage report: watermark", "error", err)
		return
	}
	for _, month := range usageReportMonths(now, latest, covered) {
		if err := publishMonthlyUsageReport(ctx, costAPI, enqueue, month); err != nil {
			log.Warn("usage report: publish", "month", month.Format("2006-01"), "error", err)
			return // keep the watermark monotonic: retry the same month next pass
		}
		log.Info("usage report published", "month", month.Format("2006-01"))
	}
}

// usageReportMonths lists the closed calendar months still missing a report:
// everything after the latest published month up to (excluding) the current
// one — or just the previous month on a fresh install. History beyond the
// backfill cap is not chased; the live tool still folds any explicit window
// on demand.
func usageReportMonths(now, latest time.Time, covered bool) []time.Time {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	prevMonth := monthStart.AddDate(0, -1, 0)
	start := prevMonth
	if covered {
		start = latest.AddDate(0, 1, 0)
		if floor := prevMonth.AddDate(0, -(usageReportMaxBackfill - 1), 0); start.Before(floor) {
			start = floor
		}
	}
	var months []time.Time
	for month := start; month.Before(monthStart); month = month.AddDate(0, 1, 0) {
		months = append(months, month)
	}
	return months
}

// lastReportedUsageMonth reads the newest usage.monthly_report event's
// month ("2006-01"); covered=false when none exists yet.
func lastReportedUsageMonth(ctx context.Context, observationURL string) (time.Time, bool, error) {
	params := url.Values{"type": {usageReportEventType}, "order": {"desc"}, "limit": {"1"}}
	events, _, err := supervisorTypedEventPage(ctx, observationURL, os.Getenv("TEMPORALITY_API_TOKEN"), params)
	if err != nil {
		return time.Time{}, false, err
	}
	if len(events) == 0 {
		return time.Time{}, false, nil
	}
	month, _ := events[0].Data["month"].(string)
	parsed, err := time.Parse("2006-01", month)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("reported month %q: %w", month, err)
	}
	return parsed, true, nil
}

// publishMonthlyUsageReport folds one closed calendar month through every
// dimension and journals the report as one event. Deterministic event id —
// a retried pass cannot fork a month into two reports.
func publishMonthlyUsageReport(ctx context.Context, costAPI *cost.CostAPI, enqueue func(context.Context, observation.Event) error, month time.Time) error {
	monthKey := month.Format("2006-01")
	until := month.AddDate(0, 1, 0)
	rows := map[string][]cost.UsageRow{}
	var totals cost.UsageReport
	for i, dim := range cost.UsageDimensions {
		report, err := costAPI.UsageReport(ctx, cost.UsageQuery{
			GroupBy: dim,
			Months:  1,
			Since:   &month,
			Until:   &until,
			Limit:   usageReportRowLimit,
		})
		if err != nil {
			return fmt.Errorf("%s: %w", dim, err)
		}
		rows[dim] = report.Rows
		if i == 0 {
			totals = report // the grand total is dimension-independent
		}
	}
	data := map[string]any{
		"month":          monthKey,
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"total_tokens":   totals.Total.TotalTokens,
		"total_cost_usd": totals.Total.CostUSD,
		"hab_seconds":    totals.Total.HABSeconds,
		"hwt_seconds":    totals.Total.HWTSeconds,
		"rows":           rows,
	}
	return enqueue(ctx, observation.Event{
		Schema:     observation.Schema,
		EventID:    "usage-report/" + monthKey,
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: "supervisor", Type: "system"}},
		Type:       usageReportEventType,
		Data:       data,
	})
}

// registerMonthlyUsageReports serves GET /v1/agent/usage/monthly: the
// published monthly reports, newest first (the dashboard and the agents'
// «что было в прошлом месяце» question read the same events).
func registerMonthlyUsageReports(mux *http.ServeMux, gate *controlplane.Gate, observationURL string) {
	mux.HandleFunc("GET /v1/agent/usage/monthly", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		events, err := userStatsJournalPages(r.Context(), observationURL, os.Getenv("TEMPORALITY_API_TOKEN"), usageReportEventType, time.Time{}, "", 4)
		if err != nil {
			writeError(w, 502, fmt.Errorf("journal query: %w", err))
			return
		}
		reports := []map[string]any{}
		for i := range events {
			ev := &events[i]
			if ev.Type != usageReportEventType {
				continue
			}
			report := map[string]any{"month": ev.Data["month"], "occurred_at": ev.OccurredAt}
			for key, value := range ev.Data {
				report[key] = value
			}
			reports = append(reports, report)
		}
		writeJSON(w, 200, map[string]any{"reports": reports})
	})
}
