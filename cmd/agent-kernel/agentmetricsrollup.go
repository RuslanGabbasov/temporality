package main

// AgentSRE metrics rollup (docs/agent-evolution.md §3): a background pass
// that folds each day's real work into per-version daily counters and keeps
// the observation verdicts honest. Workspace facts (runs, verdicts,
// interventions, reworks) come from one SQL pass; the journal enriches them
// with what only it knows (tokens/cost/wall, uncertain operations, approval
// load, supervisor incidents). Upserts are idempotent — the pass re-rolls
// today hourly so the health card stays live, and yesterday once.
//
// When a version's observation window closes (per-agent days AND minimum
// runs, docs/agent-evolution.md §3.1), the verdict is journaled once as
// agent.version.observed; a degraded verdict additionally journals
// agent.version.degraded so the UI banner and the owner see it.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/temporality-project/temporality/workspace"
)

// rollupEvent is one journal event reduced to what the rollup needs.
type rollupEvent struct {
	Run        string
	Type       string
	Data       map[string]any
	OccurredAt time.Time
}

// startAgentMetricsRollup runs the rollup loop until the context is cancelled:
// one pass at startup, then hourly. Failures are logged and retried next
// tick — metrics are derived data, a bad hour costs a stale card, never data.
func startAgentMetricsRollup(ctx context.Context, log *slog.Logger, ws *workspace.Store, observationURL string) {
	runAgentMetricsRollup(ctx, log, ws, observationURL)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
		runAgentMetricsRollup(ctx, log, ws, observationURL)
	}
}

// runAgentMetricsRollup re-rolls yesterday and today, then re-evaluates the
// observation verdicts of every agent with data.
func runAgentMetricsRollup(ctx context.Context, log *slog.Logger, ws *workspace.Store, observationURL string) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, day := range []time.Time{today.Add(-24 * time.Hour), today} {
		if err := rollupDay(ctx, log, ws, observationURL, day); err != nil {
			log.Warn("agent metrics rollup", "day", day.Format("2006-01-02"), "error", err)
		}
	}
	if err := evaluateObservations(ctx, log, ws, observationURL); err != nil {
		log.Warn("agent metrics observation verdicts", "error", err)
	}
}

// rollupDay computes and stores one day of per-version counters.
func rollupDay(ctx context.Context, log *slog.Logger, ws *workspace.Store, observationURL string, day time.Time) error {
	facts, err := ws.AgentVersionDayFacts(ctx, day)
	if err != nil {
		return fmt.Errorf("workspace day facts: %w", err)
	}
	if len(facts) == 0 {
		return nil
	}
	// Journal enrichment, one windowed scan for the whole day.
	events, err := fetchJournalEventsWindow(ctx, observationURL, os.Getenv("TEMPORALITY_API_TOKEN"),
		[]string{"run.started", "run.completed", "run.failed", "run.cancelled", "tool.failed", "supervisor.verdict", "approval.requested"},
		day, day.Add(24*time.Hour))
	if err != nil {
		return fmt.Errorf("journal enrichment: %w", err)
	}
	type runTimes struct {
		started, ended time.Time
		tokens         int64
		cost           float64
		uncertain      int
		approvals      int
	}
	runs := map[string]*runTimes{}
	for _, event := range events {
		if event.Run == "" {
			continue
		}
		entry := runs[event.Run]
		if entry == nil {
			entry = &runTimes{}
			runs[event.Run] = entry
		}
		switch event.Type {
		case "run.started":
			entry.started = event.OccurredAt
		case "run.completed", "run.failed", "run.cancelled":
			entry.ended = event.OccurredAt
			if value, ok := event.Data["total_tokens"].(float64); ok {
				entry.tokens += int64(value)
			}
			if value, ok := event.Data["cost_usd"].(float64); ok {
				entry.cost += value
			}
		case "tool.failed":
			if effect, _ := event.Data["effect"].(string); effect == "uncertain" {
				entry.uncertain++
			}
		case "approval.requested":
			entry.approvals++
		}
	}
	// Supervisor incidents belong to the version through the run: count
	// needs_attention verdicts per run from the same window.
	incidents := map[string]int{}
	for _, event := range events {
		if event.Type == "supervisor.verdict" {
			if attention, _ := event.Data["needs_attention"].(bool); attention && event.Run != "" {
				incidents[event.Run]++
			}
		}
	}
	for key, fact := range facts {
		if key.Version <= 0 {
			continue // pre-versioning runs cannot be attributed honestly
		}
		metrics := workspace.AgentVersionMetrics{
			AgentID: key.AgentID, Version: key.Version, Day: day,
			Runs: fact.Runs, Succeeded: fact.Succeeded, Failed: fact.Failed,
			Cancelled: fact.Cancelled, TurnLimited: fact.TurnLimited,
			VerdictAccepted: fact.VerdictAccepted, VerdictCorrected: fact.VerdictCorrected,
			VerdictRejected: fact.VerdictRejected,
			Interventions:   fact.Interventions, Reworks: fact.Reworks,
		}
		for _, runID := range fact.RunIDs {
			entry := runs[runID]
			if entry == nil {
				continue
			}
			metrics.Tokens += entry.tokens
			metrics.CostUSD += entry.cost
			metrics.UncertainOps += entry.uncertain
			metrics.Interventions += entry.approvals
			metrics.Incidents += incidents[runID]
			if !entry.started.IsZero() && !entry.ended.IsZero() {
				metrics.WallMs += entry.ended.Sub(entry.started).Milliseconds()
			}
		}
		if err := ws.UpsertAgentVersionMetrics(ctx, metrics); err != nil {
			return fmt.Errorf("upsert metrics %s v%d: %w", key.AgentID, key.Version, err)
		}
	}
	log.Info("agent metrics rolled up", "day", day.Format("2006-01-02"), "versions", len(facts))
	return nil
}

// evaluateObservations closes observation windows and journals their
// verdicts — once per version: an agent.version.observed event marks the
// window as reported, so the pass stays idempotent.
func evaluateObservations(ctx context.Context, log *slog.Logger, ws *workspace.Store, observationURL string) error {
	token := os.Getenv("TEMPORALITY_API_TOKEN")
	observed, err := fetchJournalEventsWindow(ctx, observationURL, token, []string{"agent.version.observed"}, time.Time{}, time.Time{})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, event := range observed {
		agentID, _ := event.Data["agent_id"].(string)
		version, _ := event.Data["version"].(float64)
		if agentID != "" {
			seen[fmt.Sprintf("%s\x00%d", agentID, int(version))] = true
		}
	}
	// Every version with metrics is a candidate; only the closed windows of
	// the version's own observation report.
	for _, key := range versionsWithMetrics(ctx, ws) {
		agentID := key.agentID
		a, err := ws.GetAgent(ctx, agentID)
		if err != nil {
			continue
		}
		if seen[fmt.Sprintf("%s\x00%d", agentID, key.version)] {
			continue
		}
		snapshot, err := ws.GetAgentVersion(ctx, agentID, key.version)
		if err != nil {
			continue
		}
		cfg, err := ws.AgentObservation(ctx, agentID)
		if err != nil {
			continue
		}
		since := snapshot.CreatedAt.Add(-time.Duration(cfg.Days) * 24 * time.Hour)
		current, err := ws.ListAgentVersionMetrics(ctx, agentID, key.version, since)
		if err != nil {
			continue
		}
		baseline := []workspace.AgentVersionMetrics{}
		if snapshot.BaselineVersion > 0 && snapshot.BaselineVersion != key.version {
			baselineSince := snapshot.CreatedAt.Add(-time.Duration(cfg.Days) * 24 * time.Hour)
			rows, err := ws.ListAgentVersionMetrics(ctx, agentID, snapshot.BaselineVersion, baselineSince)
			if err == nil {
				for _, row := range rows {
					if row.Day.Before(snapshot.CreatedAt) {
						baseline = append(baseline, row)
					}
				}
			}
		}
		result := workspace.EvaluateObservation(cfg, snapshot.CreatedAt, current, baseline, time.Now().UTC())
		if result.State != "closed" || result.Verdict == "" {
			continue
		}
		emitAgentVersionEvent(ctx, observationURL, token, "agent.version.observed", a, key.version, map[string]any{
			"verdict": result.Verdict, "runs": result.Runs, "elapsed_days": result.ElapsedDays,
			"baseline_version": snapshot.BaselineVersion,
		})
		if result.Verdict == "degraded" {
			emitAgentVersionEvent(ctx, observationURL, token, "agent.version.degraded", a, key.version, map[string]any{
				"runs": result.Runs, "baseline_version": snapshot.BaselineVersion,
			})
			log.Warn("agent version degraded", "agent", agentID, "version", key.version, "runs", result.Runs)
		}
	}
	return nil
}

// versionsWithMetrics lists the (agent, version) pairs that carry rollup
// rows, newest version first per agent.
type metricsVersionKey struct {
	agentID string
	version int
}

func versionsWithMetrics(ctx context.Context, ws *workspace.Store) []metricsVersionKey {
	// The metrics listing is per agent+version; the pass discovers candidates
	// from the agents it just rolled up plus a bounded probe of recent
	// versions. Kept simple on purpose: an agent without fresh runs re-reads
	// its existing rows and skips on the observed marker.
	agents, err := ws.ListAgentsVisible(ctx, nil)
	if err != nil {
		return nil
	}
	keys := []metricsVersionKey{}
	for _, a := range agents {
		if a.DefinitionVersion > 0 {
			keys = append(keys, metricsVersionKey{agentID: a.ID, version: a.DefinitionVersion})
		}
	}
	return keys
}

// fetchJournalEventsWindow pages one typed window of journal events. Unlike
// fetchJournalTypedEvents it honors since/until and keeps occurred_at — the
// rollup measures wall time and day boundaries. Cap: toolEventsMaxPages-like
// 40 pages × 500 rows per type group is far beyond one healthy day.
func fetchJournalEventsWindow(ctx context.Context, observationURL, token string, types []string, since, until time.Time) ([]rollupEvent, error) {
	if observationURL == "" {
		return nil, nil
	}
	var collected []rollupEvent
	cursor := ""
	for page := 0; page < 40; page++ {
		query := url.Values{}
		query.Set("types", strings.Join(types, ","))
		query.Set("limit", "500")
		if !since.IsZero() {
			query.Set("since", since.Format(time.RFC3339Nano))
		}
		if !until.IsZero() {
			query.Set("until", until.Format(time.RFC3339Nano))
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			strings.TrimSuffix(observationURL, "/")+"/v1/observations/events?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("journal events window returned HTTP %d: %s", response.StatusCode, truncateForLog(strings.TrimSpace(string(body)), 200))
		}
		var pageData struct {
			Events     []rollupWireEvent `json:"events"`
			NextCursor string            `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &pageData); err != nil {
			return nil, err
		}
		for _, event := range pageData.Events {
			collected = append(collected, rollupEvent{
				Run: event.Context.Run, Type: event.Type, Data: event.Data, OccurredAt: event.OccurredAt,
			})
		}
		if pageData.NextCursor == "" {
			return collected, nil
		}
		cursor = pageData.NextCursor
	}
	return collected, nil
}

// rollupWireEvent is the journal wire shape the rollup decodes.
type rollupWireEvent struct {
	OccurredAt time.Time `json:"occurred_at"`
	Type       string    `json:"type"`
	Context    struct {
		Run string `json:"run"`
	} `json:"context"`
	Data map[string]any `json:"data"`
}
