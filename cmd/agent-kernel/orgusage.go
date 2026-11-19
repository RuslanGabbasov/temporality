package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/workspace"
)

// Monthly org usage accounting (docs/org-structure.md §24, policy monthly
// caps): a watermarked sweep reads model.completed events month-to-date,
// attributes every call to the run's org position (the same attribution the
// boards use), prices it with the kernel's cost config and accumulates
// org_month_usage — one row per org node per calendar month. The policy
// admission gate and the usage stats view read the same rows: the number
// that LIMITS a unit is the number the unit SEES.

const (
	orgUsageFirstDelay  = 10 * time.Minute
	orgUsageEvery       = time.Minute
	orgUsageMaxPages    = 20
	orgUsagePage        = 500
	// Cost computation rides the kernel's price config: the same numbers the
	// dashboard shows.
)

// StartOrgUsageLoop runs the monthly usage sweep.
func StartOrgUsageLoop(ctx context.Context, log *slog.Logger, ws *workspace.Store, observationURL, journalToken string, costConfig cost.Config) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(orgUsageFirstDelay):
		}
		ticker := time.NewTicker(orgUsageEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runPassGuarded(log, "org-usage", ctx, func(pctx context.Context) {
					runOrgUsagePass(pctx, log, ws, observationURL, journalToken, costConfig)
				})
			}
		}
	}()
}

func runOrgUsagePass(ctx context.Context, log *slog.Logger, ws *workspace.Store, observationURL, journalToken string, costConfig cost.Config) {
	watermark, err := ws.OrgUsageWatermark(ctx)
	if err != nil {
		log.Warn("org usage: watermark", "error", err)
		return
	}
	since := watermark
	if since.IsZero() {
		// First run on a fresh install: fold the current month only — older
		// months are history the stats view can live without.
		now := time.Now().UTC()
		since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}

	params := url.Values{
		"type":  {"model.completed"},
		"order": {"asc"},
		"since": {since.Format(time.RFC3339Nano)},
		"limit": {fmt.Sprint(orgUsagePage)},
	}
	cursor := ""
	newest := watermark
	folded := 0
	runNode := map[string]string{} // project\x00run → org node, cached per pass
	for page := 0; page < orgUsageMaxPages; page++ {
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		events, next, err := supervisorTypedEventPage(ctx, observationURL, journalToken, params)
		if err != nil {
			log.Warn("org usage: journal page", "error", err)
			return
		}
		for i := range events {
			ev := events[i]
			project := ev.Context.Project
			runID := ev.Context.Run
			if project == "" || runID == "" {
				continue
			}
			cacheKey := project + "\x00" + runID
			node, ok := runNode[cacheKey]
			if !ok {
				node, err = ws.OrgNodeForRun(ctx, project, runID)
				if err != nil {
					node = "" // unattributed: usage still counts, at the root
					_ = err
				}
				runNode[cacheKey] = node
			}
			model, _ := ev.Data["model"].(string)
			if model == "" {
				model, _ = ev.Data["provider"].(string)
			}
			prompt := usageInt(ev.Data["prompt_tokens"])
			cached := usageInt(ev.Data["cached_tokens"])
			completion := usageInt(ev.Data["completion_tokens"])
			tokens := prompt + completion
			costUSD := costConfig.ModelCost(model, int(prompt), int(cached), int(completion))
			month := ev.OccurredAt.UTC().Format("2006-01")
			if err := ws.AddOrgMonthUsage(ctx, node, month, 0, tokens, costUSD); err != nil {
				log.Warn("org usage: accumulate", "error", err)
				return
			}
			folded++
			if occurred := ev.OccurredAt; occurred.After(newest) {
				newest = occurred
			}
		}
		if next == "" || len(events) == 0 {
			break
		}
		cursor = next
	}
	if newest.After(watermark) {
		if err := ws.AdvanceOrgUsageSync(ctx, newest); err != nil {
			log.Warn("org usage: advance watermark", "error", err)
		}
	}
	if folded > 0 {
		log.Info("org usage folded", "calls", folded)
	}
}

func usageInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		var parsed int64
		_, _ = fmt.Sscanf(n, "%d", &parsed)
		return parsed
	}
	return 0
}

// enforceMonthlyCaps is the monthly policy admission gate (§24): every source
// row that sets a monthly cap judges the month-to-date usage its own org
// scope SEES (the unit's subtree; the installation-wide row sees everything).
// The usage fold runs on a minute cadence, so the number is fresh enough to
// gate a run — and the same rows the stats view shows are the rows that
// limit.
func enforceMonthlyCaps(ctx context.Context, ws *workspace.Store, sources []workspace.Policy) error {
	month := time.Now().UTC().Format("2006-01")
	for _, src := range sources {
		if src.MonthlyMaxTokens == nil && src.MonthlyMaxBudgetUSD == nil {
			continue
		}
		ids, err := ws.OrgUnitSubtreeIDs(ctx, src.OrgUnitID)
		if err != nil {
			return err
		}
		tokens, costUSD, _, err := ws.OrgMonthSubtreeUsage(ctx, ids, month)
		if err != nil {
			return err
		}
		where := "installation-wide"
		if src.OrgUnitID != "" {
			where = fmt.Sprintf("org unit %q", src.OrgUnitID)
		}
		if src.MonthlyMaxTokens != nil && tokens >= *src.MonthlyMaxTokens {
			return fmt.Errorf("monthly token limit reached for %s: %d of %d tokens used this month", where, tokens, *src.MonthlyMaxTokens)
		}
		if src.MonthlyMaxBudgetUSD != nil && costUSD >= *src.MonthlyMaxBudgetUSD {
			return fmt.Errorf("monthly budget limit reached for %s: $%.2f of $%.2f used this month", where, costUSD, *src.MonthlyMaxBudgetUSD)
		}
	}
	return nil
}
