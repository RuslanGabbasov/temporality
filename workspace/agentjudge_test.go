package workspace

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestComputeRunVerdictJudgeNotConfigured pins §6.2: a suite with judge
// cases on an installation without a valid EVAL_JUDGE_MODEL records the run
// as error with cause judge_not_configured — never pass, never a silent
// downgrade to patterns.
func TestComputeRunVerdictJudgeNotConfigured(t *testing.T) {
	cases := []EvaluationCaseResult{
		{Name: "a", Passed: true, Check: EvalCheckPatterns},
		{Name: "b", Passed: true, Check: EvalCheckJudge, JudgeScore: intPtr(90)},
		{Name: "c", Passed: true, Check: EvalCheckPatternsJudge, JudgeScore: intPtr(80)},
	}
	verdict, cause := ComputeRunVerdict(cases, RunVerdictContext{
		JudgeConfigured: false,
		Thresholds:      EvalThresholds{MinPassRate: 1.0},
	})
	require.Equal(t, EvalVerdictError, verdict)
	require.Equal(t, CauseJudgeNotConfigured, cause)

	// Patterns-only suites are unaffected by the missing judge (§6.1
	// backward compatibility); the suite still must satisfy the §5.2
	// minimum of three cases.
	patternsOnly := []EvaluationCaseResult{
		{Name: "p1", Passed: true, Check: EvalCheckPatterns},
		{Name: "p2", Passed: true, Check: EvalCheckPatterns},
		{Name: "p3", Passed: true, Check: EvalCheckPatterns},
	}
	verdict, cause = ComputeRunVerdict(patternsOnly, RunVerdictContext{
		JudgeConfigured: false,
		Thresholds:      EvalThresholds{MinPassRate: 1.0},
	})
	require.Equal(t, EvalVerdictPass, verdict)
	require.Empty(t, cause)
}

// TestJudgeSameModel pins the §11.1 self-bias marker: equal models flag,
// different models and empty tested models (legacy rows) do not.
func TestJudgeSameModel(t *testing.T) {
	require.True(t, JudgeSameModel("gpt-x", "gpt-x"))
	require.True(t, JudgeSameModel(" gpt-x ", "gpt-x"))
	require.False(t, JudgeSameModel("gpt-x", "gpt-y"))
	require.False(t, JudgeSameModel("", "gpt-x"))
	require.False(t, JudgeSameModel("gpt-x", ""))
}

// TestJudgeCalibrationDegraded pins the §6.4 floor: agreement under 0.7 is
// degraded, 0.7 exactly is not.
func TestJudgeCalibrationDegraded(t *testing.T) {
	require.True(t, JudgeCalibrationDegraded(0.69))
	require.False(t, JudgeCalibrationDegraded(0.7))
	require.False(t, JudgeCalibrationDegraded(1.0))
}

// TestCalibrationAgreementBelowFloorIsDegraded pins the calibration
// reduction of §6.4: 3 of 5 agreements = 0.6 → degraded, with the mean
// absolute deviation from the known verdicts and a ten-bucket histogram.
func TestCalibrationAgreementBelowFloorIsDegraded(t *testing.T) {
	scores := []JudgeCalibrationScore{
		{Name: "ok-1", Label: true, Score: 90, Match: true},
		{Name: "ok-2", Label: true, Score: 80, Match: true},
		{Name: "ok-3", Label: false, Score: 10, Match: true},
		{Name: "bad-1", Label: true, Score: 20, Match: false},
		{Name: "bad-2", Label: false, Score: 70, Match: false},
	}
	agreement, meanAbsDeviation, histogram := ComputeCalibrationScores(scores)
	require.InDelta(t, 0.6, agreement, 1e-9)
	require.True(t, JudgeCalibrationDegraded(agreement))
	// deviations: 10 + 20 + 10 + 80 + 70 = 190 / 5 = 38
	require.InDelta(t, 38.0, meanAbsDeviation, 1e-9)
	require.Len(t, histogram, 10)
	require.Equal(t, 1, histogram[1]) // score 10
	require.Equal(t, 1, histogram[2]) // score 20
	require.Equal(t, 1, histogram[7])
	require.Equal(t, 1, histogram[8])
	require.Equal(t, 1, histogram[9])

	agreement, _, _ = ComputeCalibrationScores(scores[:4])
	require.InDelta(t, 0.75, agreement, 1e-9)
	require.False(t, JudgeCalibrationDegraded(agreement))
}

// TestComputeRunVerdictJudgeErrors pins §6.2/§5.2: a case that ended in a
// judge failure makes the whole run error with that cause — invalid JSON
// twice stays judge_invalid_json, and an error run can never degrade into
// pass or fail.
func TestComputeRunVerdictJudgeErrors(t *testing.T) {
	invalidJSON := []EvaluationCaseResult{
		{Name: "a", Passed: true, Check: EvalCheckPatterns},
		{Name: "b", Passed: false, Check: EvalCheckJudge, Error: "judge returned invalid JSON twice: no JSON object", Cause: CauseJudgeInvalidJSON},
		{Name: "c", Passed: true, Check: EvalCheckPatterns},
	}
	verdict, cause := ComputeRunVerdict(invalidJSON, RunVerdictContext{
		JudgeConfigured: true,
		Thresholds:      EvalThresholds{MinPassRate: 1.0},
	})
	require.Equal(t, EvalVerdictError, verdict)
	require.Equal(t, CauseJudgeInvalidJSON, cause)

	callFailed := []EvaluationCaseResult{
		{Name: "a", Passed: true, Check: EvalCheckPatterns},
		{Name: "b", Passed: false, Check: EvalCheckPatternsJudge, Error: "judge call failed: timeout", Cause: CauseJudgeError},
		{Name: "c", Passed: true, Check: EvalCheckPatterns},
	}
	verdict, cause = ComputeRunVerdict(callFailed, RunVerdictContext{
		JudgeConfigured: true,
		Thresholds:      EvalThresholds{MinPassRate: 1.0},
	})
	require.Equal(t, EvalVerdictError, verdict)
	require.Equal(t, CauseJudgeError, cause)
}

// TestComputeRunVerdictPatternsPlusJudge pins §6.3 determinism at the
// verdict layer: a failed pattern check fails the case even with a top
// judge score — the recorded outcome, not the judge, decides.
func TestComputeRunVerdictPatternsPlusJudge(t *testing.T) {
	cases := []EvaluationCaseResult{
		{Name: "a", Passed: true, Check: EvalCheckPatterns},
		{Name: "b", Passed: false, Check: EvalCheckPatternsJudge, JudgeScore: intPtr(95)},
		{Name: "c", Passed: true, Check: EvalCheckPatterns},
	}
	verdict, cause := ComputeRunVerdict(cases, RunVerdictContext{
		JudgeConfigured: true,
		Thresholds:      EvalThresholds{MinPassRate: 1.0},
	})
	require.Equal(t, EvalVerdictFail, verdict)
	require.Empty(t, cause)
}
