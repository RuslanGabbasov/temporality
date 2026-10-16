package workspace

import (
	"context"
	"sort"
	"time"
)

// Quality summary (docs/system-dashboard.md §3.3): the dashboard's quality tab
// answers with light SQL per agent instead of the heavy AgentQualityReport —
// last evaluation, stale goldens, pending proposals and the loop-level gate
// health over a fixed window.

// QualitySummaryEvaluation is the last evaluation run of one agent.
type QualitySummaryEvaluation struct {
	At             time.Time `json:"at"`
	Verdict        string    `json:"verdict"`
	Passed         int       `json:"passed"`
	Failed         int       `json:"failed"`
	PassRate       float64   `json:"pass_rate"`
	JudgeAgreement *float64  `json:"judge_agreement"`
}

// QualitySummaryAgent is one visible agent's quality row.
type QualitySummaryAgent struct {
	AgentID           string                    `json:"agent_id"`
	Name              string                    `json:"name"`
	Project           string                    `json:"project"`
	DefinitionVersion int                       `json:"definition_version"`
	LastEvaluation    *QualitySummaryEvaluation `json:"last_evaluation,omitempty"`
	PendingProposals  int                       `json:"pending_proposals"`
	StaleGoldens      int                       `json:"stale_goldens"`
}

// QualitySummaryLoop is the loop health over the summary window: gate outcomes,
// override rate (goal < 5 %) and judge degradation.
type QualitySummaryLoop struct {
	WindowDays        int     `json:"window_days"`
	GatePassed        int     `json:"gate_passed"`
	GateBlocked       int     `json:"gate_blocked"`
	GateOverridden    int     `json:"gate_overridden"`
	OverrideRate      float64 `json:"override_rate"`
	JudgeDegradedRuns int     `json:"judge_degraded_runs"`
	EvaluationRuns    int     `json:"evaluation_runs"`
}

// QualitySummary is the whole answer: one row per visible agent plus the
// loop-level counters.
type QualitySummary struct {
	Agents []QualitySummaryAgent `json:"agents"`
	Loop   QualitySummaryLoop    `json:"loop"`
}

// Gate event types counted by the loop health (specs/quality-loop.md §12).
const (
	gateEventPassed     = "agent.evaluation.gate.passed"
	gateEventBlocked    = "agent.evaluation.gate.blocked"
	gateEventOverridden = "agent.evaluation.gate.overridden"
)

// BuildQualitySummary assembles the response from preloaded inputs; all maps
// may be nil or partial (absent agent = zero counts, no fake values). Agents
// are sorted by id so the answer is stable regardless of listing order.
func BuildQualitySummary(agents []Agent, lastEval map[string]AgentEvaluationRun, judgeAgreement map[string]float64, pending map[string]int, staleGoldens map[string]int, gateCounts map[string]int, evalWindowRuns int, judgeDegradedRuns int, windowDays int) QualitySummary {
	summary := QualitySummary{Agents: make([]QualitySummaryAgent, 0, len(agents))}
	for _, a := range agents {
		row := QualitySummaryAgent{
			AgentID:           a.ID,
			Name:              a.Name,
			Project:           a.ProjectID,
			DefinitionVersion: a.DefinitionVersion,
			PendingProposals:  pending[a.ID],
			StaleGoldens:      staleGoldens[a.ID],
		}
		if run, ok := lastEval[a.ID]; ok {
			evaluation := &QualitySummaryEvaluation{
				At:      run.CreatedAt,
				Verdict: run.Verdict,
				Passed:  run.Passed,
				Failed:  run.Failed,
			}
			if total := run.Passed + run.Failed; total > 0 {
				evaluation.PassRate = roundQualityRate(float64(run.Passed) / float64(total))
			}
			// Agreement comes from the latest calibration of the run's judge
			// model; models without one stay null, never a fake 0.
			if agreement, ok := judgeAgreement[run.JudgeModel]; ok {
				evaluation.JudgeAgreement = &agreement
			}
			row.LastEvaluation = evaluation
		}
		summary.Agents = append(summary.Agents, row)
	}
	sort.Slice(summary.Agents, func(i, j int) bool { return summary.Agents[i].AgentID < summary.Agents[j].AgentID })
	loop := QualitySummaryLoop{
		WindowDays:        windowDays,
		GatePassed:        gateCounts[gateEventPassed],
		GateBlocked:       gateCounts[gateEventBlocked],
		GateOverridden:    gateCounts[gateEventOverridden],
		JudgeDegradedRuns: judgeDegradedRuns,
		EvaluationRuns:    evalWindowRuns,
	}
	if decisions := loop.GatePassed + loop.GateBlocked + loop.GateOverridden; decisions > 0 {
		loop.OverrideRate = roundQualityRate(float64(loop.GateOverridden) / float64(decisions))
	}
	summary.Loop = loop
	return summary
}

// lastEvaluationRuns returns the newest evaluation run per requested agent
// (DISTINCT ON keeps one row per agent: latest created_at, id as tiebreak).
func (s *Store) lastEvaluationRuns(ctx context.Context, agentIDs []string) (map[string]AgentEvaluationRun, error) {
	runs := map[string]AgentEvaluationRun{}
	if len(agentIDs) == 0 {
		return runs, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT ON (agent_id) agent_id, agent_version, passed, failed, verdict, judge_model, created_at
		 FROM workspace_agent_evaluation_run WHERE agent_id = ANY($1)
		 ORDER BY agent_id, created_at DESC, id DESC`, agentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var run AgentEvaluationRun
		if err := rows.Scan(&run.AgentID, &run.AgentVersion, &run.Passed, &run.Failed,
			&run.Verdict, &run.JudgeModel, &run.CreatedAt); err != nil {
			return nil, err
		}
		runs[run.AgentID] = run
	}
	return runs, rows.Err()
}

// staleGoldenCounts counts golden cases past their stale_after per agent
// (docs/quality-loop.md §13 option B): held-out cases never decide the gate
// (§4.5) so they never count as stale goldens either.
func (s *Store) staleGoldenCounts(ctx context.Context, agentIDs []string, now time.Time) (map[string]int, error) {
	counts := map[string]int{}
	if len(agentIDs) == 0 {
		return counts, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT s.agent_id, count(*) FROM workspace_agent_evaluation_suite s
		 CROSS JOIN LATERAL jsonb_array_elements(s.cases) c
		 WHERE s.agent_id = ANY($1)
		   AND coalesce((c->>'held_out')::boolean, false) = false
		   AND c->>'stale_after' IS NOT NULL
		   AND (c->>'stale_after')::timestamptz < $2
		 GROUP BY s.agent_id`, agentIDs, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var agentID string
		var count int
		if err := rows.Scan(&agentID, &count); err != nil {
			return nil, err
		}
		counts[agentID] = count
	}
	return counts, rows.Err()
}

// gateEventCounts tallies the eval-gate journal events of the given agents
// over the window; the events carry data.agent_id (cmd/agent-kernel/
// evalgate.go emitEvalGateEvent).
func (s *Store) gateEventCounts(ctx context.Context, since time.Time, agentIDs []string) (map[string]int, error) {
	counts := map[string]int{}
	if len(agentIDs) == 0 {
		return counts, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT type, count(*) FROM observation_events
		 WHERE occurred_at >= $1
		   AND type IN ('agent.evaluation.gate.passed','agent.evaluation.gate.blocked','agent.evaluation.gate.overridden')
		   AND data->>'agent_id' = ANY($2)
		 GROUP BY type`, since, agentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var eventType string
		var count int
		if err := rows.Scan(&eventType, &count); err != nil {
			return nil, err
		}
		counts[eventType] = count
	}
	return counts, rows.Err()
}

// evaluationWindowCounts returns how many evaluation runs the window holds and
// how many of them ran with a degraded judge calibration.
func (s *Store) evaluationWindowCounts(ctx context.Context, since time.Time) (runs int, degraded int, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE judge_degraded)
		 FROM workspace_agent_evaluation_run WHERE created_at >= $1`, since).
		Scan(&runs, &degraded)
	return runs, degraded, err
}

// QualitySummary assembles the dashboard summary (docs/system-dashboard.md
// §3.3) for the given (already visibility-filtered) agents: light per-agent
// SQL plus the loop-level gate and judge-degradation counts over windowDays.
func (s *Store) QualitySummary(ctx context.Context, agents []Agent, windowDays int) (QualitySummary, error) {
	now := time.Now().UTC()
	agentIDs := make([]string, 0, len(agents))
	for _, a := range agents {
		agentIDs = append(agentIDs, a.ID)
	}
	lastEval, err := s.lastEvaluationRuns(ctx, agentIDs)
	if err != nil {
		return QualitySummary{}, err
	}
	staleGoldens, err := s.staleGoldenCounts(ctx, agentIDs, now)
	if err != nil {
		return QualitySummary{}, err
	}
	since := now.AddDate(0, 0, -windowDays)
	gates, err := s.gateEventCounts(ctx, since, agentIDs)
	if err != nil {
		return QualitySummary{}, err
	}
	evalRuns, degraded, err := s.evaluationWindowCounts(ctx, since)
	if err != nil {
		return QualitySummary{}, err
	}
	pending, err := s.PendingProposalCounts(ctx)
	if err != nil {
		return QualitySummary{}, err
	}
	// Judge agreement of the deciding calibration per distinct judge model of
	// the last runs (same projection as AgentQualityReport): models without a
	// calibration row stay absent and answer null.
	judgeAgreement := map[string]float64{}
	for _, run := range lastEval {
		if run.JudgeModel == "" {
			continue
		}
		if _, seen := judgeAgreement[run.JudgeModel]; seen {
			continue
		}
		calibration, err := s.latestJudgeCalibration(ctx, run.JudgeModel)
		if err != nil {
			return QualitySummary{}, err
		}
		if calibration.JudgeModel != "" {
			judgeAgreement[run.JudgeModel] = calibration.AgreementRate
		}
	}
	return BuildQualitySummary(agents, lastEval, judgeAgreement, pending, staleGoldens, gates, evalRuns, degraded, windowDays), nil
}
