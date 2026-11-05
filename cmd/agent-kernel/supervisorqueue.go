package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// Supervisor attention queue + agentSRE rollups (docs/supervisor-model.md §3).
//
// The queue serves CASES server-side — stable pagination, facets, one badge
// truth — instead of asking the UI to page a journal that outgrew its window
// (the empty-tab incident: the client fetched the OLDEST 500 verdicts, all
// acknowledged weeks ago, and showed «no problems» while fresh verdicts
// stacked up beyond the page). Two background passes feed it:
//
//   - the case sync folds supervisor.verdict events into
//     workspace_supervisor_case rows (idempotent per event, watermark-gated);
//   - the SRE rollup buckets observation.ComputeHealthStats per hour/project
//     into the operational series the Signals panel reads. Version-level SLI
//     comparison is agent-evolution v2's workspace_agent_version_metrics —
//     this series is the operational health only.

const (
	supervisorQueueTick      = 2 * time.Minute
	sreRollupTick            = 30 * time.Minute
	supervisorSyncLookback   = 24 * time.Hour
	supervisorSyncPage       = 500
	supervisorSyncMaxPages   = 20
	supervisorEventRetention = 90 * 24 * time.Hour
	sreRollupWindow          = 6 * time.Hour
	sreRollupStartup         = 48 * time.Hour
	sreRollupDailyWindow     = 30 * 24 * time.Hour
)

// healthEventTypes are the journal types observation.ComputeHealthStats
// aggregates (the SRE pull asks for exactly these).
var healthEventTypes = strings.Join([]string{
	"run.started", "run.completed", "run.failed", "run.cancelled",
	"model.started", "model.attempt", "model.completed", "model.failed", "model.nudged",
	"approval.requested", "approval.granted", "approval.rejected", "approval.auto_granted",
	"human.answered", "agent.summary",
}, ",")

// RegisterSupervisorQueueRoutes mounts the queue/rollup endpoints and starts
// the feeding passes (async — never blocks request wiring).
func RegisterSupervisorQueueRoutes(ctx context.Context, mux *http.ServeMux, ws *workspace.Store, gate *controlplane.Gate, observationURL, journalToken string, log *slog.Logger) {
	// The attention queue (§7): cases, facets and the badge count in one
	// page. Operator, like the ack surface it replaces.
	mux.HandleFunc("GET /v1/workspace/supervisor/queue", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		showResolved := r.URL.Query().Get("show_resolved") == "1" || r.URL.Query().Get("show_resolved") == "true"
		queue, err := ws.ListSupervisorQueue(r.Context(), workspace.SupervisorQueueFilter{
			Family:       strings.TrimSpace(r.URL.Query().Get("family")),
			Project:      strings.TrimSpace(r.URL.Query().Get("project")),
			Status:       strings.TrimSpace(r.URL.Query().Get("status")),
			ShowResolved: showResolved,
			Limit:        limit,
			Offset:       offset,
		})
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, queue)
	})

	// Decide (§7): the case carries its key in the body — case keys contain
	// slashes (supervisor-case/<family>/<subject>), a path parameter could
	// never hold one.
	mux.HandleFunc("POST /v1/workspace/supervisor/queue/decide", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			CaseKey  string `json:"case_key"`
			Decision string `json:"decision"`
			Comment  string `json:"comment"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		req.CaseKey = strings.TrimSpace(req.CaseKey)
		if req.CaseKey == "" {
			writeError(w, 422, errors.New("case_key is required"))
			return
		}
		decision := strings.TrimSpace(req.Decision)
		if decision == "" {
			decision = "acknowledge"
		}
		c, err := ws.DecideSupervisorCase(r.Context(), req.CaseKey, decision, requestUserID(r))
		switch {
		case errors.Is(err, workspace.ErrSupervisorCaseDecided):
			writeError(w, 409, fmt.Errorf("case %q already decided", req.CaseKey))
		case errors.Is(err, workspace.ErrNotFound):
			writeError(w, 404, errors.New("case not found"))
		case err != nil:
			writeError(w, 500, err)
		default:
			writeJSON(w, 200, c)
		}
	})

	// The operational series (§3): bucketed health for the Signals panel and
	// the anomaly detector. Reader-level — aggregates carry no production text.
	mux.HandleFunc("GET /v1/workspace/sre/rollup", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		granularity := r.URL.Query().Get("granularity")
		if granularity == "" {
			granularity = "hour"
		}
		since := time.Now().UTC().Add(-24 * time.Hour)
		if raw := r.URL.Query().Get("since"); raw != "" {
			if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
				since = parsed
			}
		}
		rollups, err := ws.ListSreRollup(r.Context(), granularity, since,
			strings.TrimSpace(r.URL.Query().Get("project")), strings.TrimSpace(r.URL.Query().Get("model")))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"rollups": rollups, "count": len(rollups)})
	})

	go runSupervisorQueueJob(ctx, ws, observationURL, journalToken, log)
}

// runSupervisorQueueJob: the case sync on its own cadence, the SRE rollup on
// its (a startup pass covers restarts), both best-effort — a failed pass is
// logged and retried by the next tick.
func runSupervisorQueueJob(ctx context.Context, ws *workspace.Store, observationURL, journalToken string, log *slog.Logger) {
	syncSupervisorCases(ctx, ws, observationURL, journalToken, log)
	rollupSre(ctx, ws, observationURL, journalToken, sreRollupStartup, log)
	queueTicker := time.NewTicker(supervisorQueueTick)
	sreTicker := time.NewTicker(sreRollupTick)
	defer queueTicker.Stop()
	defer sreTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-queueTicker.C:
			syncSupervisorCases(ctx, ws, observationURL, journalToken, log)
		case <-sreTicker.C:
			rollupSre(ctx, ws, observationURL, journalToken, sreRollupWindow, log)
		}
	}
}

// syncSupervisorCases folds new supervisor.verdict events into cases. Events
// walk oldest→newest past the watermark; the workspace_supervisor_case_event
// anchor makes replays idempotent, so a crashed pass never double-counts.
func syncSupervisorCases(ctx context.Context, ws *workspace.Store, observationURL, journalToken string, log *slog.Logger) {
	watermark, err := ws.SupervisorSyncWatermark(ctx)
	if err != nil {
		log.Warn("supervisor case sync: watermark", "error", err)
		return
	}
	since := watermark
	if since.IsZero() {
		since = time.Now().UTC().Add(-supervisorSyncLookback)
	} else {
		since = since.Add(-time.Second) // boundary slack; the anchor dedupes
	}
	cursor := ""
	newest := watermark
	for page := 0; page < supervisorSyncMaxPages; page++ {
		params := url.Values{
			"type": {"supervisor.verdict"}, "order": {"asc"},
			"since": {since.Format(time.RFC3339Nano)}, "limit": {strconv.Itoa(supervisorSyncPage)},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		events, next, err := supervisorEventPage(ctx, observationURL, journalToken, params)
		if err != nil {
			log.Warn("supervisor case sync: journal page", "error", err)
			return
		}
		for _, event := range events {
			data, _ := event["data"].(map[string]any)
			if kind, _ := data["kind"].(string); kind == "reconciliation" {
				continue // the supervisor's applied work — the run surfaces own it
			}
			eventID, _ := event["event_id"].(string)
			eventContext, _ := event["context"].(map[string]any)
			project, _ := eventContext["project"].(string)
			occurredAt := supervisorEventTime(event["occurred_at"])
			family, _ := data["case"].(string)
			if family == "" {
				family = "unknown"
			}
			cause, _ := data["cause_class"].(string)
			subject, _ := data["subject"].(string)
			summary, _ := data["summary"].(string)
			recommendation, _ := data["recommendation"].(string)
			payload, _ := json.Marshal(data)
			if _, err := ws.UpsertSupervisorCase(ctx, eventID, family, cause, subject, project,
				summary, recommendation, payload, occurredAt); err != nil {
				log.Warn("supervisor case sync: fold verdict", "run", eventID, "error", err)
				return
			}
			if occurredAt.After(newest) {
				newest = occurredAt
			}
		}
		if next == "" || len(events) == 0 {
			break
		}
		cursor = next
	}
	if newest.After(watermark) {
		if err := ws.AdvanceSupervisorSync(ctx, newest); err != nil {
			log.Warn("supervisor case sync: advance watermark", "error", err)
		}
	}
	if _, err := ws.PruneSupervisorCaseEvents(ctx, time.Now().UTC().Add(-supervisorEventRetention)); err != nil {
		log.Warn("supervisor case sync: prune anchors", "error", err)
	}
}

// rollupSre recomputes the recent hourly buckets from the journal and
// rebuilds the day rows from the hour rows. ComputeHealthStats is the same
// math the dashboard renders — the series and the live view never disagree.
func rollupSre(ctx context.Context, ws *workspace.Store, observationURL, journalToken string, window time.Duration, log *slog.Logger) {
	since := time.Now().UTC().Truncate(time.Hour).Add(-window)
	events := []observation.Event{}
	cursor := ""
	for page := 0; page < supervisorSyncMaxPages; page++ {
		params := url.Values{
			"types": {healthEventTypes}, "order": {"asc"},
			"since": {since.Format(time.RFC3339Nano)}, "limit": {strconv.Itoa(supervisorSyncPage)},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		batch, next, err := supervisorTypedEventPage(ctx, observationURL, journalToken, params)
		if err != nil {
			log.Warn("sre rollup: journal page", "error", err)
			return
		}
		events = append(events, batch...)
		if next == "" || len(batch) == 0 {
			break
		}
		cursor = next
	}
	settings, _ := ws.SupervisorSettings(ctx)
	human := observation.HumanFactors{
		ApprovalSeconds:   settings.HAB.ApprovalSeconds,
		AnswerBaseSeconds: settings.HAB.AnswerBaseSeconds,
		ReadingWPM:        settings.HAB.ReadingWPM,
	}
	type bucketKey struct {
		hour    time.Time
		project string
	}
	groups := map[bucketKey][]observation.Event{}
	for _, event := range events {
		if event.OccurredAt.Before(since) {
			continue
		}
		key := bucketKey{hour: event.OccurredAt.UTC().Truncate(time.Hour), project: event.Context.Project}
		groups[key] = append(groups[key], event)
	}
	rows := []workspace.SreRollup{}
	for key, group := range groups {
		stats := observation.ComputeHealthStats(group, key.hour, key.hour.Add(time.Hour), human)
		rows = append(rows, sreRowsFromStats(key.hour, key.project, stats)...)
	}
	if err := ws.UpsertSreBuckets(ctx, rows); err != nil {
		log.Warn("sre rollup: store buckets", "error", err)
		return
	}
	if err := ws.AggregateSreDaily(ctx, time.Now().UTC().Add(-sreRollupDailyWindow)); err != nil {
		log.Warn("sre rollup: daily aggregates", "error", err)
	}
}

// sreRowsFromStats shapes one bucket: the project aggregate (run flow, human
// factors) plus one row per model (latency and token detail).
func sreRowsFromStats(hour time.Time, project string, stats observation.HealthStats) []workspace.SreRollup {
	aggregate := workspace.SreRollup{
		BucketStart: hour, Granularity: "hour", Project: project,
		RunsStarted:        stats.Runs.Started,
		RunsCompleted:      stats.Runs.Completed,
		RunsFailed:         stats.Runs.Failed,
		RunsCancelled:      stats.Runs.Cancelled,
		RunsRunning:        stats.Runs.RunningNow,
		ModelCalls:         stats.Totals.ModelCalls,
		ModelFailures:      stats.Totals.ModelFailures,
		ModelNudged:        stats.Totals.Nudged,
		ApprovalsRequested: stats.Approvals.Requested,
		ApprovalsGranted:   stats.Approvals.Granted,
		ApprovalsAuto:      stats.Approvals.AutoGranted,
		AskHumanAnswered:   stats.Human.AskHumanAnswered,
		HABSeconds:         stats.Human.HABSeconds,
		HWTSeconds:         stats.Human.HWTSeconds,
		FlowEfficiency:     stats.Human.FlowEfficiency,
	}
	rows := []workspace.SreRollup{aggregate}
	for _, model := range stats.Models {
		rows = append(rows, workspace.SreRollup{
			BucketStart: hour, Granularity: "hour", Project: project, Model: model.Model,
			ModelCalls:    model.Calls,
			ModelFailures: model.Failed,
			ModelRetries:  model.Retries,
			ModelNudged:   model.Nudged,
			LatencyP50MS:  model.P50LatencyMS,
			LatencyP95MS:  model.P95LatencyMS,
			TotalTokens:   model.TotalTokens,
			CachedTokens:  model.CachedTokens,
		})
	}
	return rows
}

// supervisorEventPage fetches one raw journal page plus its cursor.
func supervisorEventPage(ctx context.Context, observationURL, token string, params url.Values) ([]map[string]any, string, error) {
	var page struct {
		Events     []map[string]any `json:"events"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := supervisorGetJSON(ctx, observationURL, token, params, &page); err != nil {
		return nil, "", err
	}
	return page.Events, page.NextCursor, nil
}

// supervisorTypedEventPage fetches one typed journal page (the SRE pull
// keeps the typed envelope ComputeHealthStats consumes).
func supervisorTypedEventPage(ctx context.Context, observationURL, token string, params url.Values) ([]observation.Event, string, error) {
	var page struct {
		Events     []observation.Event `json:"events"`
		NextCursor string              `json:"next_cursor"`
	}
	if err := supervisorGetJSON(ctx, observationURL, token, params, &page); err != nil {
		return nil, "", err
	}
	return page.Events, page.NextCursor, nil
}

func supervisorGetJSON(ctx context.Context, observationURL, token string, params url.Values, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(observationURL, "/")+"/v1/observations/events?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("journal events returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(into)
}

// supervisorEventTime parses the raw occurred_at (RFC3339Nano string) with a
// safe fallback — a malformed timestamp must not stall the sync.
func supervisorEventTime(raw any) time.Time {
	if text, ok := raw.(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}
