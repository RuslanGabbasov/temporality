package workspace

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// agentSRE rollups (docs/supervisor-model.md §3): the ongoing operational
// series behind the supervisor's Signals panel and the anomaly detector —
// bucketed health stats per project/model, computed with the same
// observation.ComputeHealthStats math the dashboard uses. Version-level SLI
// comparison («стала ли версия лучше») lives in workspace_agent_version_metrics
// (agent-evolution v2); this table is the OPERATIONAL series only — «здорова
// ли система и что меняется во времени».

// SreRollup is one bucket of the operational series. Empty Model means the
// project aggregate; per-model rows carry latency and token details.
type SreRollup struct {
	BucketStart        time.Time `json:"bucket_start"`
	Granularity        string    `json:"granularity"`
	Project            string    `json:"project"`
	Model              string    `json:"model"`
	RunsStarted        int       `json:"runs_started"`
	RunsCompleted      int       `json:"runs_completed"`
	RunsFailed         int       `json:"runs_failed"`
	RunsCancelled      int       `json:"runs_cancelled"`
	RunsRunning        int       `json:"runs_running"`
	ModelCalls         int       `json:"model_calls"`
	ModelFailures      int       `json:"model_failures"`
	ModelRetries       int       `json:"model_retries"`
	ModelNudged        int       `json:"model_nudged"`
	LatencyP50MS       int64     `json:"latency_p50_ms"`
	LatencyP95MS       int64     `json:"latency_p95_ms"`
	TotalTokens        int64     `json:"total_tokens"`
	CachedTokens       int64     `json:"cached_tokens"`
	ApprovalsRequested int       `json:"approvals_requested"`
	ApprovalsGranted   int       `json:"approvals_granted"`
	ApprovalsAuto      int       `json:"approvals_auto"`
	AskHumanAnswered   int       `json:"ask_human_answered"`
	HABSeconds         int64     `json:"hab_seconds"`
	HWTSeconds         int64     `json:"hwt_seconds"`
	FlowEfficiency     int       `json:"flow_efficiency"`
}

const sreRollupColumns = `bucket_start, granularity, project, model, runs_started, runs_completed,
runs_failed, runs_cancelled, runs_running, model_calls, model_failures, model_retries, model_nudged,
latency_p50_ms, latency_p95_ms, total_tokens, cached_tokens, approvals_requested, approvals_granted,
approvals_auto, ask_human_answered, hab_seconds, hwt_seconds, flow_efficiency`

func scanSreRollup(scanner interface{ Scan(dest ...any) error }) (SreRollup, error) {
	var r SreRollup
	err := scanner.Scan(&r.BucketStart, &r.Granularity, &r.Project, &r.Model, &r.RunsStarted,
		&r.RunsCompleted, &r.RunsFailed, &r.RunsCancelled, &r.RunsRunning, &r.ModelCalls,
		&r.ModelFailures, &r.ModelRetries, &r.ModelNudged, &r.LatencyP50MS, &r.LatencyP95MS,
		&r.TotalTokens, &r.CachedTokens, &r.ApprovalsRequested, &r.ApprovalsGranted,
		&r.ApprovalsAuto, &r.AskHumanAnswered, &r.HABSeconds, &r.HWTSeconds, &r.FlowEfficiency)
	return r, err
}

// UpsertSreBuckets replaces bucket rows with freshly computed ones (the tick
// re-computes recent buckets — late run outcomes land on the next pass).
func (s *Store) UpsertSreBuckets(ctx context.Context, rows []SreRollup) error {
	for _, r := range rows {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO workspace_sre_rollup (`+sreRollupColumns+`)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
			 ON CONFLICT (bucket_start, granularity, project, model) DO UPDATE SET
			   runs_started = EXCLUDED.runs_started, runs_completed = EXCLUDED.runs_completed,
			   runs_failed = EXCLUDED.runs_failed, runs_cancelled = EXCLUDED.runs_cancelled,
			   runs_running = EXCLUDED.runs_running, model_calls = EXCLUDED.model_calls,
			   model_failures = EXCLUDED.model_failures, model_retries = EXCLUDED.model_retries,
			   model_nudged = EXCLUDED.model_nudged, latency_p50_ms = EXCLUDED.latency_p50_ms,
			   latency_p95_ms = EXCLUDED.latency_p95_ms, total_tokens = EXCLUDED.total_tokens,
			   cached_tokens = EXCLUDED.cached_tokens, approvals_requested = EXCLUDED.approvals_requested,
			   approvals_granted = EXCLUDED.approvals_granted, approvals_auto = EXCLUDED.approvals_auto,
			   ask_human_answered = EXCLUDED.ask_human_answered, hab_seconds = EXCLUDED.hab_seconds,
			   hwt_seconds = EXCLUDED.hwt_seconds, flow_efficiency = EXCLUDED.flow_efficiency`,
			r.BucketStart, r.Granularity, r.Project, r.Model, r.RunsStarted, r.RunsCompleted,
			r.RunsFailed, r.RunsCancelled, r.RunsRunning, r.ModelCalls, r.ModelFailures,
			r.ModelRetries, r.ModelNudged, r.LatencyP50MS, r.LatencyP95MS, r.TotalTokens,
			r.CachedTokens, r.ApprovalsRequested, r.ApprovalsGranted, r.ApprovalsAuto,
			r.AskHumanAnswered, r.HABSeconds, r.HWTSeconds, r.FlowEfficiency); err != nil {
			return err
		}
	}
	return nil
}

// ListSreRollup reads the series for the Signals panel: one granularity,
// bounded window, optional project/model narrowing.
func (s *Store) ListSreRollup(ctx context.Context, granularity string, since time.Time, project, model string) ([]SreRollup, error) {
	if granularity != "hour" && granularity != "day" {
		return nil, fmt.Errorf("granularity must be hour or day")
	}
	args := []any{granularity, since}
	where := []string{"granularity = $1", "bucket_start >= $2"}
	if project != "" {
		args = append(args, project)
		where = append(where, fmt.Sprintf("project = $%d", len(args)))
	}
	if model != "" {
		args = append(args, model)
		where = append(where, fmt.Sprintf("model = $%d", len(args)))
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+sreRollupColumns+` FROM workspace_sre_rollup WHERE `+strings.Join(where, " AND ")+
			` ORDER BY bucket_start ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SreRollup{}
	for rows.Next() {
		r, err := scanSreRollup(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// AggregateSreDaily rebuilds the day buckets from the hour rows (the hourly
// series is the source of truth; day rows are its settled sums). Buckets
// with incomplete hours simply keep growing until the day closes — honest
// enough for trend display and cheap enough to run every tick.
func (s *Store) AggregateSreDaily(ctx context.Context, since time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_sre_rollup (`+sreRollupColumns+`)
		 SELECT date_trunc('day', bucket_start), 'day', project, model,
		        SUM(runs_started), SUM(runs_completed), SUM(runs_failed), SUM(runs_cancelled),
		        SUM(runs_running), SUM(model_calls), SUM(model_failures), SUM(model_retries),
		        SUM(model_nudged), MAX(latency_p50_ms), MAX(latency_p95_ms), SUM(total_tokens),
		        SUM(cached_tokens), SUM(approvals_requested), SUM(approvals_granted),
		        SUM(approvals_auto), SUM(ask_human_answered), SUM(hab_seconds), SUM(hwt_seconds),
		        CASE WHEN SUM(hab_seconds) + SUM(hwt_seconds) > 0
		             THEN (100 * SUM(hab_seconds)) / (SUM(hab_seconds) + SUM(hwt_seconds))
		             ELSE 0 END
		 FROM workspace_sre_rollup
		 WHERE granularity = 'hour' AND bucket_start >= $1
		 GROUP BY 1, 3, 4
		 ON CONFLICT (bucket_start, granularity, project, model) DO UPDATE SET
		   runs_started = EXCLUDED.runs_started, runs_completed = EXCLUDED.runs_completed,
		   runs_failed = EXCLUDED.runs_failed, runs_cancelled = EXCLUDED.runs_cancelled,
		   runs_running = EXCLUDED.runs_running, model_calls = EXCLUDED.model_calls,
		   model_failures = EXCLUDED.model_failures, model_retries = EXCLUDED.model_retries,
		   model_nudged = EXCLUDED.model_nudged, latency_p50_ms = EXCLUDED.latency_p50_ms,
		   latency_p95_ms = EXCLUDED.latency_p95_ms, total_tokens = EXCLUDED.total_tokens,
		   cached_tokens = EXCLUDED.cached_tokens, approvals_requested = EXCLUDED.approvals_requested,
		   approvals_granted = EXCLUDED.approvals_granted, approvals_auto = EXCLUDED.approvals_auto,
		   ask_human_answered = EXCLUDED.ask_human_answered, hab_seconds = EXCLUDED.hab_seconds,
		   hwt_seconds = EXCLUDED.hwt_seconds, flow_efficiency = EXCLUDED.flow_efficiency`, since)
	return err
}
