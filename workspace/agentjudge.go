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

// LLM-judge with calibration and thresholds (specs/quality-loop.md §6, M4).
// The judge is the third case check class next to must_contain /
// must_not_contain patterns; one installation-wide model (EVAL_JUDGE_MODEL)
// is validated against the configured providers, calibrated on historical
// verdicts before it may gate anything, and its failures always surface as
// run error — never as pass.

// Check classes (§6.1).
const (
	EvalCheckPatterns      = "patterns"       // must_contain/must_not_contain only (default)
	EvalCheckJudge         = "judge"          // judge score decides the case
	EvalCheckPatternsJudge = "patterns+judge" // patterns are necessary, judge is sufficient
)

// Run verdicts (§5.2). "unknown" marks legacy rows written before migration
// 000056; they never enter the gate.
const (
	EvalVerdictPass    = "pass"
	EvalVerdictFail    = "fail"
	EvalVerdictError   = "error"
	EvalVerdictUnknown = "unknown"
)

// Run verdict / case error causes (§6.2, §9).
const (
	CauseJudgeNotConfigured = "judge_not_configured"
	CauseJudgeInvalidJSON   = "judge_invalid_json"
	CauseJudgeError         = "judge_error"
	CauseModelTimeout       = "model_timeout"
	CauseModelError         = "model_error"
	CauseSuiteEmpty         = "suite_empty"
	CauseSuiteTooSmall      = "suite_too_small"
	CausePassRateBelowMin   = "pass_rate_below_min"
	CausePassRegression     = "pass_regression"
)

// Run triggers (§5.1).
const (
	TriggerManual   = "manual"
	TriggerGate     = "gate"
	TriggerSchedule = "schedule"
)

// Environment contract (§6.2, §6.5).
const (
	EnvJudgeModel           = "EVAL_JUDGE_MODEL"
	EnvMinPassRate          = "EVAL_MIN_PASS_RATE"
	EnvRegressionTolerance  = "EVAL_REGRESSION_TOLERANCE"
	JudgeDegradedBelow      = 0.7 // §6.4: agreement under this floor marks the judge degraded
	JudgeCalibrationCaseMax = 10  // §6.4: k cases in the fixed calibration set
)

// CheckUsesJudge reports whether a check class scores the case with the
// judge model (§6.1).
func CheckUsesJudge(check string) bool {
	return check == EvalCheckJudge || check == EvalCheckPatternsJudge
}

// JudgeSameModel reports the §11.1 self-bias marker: the judge scored the
// same model that produced the answers. Allowed (single-provider installs),
// but always visible in the UI next to the judge agreement. An empty tested
// model (legacy record path) never flags same-model.
func JudgeSameModel(testedModel, judgeModel string) bool {
	tested := strings.TrimSpace(testedModel)
	return tested != "" && tested == strings.TrimSpace(judgeModel)
}

// JudgeCalibrationDegraded applies the §6.4 floor to a stored agreement:
// under 0.7 the judge is degraded and the gate refuses its runs as red with
// cause judge_degraded.
func JudgeCalibrationDegraded(agreementRate float64) bool {
	return agreementRate < JudgeDegradedBelow
}

// NormalizeEvalCheck maps an absent check class to the backward-compatible
// default; unknown values are returned as-is so validation can reject them.
func NormalizeEvalCheck(check string) string {
	if strings.TrimSpace(check) == "" {
		return EvalCheckPatterns
	}
	return check
}

// JudgeConfig is the installation-wide judge model resolution (§6.2): the
// EVAL_JUDGE_MODEL value validated against the models of the configured
// providers. Empty or unknown ⇒ judge mode is off (configured=false).
type JudgeConfig struct {
	Model      string `json:"model"`
	ProviderID string `json:"provider_id,omitempty"`
	Configured bool   `json:"configured"`
}

// JudgeConfig resolves EVAL_JUDGE_MODEL against the configured providers.
func (s *Store) JudgeConfig(ctx context.Context) (JudgeConfig, error) {
	model := strings.TrimSpace(os.Getenv(EnvJudgeModel))
	if model == "" {
		return JudgeConfig{}, nil
	}
	providers, err := s.ListProviders(ctx)
	if err != nil {
		return JudgeConfig{}, err
	}
	for _, p := range providers {
		for _, candidate := range p.Models {
			if strings.TrimSpace(candidate) == model {
				return JudgeConfig{Model: model, ProviderID: p.ID, Configured: true}, nil
			}
		}
	}
	return JudgeConfig{Model: model, Configured: false}, nil
}

// EvalThresholds are the effective regression gates of a run (§6.5):
// the global minimum pass rate and the tolerated drop below the best
// historical run of the same version.
type EvalThresholds struct {
	MinPassRate         float64 `json:"min_pass_rate"`
	RegressionTolerance float64 `json:"regression_tolerance"`
}

// EffectiveEvaluationThresholds reads EVAL_MIN_PASS_RATE (default 1.0 — the
// pre-judge semantics: any failure fails the run) and
// EVAL_REGRESSION_TOLERANCE (default 0 — any drop beyond the best historical
// run of the version fails). Unparseable or out-of-range values fall back to
// the defaults: a broken knob must not widen the gate.
func EffectiveEvaluationThresholds() EvalThresholds {
	return EvalThresholds{
		MinPassRate:         envFloatClamped(EnvMinPassRate, 1.0, 0, 1),
		RegressionTolerance: envFloatClamped(EnvRegressionTolerance, 0, 0, 1),
	}
}

func envFloatClamped(name string, fallback, min, max float64) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}

// RunVerdictContext carries everything the verdict algorithm needs besides
// the case outcomes (§5.2): the judge state and the effective thresholds.
type RunVerdictContext struct {
	JudgeConfigured     bool
	Thresholds          EvalThresholds
	BestVersionPassRate float64 // best historical pass rate on this version; 0 = none
}

// ComputeRunVerdict is the single run verdict algorithm (§5.2, §6):
//
//   - error: a judge case while the judge is unconfigured, no cases, fewer
//     than 3 cases, or any case that ended in a model/judge error — an error
//     can never degrade into pass;
//   - fail:  any failed case, pass rate under EVAL_MIN_PASS_RATE, or a drop
//     beyond EVAL_REGRESSION_TOLERANCE against the best historical run of
//     the same version;
//   - pass:  everything green.
//
// The tested model never supplies any of these inputs beyond its answers,
// which the deterministic checks judge (§5.2). Degraded calibration is NOT
// folded into the verdict: a run with judge_degraded=true keeps its own
// verdict and the gate (M3) refuses it with cause judge_degraded (§6.4).
func ComputeRunVerdict(cases []EvaluationCaseResult, env RunVerdictContext) (verdict, cause string) {
	judgeCases := false
	for _, c := range cases {
		if CheckUsesJudge(NormalizeEvalCheck(c.Check)) {
			judgeCases = true
			break
		}
	}
	if judgeCases && !env.JudgeConfigured {
		return EvalVerdictError, CauseJudgeNotConfigured
	}
	if len(cases) == 0 {
		return EvalVerdictError, CauseSuiteEmpty
	}
	if len(cases) < 3 {
		return EvalVerdictError, CauseSuiteTooSmall
	}
	passed, failed := 0, 0
	for _, c := range cases {
		if c.Error != "" {
			if c.Cause != "" {
				return EvalVerdictError, c.Cause
			}
			return EvalVerdictError, CauseModelError
		}
		if c.Passed {
			passed++
		} else {
			failed++
		}
	}
	if failed > 0 {
		return EvalVerdictFail, ""
	}
	passRate := float64(passed) / float64(len(cases))
	if env.Thresholds.MinPassRate > 0 && passRate < env.Thresholds.MinPassRate {
		return EvalVerdictFail, CausePassRateBelowMin
	}
	if env.BestVersionPassRate > passRate && env.BestVersionPassRate-passRate > env.Thresholds.RegressionTolerance {
		return EvalVerdictFail, CausePassRegression
	}
	return EvalVerdictPass, ""
}

// finalizeAgentEvaluationRun computes the server-side verdict block of a run
// record: case counts are recomputed from the outcomes, the judge state is
// read from configuration and calibration history, the threshold snapshot is
// built and the verdict algorithm decides pass/fail/error. Client-supplied
// verdict fields are overwritten — the request never decides.
func (s *Store) finalizeAgentEvaluationRun(ctx context.Context, run *AgentEvaluationRun) error {
	passed, failed := 0, 0
	for _, c := range run.Cases {
		switch {
		case c.Error != "":
		case c.Passed:
			passed++
		default:
			failed++
		}
	}
	run.Passed, run.Failed = passed, failed

	judgeCases := false
	for _, c := range run.Cases {
		if CheckUsesJudge(NormalizeEvalCheck(c.Check)) {
			judgeCases = true
			break
		}
	}
	thresholds := EffectiveEvaluationThresholds()
	snapshot := map[string]any{
		"min_pass_rate":        thresholds.MinPassRate,
		"regression_tolerance": thresholds.RegressionTolerance,
	}
	env := RunVerdictContext{Thresholds: thresholds}
	if judgeCases {
		config, err := s.JudgeConfig(ctx)
		if err != nil {
			return err
		}
		env.JudgeConfigured = config.Configured
		if config.Configured {
			run.JudgeModel = config.Model
			run.JudgeSameModel = JudgeSameModel(run.TestedModel, config.Model)
			if calibration, err := s.latestJudgeCalibration(ctx, config.Model); err != nil {
				return err
			} else {
				run.JudgeDegraded = calibration.Degraded
			}
			// Snapshot the per-case judge thresholds that governed this run
			// (§5.1) from the suite the runner read.
			if suite, err := s.GetAgentEvaluationSuite(ctx, run.AgentID); err == nil {
				thresholdsByCase := map[string]int{}
				for _, c := range suite.Cases {
					if CheckUsesJudge(NormalizeEvalCheck(c.Check)) && c.JudgeThreshold != nil {
						thresholdsByCase[c.Name] = *c.JudgeThreshold
					}
				}
				if len(thresholdsByCase) > 0 {
					snapshot["judge_thresholds"] = thresholdsByCase
				}
			}
		}
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	var snapshotMap map[string]any
	if err := json.Unmarshal(snapshotJSON, &snapshotMap); err != nil {
		return err
	}
	run.Threshold = snapshotMap

	best, err := s.bestAgentEvaluationPassRate(ctx, run.AgentID, run.AgentVersion)
	if err != nil {
		return err
	}
	env.BestVersionPassRate = best
	run.Verdict, run.Cause = ComputeRunVerdict(run.Cases, env)
	return nil
}

// bestAgentEvaluationPassRate returns the best historical pass rate on an
// agent definition version (§6.5); error runs are excluded — their rates
// carry no signal. The run being recorded is not inserted yet, so it never
// compares against itself.
func (s *Store) bestAgentEvaluationPassRate(ctx context.Context, agentID string, version int) (float64, error) {
	var best *float64
	err := s.pool.QueryRow(ctx,
		`SELECT MAX(passed::float8 / (passed + failed))
		 FROM workspace_agent_evaluation_run
		 WHERE agent_id = $1 AND agent_version = $2 AND verdict IN ('pass','fail') AND passed + failed > 0`,
		agentID, version).Scan(&best)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if best == nil {
		return 0, nil
	}
	return *best, nil
}

// JudgeCalibrationCase is one entry of the fixed calibration set (§6.4): a
// suite case, the answer a historical run produced for it and the verdict
// that answer is known to deserve. Labels are human-first (§3, M1): a
// golden case whose source run a human corrected flips to fail — the human
// evidence outranks the pattern verdict; cases without human signal keep
// their recorded pattern verdict.
type JudgeCalibrationCase struct {
	Name        string `json:"name"`
	Input       string `json:"input"`
	Answer      string `json:"answer"`
	Label       bool   `json:"label"`
	LabelSource string `json:"label_source"` // human | patterns
	JudgePrompt string `json:"judge_prompt,omitempty"`
}

// JudgeCalibrationScore is one judged calibration entry (snapshot row).
type JudgeCalibrationScore struct {
	Name  string `json:"name"`
	Label bool   `json:"label"`
	Score int    `json:"score"`
	Match bool   `json:"match"`
}

// JudgeCalibration is a stored calibration result (§6.4): agreement of one
// judge model against known verdicts, kept as history next to judge cases.
type JudgeCalibration struct {
	ID               int64                   `json:"id"`
	AgentID          string                  `json:"agent_id"`
	JudgeModel       string                  `json:"judge_model"`
	AgreementRate    float64                 `json:"agreement_rate"`
	MeanAbsDeviation float64                 `json:"mean_abs_deviation"`
	Histogram        []int                   `json:"histogram"`
	Degraded         bool                    `json:"degraded"`
	CasesChecked     int                     `json:"cases_checked"`
	CasesSnapshot    []JudgeCalibrationScore `json:"cases_snapshot"`
	CreatedBy        string                  `json:"created_by,omitempty"`
	CheckedAt        time.Time               `json:"checked_at"`
}

// ComputeCalibrationScores reduces judged calibration entries to the §6.4
// result: agreement rate (share of verdicts the judge got right), mean
// absolute deviation from the known verdicts (100 for pass labels, 0 for
// fail labels) and the score histogram in ten buckets.
func ComputeCalibrationScores(scores []JudgeCalibrationScore) (agreement float64, meanAbsDeviation float64, histogram []int) {
	histogram = make([]int, 10)
	if len(scores) == 0 {
		return 0, 0, histogram
	}
	matches, deviation := 0, 0.0
	for _, s := range scores {
		if s.Match {
			matches++
		}
		target := 0.0
		if s.Label {
			target = 100
		}
		deviation += absFloat(float64(s.Score) - target)
		bucket := s.Score / 10
		if bucket > 9 {
			bucket = 9
		}
		if bucket < 0 {
			bucket = 0
		}
		histogram[bucket]++
	}
	return float64(matches) / float64(len(scores)), deviation / float64(len(scores)), histogram
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// SaveJudgeCalibration appends one calibration result.
func (s *Store) SaveJudgeCalibration(ctx context.Context, calibration *JudgeCalibration) error {
	if calibration.Histogram == nil {
		calibration.Histogram = []int{}
	}
	if calibration.CasesSnapshot == nil {
		calibration.CasesSnapshot = []JudgeCalibrationScore{}
	}
	histogram, err := json.Marshal(calibration.Histogram)
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(calibration.CasesSnapshot)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_judge_calibration
		   (agent_id, judge_model, agreement_rate, mean_abs_deviation, histogram, degraded, cases_checked, cases_snapshot, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id, checked_at`,
		calibration.AgentID, calibration.JudgeModel, calibration.AgreementRate, calibration.MeanAbsDeviation,
		histogram, calibration.Degraded, calibration.CasesChecked, snapshot, calibration.CreatedBy).
		Scan(&calibration.ID, &calibration.CheckedAt)
}

// ListJudgeCalibrations returns recent calibration results for an agent.
func (s *Store) ListJudgeCalibrations(ctx context.Context, agentID string, limit int) ([]JudgeCalibration, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, agent_id, judge_model, agreement_rate, mean_abs_deviation, histogram, degraded,
		        cases_checked, cases_snapshot, created_by, checked_at
		 FROM workspace_judge_calibration WHERE agent_id = $1 ORDER BY checked_at DESC LIMIT $2`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []JudgeCalibration
	for rows.Next() {
		var c JudgeCalibration
		var histogram, snapshot []byte
		if err := rows.Scan(&c.ID, &c.AgentID, &c.JudgeModel, &c.AgreementRate, &c.MeanAbsDeviation, &histogram,
			&c.Degraded, &c.CasesChecked, &snapshot, &c.CreatedBy, &c.CheckedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(histogram, &c.Histogram)
		_ = json.Unmarshal(snapshot, &c.CasesSnapshot)
		result = append(result, c)
	}
	return result, rows.Err()
}

// latestJudgeCalibration returns the most recent calibration of a judge
// model (any agent); no calibration yet is (zero, nil) — an uncalibrated
// judge is not degraded, calibration is what proves it (§6.4).
func (s *Store) latestJudgeCalibration(ctx context.Context, judgeModel string) (JudgeCalibration, error) {
	var c JudgeCalibration
	var histogram, snapshot []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, agent_id, judge_model, agreement_rate, mean_abs_deviation, histogram, degraded,
		        cases_checked, cases_snapshot, created_by, checked_at
		 FROM workspace_judge_calibration WHERE judge_model = $1 ORDER BY checked_at DESC LIMIT 1`, judgeModel).
		Scan(&c.ID, &c.AgentID, &c.JudgeModel, &c.AgreementRate, &c.MeanAbsDeviation, &histogram,
			&c.Degraded, &c.CasesChecked, &snapshot, &c.CreatedBy, &c.CheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return JudgeCalibration{}, nil
	}
	if err != nil {
		return c, err
	}
	_ = json.Unmarshal(histogram, &c.Histogram)
	_ = json.Unmarshal(snapshot, &c.CasesSnapshot)
	return c, nil
}

// AgentJudgeCalibrationSet builds the fixed calibration set of an agent
// (§6.4): for each current suite case, the most recent recorded answer a
// run produced for it, labelled human-first — a golden case whose source
// run a human decided (M1 projection, verdicts.go) takes the human verdict,
// anything else keeps the recorded pattern verdict. Cases without a usable
// historical answer are skipped; the set is capped at limit entries in
// suite order so repeated calibrations compare the same material.
func (s *Store) AgentJudgeCalibrationSet(ctx context.Context, agentID string, limit int) ([]JudgeCalibrationCase, error) {
	if limit <= 0 || limit > JudgeCalibrationCaseMax {
		limit = JudgeCalibrationCaseMax
	}
	suite, err := s.GetAgentEvaluationSuite(ctx, agentID)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT cases FROM workspace_agent_evaluation_run
		 WHERE agent_id = $1 AND jsonb_array_length(cases) > 0
		 ORDER BY created_at DESC LIMIT 25`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	latest := map[string]EvaluationCaseResult{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var results []EvaluationCaseResult
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &results); err != nil {
				return nil, err
			}
		}
		for _, r := range results {
			if _, seen := latest[r.Name]; !seen {
				latest[r.Name] = r
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	set := make([]JudgeCalibrationCase, 0, limit)
	for _, c := range suite.Cases {
		result, ok := latest[c.Name]
		if !ok || strings.TrimSpace(result.Answer) == "" || result.Error != "" {
			continue
		}
		entry := JudgeCalibrationCase{
			Name:        c.Name,
			Input:       c.Input,
			Answer:      result.Answer,
			Label:       result.Passed,
			LabelSource: "patterns",
			JudgePrompt: c.JudgePrompt,
		}
		if strings.HasPrefix(c.Name, "golden/") {
			if outcome, oErr := s.goldenRunOutcome(ctx, agentID, c.Name); oErr == nil {
				switch outcome {
				case RunOutcomeConfirmed:
					entry.Label, entry.LabelSource = true, "human"
				case RunOutcomeHumanCorrected:
					entry.Label, entry.LabelSource = false, "human"
				}
			}
		}
		set = append(set, entry)
		if len(set) >= limit {
			break
		}
	}
	return set, nil
}

// goldenRunOutcome resolves the source run of a golden case
// (golden/<run_id-prefix>, §4.2) and projects its human verdicts (M1,
// hitlverdict.go) into a run outcome.
func (s *Store) goldenRunOutcome(ctx context.Context, agentID, caseName string) (string, error) {
	prefix := strings.TrimPrefix(caseName, "golden/")
	if prefix == "" {
		return "", fmt.Errorf("empty golden run prefix")
	}
	var runID, projectID, status string
	err := s.pool.QueryRow(ctx,
		`SELECT run_id, project_id, status FROM workspace_run
		 WHERE agent_id = $1 AND run_id LIKE $2 || '%'
		 ORDER BY created_at DESC LIMIT 1`, agentID, prefix).
		Scan(&runID, &projectID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	verdicts, err := s.RunVerdicts(ctx, projectID, runID)
	if err != nil {
		return "", err
	}
	return ClassifyRunOutcome(status, verdicts), nil
}
