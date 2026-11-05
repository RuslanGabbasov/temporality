package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// Agent version metrics (docs/agent-evolution.md §3): a daily rollup per
// definition version in workspace_agent_version_metrics (PK: agent_id,
// version, day), fed from the data the system already records — workspace_run
// (status, task_id, agent_version), human_request verdict projections
// (hitlverdict.go) and journal enrichment by the nightly caller. No new
// journal events are needed for metrics.

// metricsColumns is the projection every metrics read scans — the column order
// and scanAgentVersionMetrics are position-sensitive.
const metricsColumns = `agent_id, version, day, runs, succeeded, failed, cancelled, turn_limited,
	verdict_accepted, verdict_corrected, verdict_rejected, interventions, reworks,
	tokens, cost_usd, wall_ms, uncertain_ops, incidents`

// dayStart truncates a timestamp to its UTC calendar day — the metrics
// grain (§3: ежедневный rollup).
func dayStart(t time.Time) time.Time {
	utc := t.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

func scanAgentVersionMetrics(row interface{ Scan(dest ...any) error }) (AgentVersionMetrics, error) {
	var m AgentVersionMetrics
	if err := row.Scan(&m.AgentID, &m.Version, &m.Day, &m.Runs, &m.Succeeded, &m.Failed,
		&m.Cancelled, &m.TurnLimited, &m.VerdictAccepted, &m.VerdictCorrected, &m.VerdictRejected,
		&m.Interventions, &m.Reworks, &m.Tokens, &m.CostUSD, &m.WallMs, &m.UncertainOps, &m.Incidents); err != nil {
		return AgentVersionMetrics{}, err
	}
	m.Day = dayStart(m.Day)
	return m, nil
}

// UpsertAgentVersionMetrics writes one daily rollup row: a re-run of the
// nightly pass overwrites the counters wholesale (docs/agent-evolution.md §3),
// never accumulates them.
func (s *Store) UpsertAgentVersionMetrics(ctx context.Context, m AgentVersionMetrics) error {
	return upsertAgentVersionMetricsRow(ctx, s.pool, m)
}

// upsertAgentVersionMetricsRow is the single upsert against the given
// executor (the pool in production) so tests can pin the SQL shape with a
// fake execer.
func upsertAgentVersionMetricsRow(ctx context.Context, ex agentExecer, m AgentVersionMetrics) error {
	_, err := ex.Exec(ctx,
		`INSERT INTO workspace_agent_version_metrics
		 (agent_id, version, day, runs, succeeded, failed, cancelled, turn_limited,
		  verdict_accepted, verdict_corrected, verdict_rejected, interventions, reworks,
		  tokens, cost_usd, wall_ms, uncertain_ops, incidents)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		 ON CONFLICT (agent_id, version, day) DO UPDATE SET
		   runs = EXCLUDED.runs, succeeded = EXCLUDED.succeeded, failed = EXCLUDED.failed,
		   cancelled = EXCLUDED.cancelled, turn_limited = EXCLUDED.turn_limited,
		   verdict_accepted = EXCLUDED.verdict_accepted, verdict_corrected = EXCLUDED.verdict_corrected,
		   verdict_rejected = EXCLUDED.verdict_rejected, interventions = EXCLUDED.interventions,
		   reworks = EXCLUDED.reworks, tokens = EXCLUDED.tokens, cost_usd = EXCLUDED.cost_usd,
		   wall_ms = EXCLUDED.wall_ms, uncertain_ops = EXCLUDED.uncertain_ops, incidents = EXCLUDED.incidents`,
		m.AgentID, m.Version, dayStart(m.Day), m.Runs, m.Succeeded, m.Failed, m.Cancelled, m.TurnLimited,
		m.VerdictAccepted, m.VerdictCorrected, m.VerdictRejected, m.Interventions, m.Reworks,
		m.Tokens, m.CostUSD, m.WallMs, m.UncertainOps, m.Incidents)
	return err
}

// ListAgentVersionMetrics returns the daily rollups of one version since the
// given day, oldest first (the `daily` array of the metrics payload).
func (s *Store) ListAgentVersionMetrics(ctx context.Context, agentID string, version int, since time.Time) ([]AgentVersionMetrics, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+metricsColumns+` FROM workspace_agent_version_metrics
		 WHERE agent_id = $1 AND version = $2 AND day >= $3 ORDER BY day ASC`,
		agentID, version, dayStart(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AgentVersionMetrics
	for rows.Next() {
		m, err := scanAgentVersionMetrics(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// AgentVersionKey identifies one definition version of one agent in the
// day-facts map.
type AgentVersionKey struct {
	AgentID string
	Version int
}

// AgentDayFacts are the run-derived counters of one (agent, version) for one
// day (docs/agent-evolution.md §3): everything workspace_run and human_request
// can prove. Tokens/cost/wall-time and journal-derived incidents come from the
// caller's journal enrichment of RunIDs.
type AgentDayFacts struct {
	Runs             int
	Succeeded        int
	Failed           int
	Cancelled        int
	TurnLimited      int
	VerdictAccepted  int
	VerdictCorrected int
	VerdictRejected  int
	Interventions    int
	Reworks          int
	RunIDs           []string
}

// AgentVersionDayFacts computes the run-derived metrics of one UTC day in a
// single batched pass over workspace_run (started_at = created_at) joined to
// its human_request rows, attributed to run.AgentVersion (0 = no attribution,
// still counted). Verdict counters are the Verdict() projection of every
// request of those runs; interventions count the requests raised that day;
// reworks count runs whose task had already been run before (self-join).
// RunIDs ride along so the caller can enrich tokens, cost and incidents from
// the journal before upserting the rollup rows.
func (s *Store) AgentVersionDayFacts(ctx context.Context, day time.Time) (map[AgentVersionKey]AgentDayFacts, error) {
	start, end := dayStart(day), dayStart(day).AddDate(0, 0, 1)
	rows, err := s.pool.Query(ctx,
		`SELECT r.id, COALESCE(r.agent_version, 0), r.status, COALESCE(r.task_id, ''),
		        EXISTS (SELECT 1 FROM workspace_run r0
		                WHERE r0.task_id = r.task_id AND r0.task_id <> ''
		                  AND (r0.created_at < r.created_at OR (r0.created_at = r.created_at AND r0.id < r.id))),
		        h.status, h.options, h.response, h.created_at
		 FROM workspace_run r
		 LEFT JOIN human_request h ON h.run_id = r.id
		 WHERE r.created_at >= $1 AND r.created_at < $2`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type runFacts struct {
		key    AgentVersionKey
		seen   bool
		rework bool
	}
	byRun := map[string]*runFacts{}
	facts := map[AgentVersionKey]*AgentDayFacts{}
	factsFor := func(key AgentVersionKey) *AgentDayFacts {
		if f, ok := facts[key]; ok {
			return f
		}
		f := &AgentDayFacts{}
		facts[key] = f
		return f
	}
	for rows.Next() {
		var runID, runStatus, taskID string
		var version int
		var rework bool
		var reqStatus, reqResponse *string
		var reqOptions []byte
		var reqCreatedAt *time.Time
		if err := rows.Scan(&runID, &version, &runStatus, &taskID, &rework,
			&reqStatus, &reqOptions, &reqResponse, &reqCreatedAt); err != nil {
			return nil, err
		}
		key := AgentVersionKey{Version: version}
		run, seen := byRun[runID]
		if !seen {
			run = &runFacts{key: key, rework: rework}
			byRun[runID] = run
			f := factsFor(key)
			f.Runs++
			switch {
			case runSucceeded(runStatus):
				f.Succeeded++
			case runStatus == "failed":
				f.Failed++
			case runStatus == "cancelled":
				f.Cancelled++
			case runStatus == "turn_limit":
				f.TurnLimited++
			}
			if rework {
				f.Reworks++
			}
			f.RunIDs = append(f.RunIDs, runID)
		}
		if reqStatus == nil {
			continue // the run has no human requests on this row
		}
		f := factsFor(run.key)
		var options []string
		if len(reqOptions) > 0 {
			_ = json.Unmarshal(reqOptions, &options)
		}
		switch Verdict(HumanRequest{Status: *reqStatus, Options: options, Response: derefPtr(reqResponse)}) {
		case VerdictAccepted:
			f.VerdictAccepted++
		case VerdictCorrected:
			f.VerdictCorrected++
		case VerdictRejected:
			f.VerdictRejected++
		}
		if reqCreatedAt != nil && !reqCreatedAt.Before(start) && reqCreatedAt.Before(end) {
			f.Interventions++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make(map[AgentVersionKey]AgentDayFacts, len(facts))
	for key, f := range facts {
		result[key] = *f
	}
	return result, nil
}

// ObservationResult is the §3.1 verdict of one observation window: open
// (observing) or closed with the honest verdict — improved / stable / degraded
// only on enough data, insufficient otherwise.
type ObservationResult struct {
	State       string `json:"state"`   // observing | closed
	Verdict     string `json:"verdict"` // "" | improved | stable | degraded | insufficient
	ElapsedDays int    `json:"elapsed_days"`
	Runs        int    `json:"runs"`
}

// EvaluateObservation resolves one observation window (docs/agent-evolution.md §3.1):
// the window closes when observation_days have passed AND observation_min_runs
// runs of the version have accumulated; a window that is more than twice its
// configured age without the run minimum closes as «недостаточно данных»
// instead of waiting forever. A closed window compares the version against its
// baseline arm like-for-like — either arm under the run minimum is always
// insufficient, never «улучшение». Rates move together (signal threshold
// max(0.05, 1/sqrt(min(n_current, n_baseline)))) and improved/degraded requires
// at least two of the four primary SLIs signaling one way with none signaling
// the other; anything else is stable. Pure: the caller supplies the rolled-up
// metrics of both arms and the clock.
func EvaluateObservation(cfg ObservationConfig, versionCreatedAt time.Time, current, baseline []AgentVersionMetrics, now time.Time) ObservationResult {
	cur, base := SumMetrics(current), SumMetrics(baseline)
	elapsed := 0
	if since := now.Sub(versionCreatedAt); since > 0 {
		elapsed = int(since / (24 * time.Hour))
	}
	result := ObservationResult{State: "observing", ElapsedDays: elapsed, Runs: cur.Runs}
	if elapsed > 2*cfg.Days && cur.Runs < cfg.MinRuns {
		result.State, result.Verdict = "closed", "insufficient"
		return result
	}
	if elapsed < cfg.Days || cur.Runs < cfg.MinRuns {
		return result
	}
	result.State = "closed"
	if base.Runs < cfg.MinRuns {
		result.Verdict = "insufficient"
		return result
	}

	threshold := math.Max(0.05, 1/math.Sqrt(float64(min(cur.Runs, base.Runs))))
	verdicts := func(w WindowSummary) float64 {
		return float64(w.VerdictAccepted + w.VerdictCorrected + w.VerdictRejected)
	}
	improved, degraded := 0, 0
	// signal folds one primary SLI's delta into the verdict tally; a metric
	// whose denominator is empty on either arm is skipped — it cannot signal.
	signal := func(curRate, baseRate, curDen, baseDen float64, lowerIsBetter bool) {
		if curDen <= 0 || baseDen <= 0 {
			return
		}
		delta := curRate/curDen - baseRate/baseDen
		if math.Abs(delta) < threshold {
			return
		}
		if (delta < 0) == lowerIsBetter {
			improved++
		} else {
			degraded++
		}
	}
	curRuns, baseRuns := float64(cur.Runs), float64(base.Runs)
	signal(float64(cur.Succeeded), float64(base.Succeeded), curRuns, baseRuns, false)                                   // success rate, higher is better
	signal(float64(cur.VerdictCorrected), float64(base.VerdictCorrected), verdicts(cur), verdicts(base), true)          // correction rate
	signal(float64(cur.Interventions), float64(base.Interventions), curRuns, baseRuns, true)                            // intervention rate
	signal(cur.UncertainOps+float64(cur.Incidents), base.UncertainOps+float64(base.Incidents), curRuns, baseRuns, true) // incident rate

	switch {
	case improved >= 2 && degraded == 0:
		result.Verdict = "improved"
	case degraded >= 2 && improved == 0:
		result.Verdict = "degraded"
	default:
		result.Verdict = "stable"
	}
	return result
}

// WindowSummary is the aggregated metrics window of one arm of a comparison
// (docs/agent-evolution.md §5 «current» / «baseline»). UncertainOps is a
// float because the journal enrichment feeds it as a per-run average sum.
type WindowSummary struct {
	Runs             int     `json:"runs"`
	Succeeded        int     `json:"succeeded"`
	Failed           int     `json:"failed"`
	Cancelled        int     `json:"cancelled"`
	TurnLimited      int     `json:"turn_limited"`
	VerdictAccepted  int     `json:"verdict_accepted"`
	VerdictCorrected int     `json:"verdict_corrected"`
	VerdictRejected  int     `json:"verdict_rejected"`
	Interventions    int     `json:"interventions"`
	Reworks          int     `json:"reworks"`
	Incidents        int     `json:"incidents"`
	Tokens           int64   `json:"tokens"`
	WallMs           int64   `json:"wall_ms"`
	CostUSD          float64 `json:"cost_usd"`
	UncertainOps     float64 `json:"uncertain_ops"`
}

// SumMetrics folds daily rollups into one window summary.
func SumMetrics(rows []AgentVersionMetrics) WindowSummary {
	var sum WindowSummary
	for _, m := range rows {
		sum.Runs += m.Runs
		sum.Succeeded += m.Succeeded
		sum.Failed += m.Failed
		sum.Cancelled += m.Cancelled
		sum.TurnLimited += m.TurnLimited
		sum.VerdictAccepted += m.VerdictAccepted
		sum.VerdictCorrected += m.VerdictCorrected
		sum.VerdictRejected += m.VerdictRejected
		sum.Interventions += m.Interventions
		sum.Reworks += m.Reworks
		sum.Incidents += m.Incidents
		sum.Tokens += m.Tokens
		sum.WallMs += m.WallMs
		sum.CostUSD += m.CostUSD
		sum.UncertainOps += float64(m.UncertainOps)
	}
	return sum
}

// MetricDelta is one row of the «delta» comparison (docs/agent-evolution.md §5):
// the rate of both arms and whether the difference clears the noise threshold.
type MetricDelta struct {
	Metric   string  `json:"metric"`
	Current  float64 `json:"current"`
	Baseline float64 `json:"baseline"`
	Delta    float64 `json:"delta"`
	Signal   bool    `json:"signal"`
}

// MetricDeltas compares two windows rate-for-rate (docs/agent-evolution.md §3.1):
// the four primary SLIs plus the informational per-run averages. A difference
// is a signal only when both arms carry at least minRuns runs and |Δ| clears
// max(0.05, 1/sqrt(min(n_current, n_baseline))); the averages are informational
// and never signal. Rates with an empty denominator read as 0 and never signal.
func MetricDeltas(cur, base WindowSummary, minRuns int) []MetricDelta {
	verdicts := func(w WindowSummary) float64 {
		return float64(w.VerdictAccepted + w.VerdictCorrected + w.VerdictRejected)
	}
	rate := func(num, den float64) float64 {
		if den <= 0 {
			return 0
		}
		return num / den
	}
	type windowRate struct {
		name          string
		current       float64
		baseline      float64
		curDen        float64
		baseDen       float64
		informational bool
	}
	rates := []windowRate{
		{"success_rate", rate(float64(cur.Succeeded), float64(cur.Runs)), rate(float64(base.Succeeded), float64(base.Runs)), float64(cur.Runs), float64(base.Runs), false},
		{"correction_rate", rate(float64(cur.VerdictCorrected), verdicts(cur)), rate(float64(base.VerdictCorrected), verdicts(base)), verdicts(cur), verdicts(base), false},
		{"intervention_rate", rate(float64(cur.Interventions), float64(cur.Runs)), rate(float64(base.Interventions), float64(base.Runs)), float64(cur.Runs), float64(base.Runs), false},
		{"incident_rate", rate(cur.UncertainOps+float64(cur.Incidents), float64(cur.Runs)), rate(base.UncertainOps+float64(base.Incidents), float64(base.Runs)), float64(cur.Runs), float64(base.Runs), false},
		{"tokens_avg", rate(float64(cur.Tokens), float64(cur.Runs)), rate(float64(base.Tokens), float64(base.Runs)), 1, 1, true},
		{"cost_avg", rate(cur.CostUSD, float64(cur.Runs)), rate(base.CostUSD, float64(base.Runs)), 1, 1, true},
		{"wall_avg", rate(float64(cur.WallMs), float64(cur.Runs)), rate(float64(base.WallMs), float64(base.Runs)), 1, 1, true},
	}
	threshold := 0.0
	if cur.Runs > 0 && base.Runs > 0 {
		threshold = math.Max(0.05, 1/math.Sqrt(float64(min(cur.Runs, base.Runs))))
	}
	deltas := make([]MetricDelta, 0, len(rates))
	for _, r := range rates {
		d := MetricDelta{Metric: r.name, Current: r.current, Baseline: r.baseline, Delta: r.current - r.baseline}
		d.Signal = !r.informational &&
			cur.Runs >= minRuns && base.Runs >= minRuns &&
			r.curDen > 0 && r.baseDen > 0 &&
			math.Abs(d.Delta) >= threshold
		deltas = append(deltas, d)
	}
	return deltas
}

// AgentObservation reads the per-agent observation window (docs/agent-evolution.md §2.3).
func (s *Store) AgentObservation(ctx context.Context, agentID string) (ObservationConfig, error) {
	var cfg ObservationConfig
	err := s.pool.QueryRow(ctx,
		`SELECT observation_days, observation_min_runs FROM workspace_agent WHERE id = $1`, agentID).
		Scan(&cfg.Days, &cfg.MinRuns)
	if errors.Is(err, pgx.ErrNoRows) {
		return ObservationConfig{}, ErrNotFound
	}
	if err != nil {
		return ObservationConfig{}, err
	}
	return cfg, nil
}

// SetAgentObservation updates the per-agent observation window. The bounds
// keep the knob honest: 1–90 days, 1–100 runs — anything else is a conflict,
// not a silent clamp.
func (s *Store) SetAgentObservation(ctx context.Context, agentID string, cfg ObservationConfig) error {
	if cfg.Days < 1 || cfg.Days > 90 || cfg.MinRuns < 1 || cfg.MinRuns > 100 {
		return fmt.Errorf("%w: observation window out of range (days 1..90, min_runs 1..100)", ErrConflict)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_agent SET observation_days = $2, observation_min_runs = $3, updated_at = now()
		 WHERE id = $1`, agentID, cfg.Days, cfg.MinRuns)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
