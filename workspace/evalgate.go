package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Eval gate (specs/quality-loop.md §5, §8.3, §10.4, M3): applying a proposal
// requires a fresh green evaluation run on the proposal's base version. This
// file owns the freshness window (§5.3, §11.3), the gate state machine and
// the gate-run lifecycle the 202-apply flow drives.

// Eval gate environment contract (§5.3, §11.3).
const (
	// EnvGateFreshnessWorkdays tunes how long a green run stays acceptable:
	// working days, default 3, 0 = no freshness limit at all. The knob
	// exists to be adjusted against the pilot's real apply frequency.
	EnvGateFreshnessWorkdays = "EVAL_GATE_FRESHNESS_WORKDAYS"
	// DefaultGateFreshnessWorkdays is the product decision of October 2026:
	// decisions take weeks, so a Friday green still gates on Wednesday.
	DefaultGateFreshnessWorkdays = 3
)

// Gate refusal causes (§6.4, §10.4).
const (
	// CauseJudgeDegraded: the run's judge calibration sits below the 0.7
	// agreement floor — the gate refuses such a run as if it were red.
	CauseJudgeDegraded = "judge_degraded"
	// CauseGateRunAborted: the gate run never produced a verdict (the
	// kernel died or the record path failed); §10.4 sends it to error so
	// the next apply may start a fresh run.
	CauseGateRunAborted = "gate_run_aborted"
	// CauseSuiteStale: a golden case is past its stale_after (§13 option
	// B) — the apply refuses with suite_stale until the cases are reviewed
	// or a writer overrides with a justification.
	CauseSuiteStale = "suite_stale"
)

// evalGateRunningWindow bounds how long a gate run may stay 'unknown': the
// HTTP runner is itself bounded by a 10-minute evaluation timeout, so a run
// older than this is an abandoned row from a dead kernel. It is failed
// instead of answered forever with 202 (§10.4: partial runs never gate).
const evalGateRunningWindow = 30 * time.Minute

// DefaultSuiteStaleDays is the golden drift horizon (§13 option B): a
// golden case whose expected output has not been reviewed for this many
// days turns stale and the apply gate refuses with suite_stale — drift
// becomes visible before it can masquerade as an agent regression.
const DefaultSuiteStaleDays = 90

// EvalSuiteStaleCases lists gate-relevant case names whose stale_after has
// passed at now (§13 option B): held-out cases never decide the gate (§4.5)
// and stale ones never sneak past it either, so they are excluded from the
// list; cases without stale_after are exempt by design.
func EvalSuiteStaleCases(cases []EvaluationCase, now time.Time) []string {
	stale := []string{}
	for _, c := range cases {
		if c.HeldOut || c.StaleAfter == nil {
			continue
		}
		if now.After(*c.StaleAfter) {
			stale = append(stale, c.Name)
		}
	}
	return stale
}

// evalGateFreshnessWorkdays reads the freshness knob. Unparseable or
// negative values fall back to the default: a broken knob must not widen the
// gate (the same rule the M4 threshold envs follow).
func evalGateFreshnessWorkdays() int {
	raw := strings.TrimSpace(os.Getenv(EnvGateFreshnessWorkdays))
	if raw == "" {
		return DefaultGateFreshnessWorkdays
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return DefaultGateFreshnessWorkdays
	}
	return value
}

// evalGateFreshCutoff returns the oldest created_at that still counts as a
// fresh gate run judged at now: the earliest day whose working-day age does
// not exceed workdays. workdays <= 0 disables the limit (zero time — every
// run is fresh). Weekends never consume freshness: a green Friday run stays
// valid through the end of the following Wednesday (§5.3). Holidays are
// deliberately not modelled in v1 (§5.3, product decision).
func evalGateFreshCutoff(now time.Time, workdays int) time.Time {
	if workdays <= 0 {
		return time.Time{}
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := today
	// working days inside the interval (day, today]: stepping the left edge
	// back one day adds that day itself to the interval, so the count only
	// grows and the walk is linear in workdays.
	working := 0
	for {
		next := working
		if isEvalWorkingDay(day) {
			next++
		}
		if next > workdays {
			return day
		}
		day, working = day.AddDate(0, 0, -1), next
	}
}

// isEvalWorkingDay reports whether a calendar day consumes freshness: the
// gate skips Saturdays and Sundays (§5.3); holiday calendars stay out of v1.
func isEvalWorkingDay(day time.Time) bool {
	weekday := day.Weekday()
	return weekday != time.Saturday && weekday != time.Sunday
}

// EvalGateState is the gate answer for one proposal apply (§5.3).
type EvalGateState string

const (
	// EvalGateGreen: a fresh, non-degraded pass on the base version —
	// apply proceeds.
	EvalGateGreen EvalGateState = "green"
	// EvalGateRed: the latest decisive run failed (or carries a degraded
	// judge) — apply answers 409 with the failed cases.
	EvalGateRed EvalGateState = "red"
	// EvalGateRunning: this proposal already has an unfinished gate run —
	// apply answers 202 with that exact evaluation_run_id (§8.3).
	EvalGateRunning EvalGateState = "running"
	// EvalGateNone: nothing usable on the base version — apply must start
	// a gate run and answer 202.
	EvalGateNone EvalGateState = "none"
)

// EvalGateDecision is the resolved gate state for one apply.
type EvalGateDecision struct {
	State EvalGateState
	// Run is the run that decided the state: the green/red run, or the
	// running gate run the 202 must reference. nil for EvalGateNone.
	Run *AgentEvaluationRun
	// Cause qualifies a red decision: judge_degraded, or the run's own
	// fail cause when it has one.
	Cause string
}

// evalGateDecide folds the gate's two lookups into one answer:
//
//   - an unfinished gate run of this proposal wins — a repeat apply waits
//     for exactly that evaluation_run_id and never starts a second run
//     (§8.3 idempotency);
//   - otherwise the latest decisive run on the base version decides: pass
//     is green only when fresh and not judge-degraded (§6.4 refuses
//     degraded runs as red), fail is red (§5.3), error or a stale pass
//     means "start a new gate run" (§5.3 / §10.4 — a retry never reuses
//     an aborted run). Legacy 'unknown' rows never reach this function
//     (§5.1).
//
// Note (§13 option B): suite drift is NOT decided here — a golden past its
// stale_after surfaces as cause suite_stale in the apply handler through
// EvalSuiteStaleCases, before the gate decision is even consulted.
func evalGateDecide(running, latest *AgentEvaluationRun, cutoff time.Time) EvalGateDecision {
	if running != nil {
		return EvalGateDecision{State: EvalGateRunning, Run: running}
	}
	if latest == nil {
		return EvalGateDecision{State: EvalGateNone}
	}
	switch latest.Verdict {
	case EvalVerdictPass:
		if latest.JudgeDegraded {
			return EvalGateDecision{State: EvalGateRed, Run: latest, Cause: CauseJudgeDegraded}
		}
		if latest.CreatedAt.Before(cutoff) {
			return EvalGateDecision{State: EvalGateNone, Run: latest}
		}
		return EvalGateDecision{State: EvalGateGreen, Run: latest}
	case EvalVerdictFail:
		return EvalGateDecision{State: EvalGateRed, Run: latest, Cause: latest.Cause}
	default: // error (or legacy unknown): no usable signal for the gate
		return EvalGateDecision{State: EvalGateNone, Run: latest}
	}
}

// evalGateRunColumns is the projection gate decisions work from. The case
// list comes along because a red 409 names the failed cases (§8.3); case
// answers are never projected here, so gate responses stay free of
// production text (§10.3).
const evalGateRunColumns = `id, agent_id, agent_version, passed, failed, cases, verdict, cause,
	judge_degraded, trigger, proposal_id, created_at`

func scanEvalGateRun(scanner interface{ Scan(dest ...any) error }) (AgentEvaluationRun, error) {
	var run AgentEvaluationRun
	var cases []byte
	var proposalID *int64
	err := scanner.Scan(&run.ID, &run.AgentID, &run.AgentVersion, &run.Passed, &run.Failed, &cases,
		&run.Verdict, &run.Cause, &run.JudgeDegraded, &run.Trigger, &proposalID, &run.CreatedAt)
	if err != nil {
		return AgentEvaluationRun{}, err
	}
	if proposalID != nil {
		run.ProposalID = *proposalID
	}
	if len(cases) > 0 {
		_ = json.Unmarshal(cases, &run.Cases)
	}
	return run, nil
}

// EvalGateStatus resolves the gate for one proposal apply (§5.3). now is an
// argument so the freshness window stays testable; HTTP callers pass
// time.Now().
func (s *Store) EvalGateStatus(ctx context.Context, agentID string, proposalID int64, baseVersion int, now time.Time) (EvalGateDecision, error) {
	running, err := s.gateProposalRunningRun(ctx, agentID, proposalID)
	if err != nil {
		return EvalGateDecision{}, err
	}
	if running != nil && now.Sub(running.CreatedAt) > evalGateRunningWindow {
		// The runner died without recording (§10.4): fail the run so this
		// apply starts a new one instead of answering 202 forever.
		if err := s.AbandonGateRun(ctx, running.ID, CauseGateRunAborted); err != nil {
			return EvalGateDecision{}, err
		}
		running = nil
	}
	latest, err := s.latestDecisiveRun(ctx, agentID, baseVersion)
	if err != nil {
		return EvalGateDecision{}, err
	}
	return evalGateDecide(running, latest, evalGateFreshCutoff(now, evalGateFreshnessWorkdays())), nil
}

// gateProposalRunningRun is the §8.3 idempotency key: the unfinished gate run
// this proposal already started, newest first.
func (s *Store) gateProposalRunningRun(ctx context.Context, agentID string, proposalID int64) (*AgentEvaluationRun, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+evalGateRunColumns+` FROM workspace_agent_evaluation_run
		 WHERE agent_id = $1 AND proposal_id = $2 AND trigger = 'gate' AND verdict = 'unknown'
		 ORDER BY created_at DESC LIMIT 1`,
		agentID, proposalID)
	run, err := scanEvalGateRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// latestDecisiveRun is the newest run carrying a usable verdict on the base
// version. Legacy 'unknown' rows never enter the gate (§5.1); the gate index
// from migration 000061 serves exactly this lookup (§10.2: read one run, not
// the history).
func (s *Store) latestDecisiveRun(ctx context.Context, agentID string, version int) (*AgentEvaluationRun, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+evalGateRunColumns+` FROM workspace_agent_evaluation_run
		 WHERE agent_id = $1 AND agent_version = $2 AND verdict IN ('pass','fail','error')
		 ORDER BY created_at DESC LIMIT 1`,
		agentID, version)
	run, err := scanEvalGateRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// CreateGateRun pre-creates the evaluation run the apply endpoint will wait
// on (§5.3): trigger='gate', verdict 'unknown' until the runner finalises it,
// linked to the proposal through migration 000060. The advisory lock plus a
// running check inside it make creation idempotent — two concurrent applies
// of one proposal share a single run instead of racing a second one in
// (§8.3). created reports whether this call inserted the row: only the
// creator may launch the runner, otherwise two applies that both resolved
// the gate to 'none' would start two evaluations over one run row (CWE-362).
func (s *Store) CreateGateRun(ctx context.Context, agentID string, version int, proposalID int64) (AgentEvaluationRun, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AgentEvaluationRun{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
		fmt.Sprintf("eval-gate-proposal:%d", proposalID)); err != nil {
		return AgentEvaluationRun{}, false, err
	}
	row := tx.QueryRow(ctx,
		`SELECT `+evalGateRunColumns+` FROM workspace_agent_evaluation_run
		 WHERE agent_id = $1 AND proposal_id = $2 AND trigger = 'gate' AND verdict = 'unknown'
		 ORDER BY created_at DESC LIMIT 1`,
		agentID, proposalID)
	if existing, scanErr := scanEvalGateRun(row); scanErr == nil {
		return existing, false, nil // already started: the same 202, no second run
	} else if !errors.Is(scanErr, pgx.ErrNoRows) {
		return AgentEvaluationRun{}, false, scanErr
	}

	run, err := scanEvalGateRun(tx.QueryRow(ctx,
		`INSERT INTO workspace_agent_evaluation_run (agent_id, agent_version, trigger, proposal_id)
		 VALUES ($1, $2, 'gate', $3)
		 RETURNING id, agent_id, agent_version, passed, failed, cases, verdict, cause,
		           judge_degraded, trigger, proposal_id, created_at`,
		agentID, version, proposalID))
	if err != nil {
		return AgentEvaluationRun{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentEvaluationRun{}, false, err
	}
	return run, true, nil
}

// AbandonGateRun fails a gate run that never produced a verdict (§10.4):
// verdict=error keeps it out of every green path while letting the next
// apply start a fresh run. A finalised row is never touched (§8).
func (s *Store) AbandonGateRun(ctx context.Context, runID int64, cause string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workspace_agent_evaluation_run SET verdict = 'error', cause = $2
		 WHERE id = $1 AND verdict = 'unknown'`,
		runID, cause)
	return err
}
