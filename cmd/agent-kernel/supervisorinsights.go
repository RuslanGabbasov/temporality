package main

// Supervisor insight pass (docs/supervisor-model.md §3, wave A3): compares
// the current health hour against the previous one, detects anomalies with
// the deterministic rules in kernel/agent, dedups against recent insights
// and journals supervisor.insight events. The supervisor model rewords the
// finding when it is configured; the deterministic template wording ships
// regardless, so a judge-off installation still gets useful explanations.

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

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// One insight per metric per window; the dedup window keeps a persisting
// anomaly from re-journaling every pass. Escalation cuts through the dedup:
// a finding that DOUBLED against the last published one (or crossed the
// absolute severe floor) is new information — staying silent for hours while
// the provider keeps degrading reads as the supervisor not noticing.
const supervisorInsightDedupWindow = 6 * time.Hour

// Escalation: republish when the finding doubles vs the last published one.
const supervisorInsightEscalateFactor = 2

// Absolute severe floor for latency findings (p95 in ms): ten minutes of
// provider p95 is actionable news no matter what the dedup window says.
const supervisorInsightSevereLatencyMS = 10 * 60_000

// runSupervisorInsightPass executes one detection pass. Model errors never
// fail the pass — the template wording is the floor.
func runSupervisorInsightPass(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL string, modelClient func(ctx context.Context) (*llm.Client, error)) {
	// Token read per pass: boot-order races must self-heal (see triage pass).
	journalToken := os.Getenv("TEMPORALITY_API_TOKEN")
	now := time.Now().UTC()
	current, err := fetchHealthStats(ctx, observationURL, journalToken, now.Add(-time.Hour), now)
	if err != nil {
		log.Warn("supervisor insights: current window", "error", err.Error())
		return
	}
	baseline, err := fetchHealthStats(ctx, observationURL, journalToken, now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		log.Warn("supervisor insights: baseline window", "error", err.Error())
		return
	}
	anomalies := agent.DetectAnomalies(current, baseline)
	if len(anomalies) == 0 {
		return
	}
	seen, magnitudes, err := fetchInsightMetrics(ctx, observationURL, journalToken, now.Add(-supervisorInsightDedupWindow))
	if err != nil {
		log.Warn("supervisor insights: list recent", "error", err.Error())
		return
	}

	client, clientErr := modelClient(ctx)
	if clientErr != nil {
		client = nil // judge off — template wording
	}
	for _, anomaly := range anomalies {
		lastAt, published := seen[anomaly.Metric]
		if published && now.Sub(lastAt) < supervisorInsightDedupWindow {
			// Inside the dedup window — but a doubled finding (or one that
			// crossed the severe floor) escalates immediately: «p95 вырос ещё
			// на порядок» is new information, not a repeat.
			lastMagnitude := magnitudes[anomaly.Metric]
			escalated := anomaly.Magnitude > 0 && lastMagnitude > 0 && anomaly.Magnitude >= supervisorInsightEscalateFactor*lastMagnitude
			if anomaly.Magnitude >= supervisorInsightSevereLatencyMS {
				escalated = true
			}
			if !escalated {
				continue
			}
			log.Info("supervisor insight escalates past the dedup window",
				"metric", anomaly.Metric, "last", lastMagnitude, "now", anomaly.Magnitude)
		}
		explanation, recommendation := agent.ExplainAnomaly(ctx, client, anomaly)
		if err := activities.PublishSupervisorInsight(ctx, anomaly.Metric, anomaly.Severity, anomaly.Title, anomaly.Current, anomaly.Baseline, explanation, recommendation, anomaly.Magnitude); err != nil {
			log.Warn("supervisor insights: publish", "metric", anomaly.Metric, "error", err.Error())
			continue
		}
		seen[anomaly.Metric] = now
		magnitudes[anomaly.Metric] = anomaly.Magnitude
		log.Info("supervisor insight recorded", "metric", anomaly.Metric, "explanation", explanation)
	}
}

// fetchHealthStats reads one health window through the journal's aggregate
// endpoint (the server-side human factors apply).
func fetchHealthStats(ctx context.Context, observationURL, journalToken string, since, until time.Time) (*observation.HealthStats, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(observationURL, "/")+"/v1/observations/stats/health?since="+url.QueryEscape(since.Format(time.RFC3339Nano))+"&until="+url.QueryEscape(until.Format(time.RFC3339Nano)), nil)
	if err != nil {
		return nil, err
	}
	if journalToken != "" {
		request.Header.Set("Authorization", "Bearer "+journalToken)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health stats %s", journalErrorSnippet(response.StatusCode, string(body)))
	}
	var stats observation.HealthStats
	if err := json.Unmarshal(body, &stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

// fetchInsightMetrics maps metric → the newest insight time inside the
// dedup window.
func fetchInsightMetrics(ctx context.Context, observationURL, journalToken string, since time.Time) (map[string]time.Time, map[string]int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(observationURL, "/")+"/v1/observations/events?type=supervisor.insight&limit=50", nil)
	if err != nil {
		return nil, nil, err
	}
	if journalToken != "" {
		request.Header.Set("Authorization", "Bearer "+journalToken)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("insight events %s", journalErrorSnippet(response.StatusCode, string(body)))
	}
	var page struct {
		Events []struct {
			OccurredAt time.Time      `json:"occurred_at"`
			Data       map[string]any `json:"data"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, nil, err
	}
	seen := map[string]time.Time{}
	magnitudes := map[string]int64{}
	for _, event := range page.Events {
		metric, _ := event.Data["metric"].(string)
		if metric == "" {
			continue
		}
		if event.OccurredAt.Before(since) {
			continue
		}
		if last, ok := seen[metric]; !ok || event.OccurredAt.After(last) {
			seen[metric] = event.OccurredAt
			// The newest event per metric carries the magnitude the escalation
			// math compares against (older events keep their own values).
			if magnitude, ok := event.Data["magnitude"].(float64); ok {
				magnitudes[metric] = int64(magnitude)
			}
		}
	}
	return seen, magnitudes, nil
}
