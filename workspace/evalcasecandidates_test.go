package workspace

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEvalCandidateSelection covers the §4.2 v1 rules on every status and
// verdict combination that matters: success with clean verdicts, corrections
// taking precedence, and every exclusion.
func TestEvalCandidateSelection(t *testing.T) {
	accepted := HITLVerdict{Type: VerdictAccepted}
	approved := HITLVerdict{Source: VerdictSourceApproval, Type: VerdictAccepted}
	corrected := HITLVerdict{Type: VerdictCorrected, Answer: "the fixed answer"}
	emptyCorrection := HITLVerdict{Type: VerdictCorrected, Answer: "   "}
	rejected := HITLVerdict{Type: VerdictRejected}

	cases := []struct {
		name     string
		status   string
		answer   string
		verdicts []HITLVerdict
		wantOK   bool
		wantSrc  string
		wantExp  string
	}{
		{"clean succeeded run", "completed", "the answer", nil, true, EvalCandidateSourceSucceededRun, "the answer"},
		{"spec literal succeeded status", "succeeded", "the answer", nil, true, EvalCandidateSourceSucceededRun, "the answer"},
		{"accepted verdicts stay succeeded_run", "completed", "a", []HITLVerdict{accepted, accepted}, true, EvalCandidateSourceSucceededRun, "a"},
		{"human approval is not an edit", "completed", "a", []HITLVerdict{approved}, true, EvalCandidateSourceSucceededRun, "a"},
		{"correction wins over success", "completed", "agent answer", []HITLVerdict{accepted, corrected}, true, EvalCandidateSourceHumanCorrected, "the fixed answer"},
		{"failed run with a correction", "failed", "", []HITLVerdict{corrected}, true, EvalCandidateSourceHumanCorrected, "the fixed answer"},
		{"terminal forced finale with correction", "turn_limit", "", []HITLVerdict{corrected}, true, EvalCandidateSourceHumanCorrected, "the fixed answer"},
		{"rejected verdict disqualifies", "completed", "a", []HITLVerdict{rejected}, false, "", ""},
		{"rejected beats correction", "completed", "a", []HITLVerdict{corrected, rejected}, false, "", ""},
		{"cancelled never harvests", "cancelled", "a", nil, false, "", ""},
		{"running never harvests", "started", "a", nil, false, "", ""},
		{"failed without correction", "failed", "partial", nil, false, "", ""},
		{"forced finale without correction", "turn_limit", "a", nil, false, "", ""},
		{"empty answer", "completed", "", nil, false, "", ""},
		{"correction without text", "completed", "a", []HITLVerdict{emptyCorrection}, false, "", ""},
		{"oversized answer", "completed", strings.Repeat("x", evalCandidateMaxAnswer+1), nil, false, "", ""},
		{"boundary answer allowed", "completed", strings.Repeat("x", evalCandidateMaxAnswer), nil, true, EvalCandidateSourceSucceededRun, strings.Repeat("x", evalCandidateMaxAnswer)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, expected, ok := evalCandidateSelection(tc.status, tc.answer, tc.verdicts)
			require.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}
			require.Equal(t, tc.wantSrc, source)
			require.Equal(t, tc.wantExp, expected)
		})
	}
}

// TestEvalProjectShareAllows pins the §4.5 70 % anti-contamination rule,
// including the bootstrap exception and the exact boundary (70 % passes).
func TestEvalProjectShareAllows(t *testing.T) {
	require.True(t, evalProjectShareAllows(nil, "p1"), "first golden must always be acceptable")
	require.True(t, evalProjectShareAllows(map[string]int{"p1": 1}, "p2"))
	require.False(t, evalProjectShareAllows(map[string]int{"p1": 1}, "p1"), "2 of 2 from one project exceeds 70%")
	// 6 of 9 already from p1: the 7th makes 7 of 10 — exactly 70 %, allowed.
	require.True(t, evalProjectShareAllows(map[string]int{"p1": 6, "p2": 3}, "p1"))
	// 7 of 9 already from p1: the 8th makes 8 of 10 — over the ceiling.
	require.False(t, evalProjectShareAllows(map[string]int{"p1": 7, "p2": 3}, "p1"))
	require.True(t, evalProjectShareAllows(map[string]int{"p1": 6, "p2": 3}, "p2"))
}

// TestGoldenCaseName pins the §4.4 suite naming: golden/<run_id-prefix>.
func TestGoldenCaseName(t *testing.T) {
	require.Equal(t, "golden/abcd1234", GoldenCaseName("abcd1234-efgh-5678"))
	require.Equal(t, "golden/short", GoldenCaseName("short"))
	require.Equal(t, "golden/12345678", GoldenCaseName("12345678"))
}

// TestPrepareCandidateMasksBeforeInsert is the §4.3 write-path guarantee:
// every candidate passes the redaction funnel before storage — secret-like
// text arrives masked with redaction_applied=true and a fragment count,
// clean text passes through untouched with the flag false.
func TestPrepareCandidateMasksBeforeInsert(t *testing.T) {
	t.Run("secret-like text is masked", func(t *testing.T) {
		c := EvalCaseCandidate{
			Input:          "connect with token=supersecret123 then retry",
			ExpectedOutput: "ran `export password=hunter2` successfully",
		}
		prepareEvalCaseCandidateForInsert(&c)
		require.NotContains(t, c.Input, "supersecret123")
		require.Contains(t, c.Input, "token=[REDACTED]")
		require.NotContains(t, c.ExpectedOutput, "hunter2")
		require.True(t, c.RedactionApplied)
		require.GreaterOrEqual(t, c.Redactions, 2)
		require.Equal(t, EvalCandidateVerdictPending, c.Verdict)
	})

	t.Run("clean text is untouched", func(t *testing.T) {
		c := EvalCaseCandidate{Input: "summarize the quarterly report", ExpectedOutput: "The report shows 12 % growth."}
		prepareEvalCaseCandidateForInsert(&c)
		require.Equal(t, "summarize the quarterly report", c.Input)
		require.Equal(t, "The report shows 12 % growth.", c.ExpectedOutput)
		require.False(t, c.RedactionApplied)
		require.Zero(t, c.Redactions)
	})
}

// TestRedactCandidateText masks every occurrence, not only the first (the
// kernel primitive stops at one match), and keeps free-form text intact.
func TestRedactCandidateText(t *testing.T) {
	got, n := redactCandidateText("token=alpha and token=beta")
	require.Equal(t, "token=[REDACTED] and token=[REDACTED]", got)
	require.Equal(t, 2, n)

	got, n = redactCandidateText("https://user:pass@example.com/path")
	require.Equal(t, "https://[REDACTED]@example.com/path", got)
	require.Equal(t, 1, n)

	got, n = redactCandidateText(`use token="with spaces" please`)
	require.Equal(t, `use token=[REDACTED] please`, got)
	require.Equal(t, 1, n)

	// A leading bearer credential is masked without dropping the rest of the
	// field (the kernel copy truncates the tail — wrong for candidate text).
	got, n = redactCandidateText("Bearer abc123\ncurl https://api.example.com")
	require.Equal(t, "Bearer [REDACTED]\ncurl https://api.example.com", got)
	require.Equal(t, 1, n)

	got, n = redactCandidateText("api_key: ZZZ")
	require.Equal(t, "api_key: [REDACTED]", got)
	require.Equal(t, 1, n)

	got, n = redactCandidateText("nothing sensitive here")
	require.Equal(t, "nothing sensitive here", got)
	require.Zero(t, n)
}

// TestUncertainEffectFromEvents mirrors the §4.2 effect=uncertain exclusion
// against the journal shapes the kernel produces.
func TestUncertainEffectFromEvents(t *testing.T) {
	started := harvestToolEvent{Type: "tool.started", OperationID: "op/1"}
	completed := harvestToolEvent{Type: "tool.completed", OperationID: "op/1"}
	failedNone := harvestToolEvent{Type: "tool.failed", OperationID: "op/1", Effect: "none"}
	failedUncertain := harvestToolEvent{Type: "tool.failed", OperationID: "op/1", Effect: "uncertain"}
	failedLegacy := harvestToolEvent{Type: "tool.failed", OperationID: "op/1", ErrorType: "activity_failed"}
	blocked := harvestToolEvent{Type: "tool.blocked", OperationID: "op/1"}
	reconciled := harvestToolEvent{Type: "operation.reconciled", OperationID: "op/1"}

	require.False(t, uncertainEffectFromEvents(nil))
	require.False(t, uncertainEffectFromEvents([]harvestToolEvent{started, completed}))
	require.False(t, uncertainEffectFromEvents([]harvestToolEvent{started, failedNone}))
	require.False(t, uncertainEffectFromEvents([]harvestToolEvent{started, blocked}))
	require.False(t, uncertainEffectFromEvents([]harvestToolEvent{started, reconciled}))
	require.True(t, uncertainEffectFromEvents([]harvestToolEvent{started}), "never-settled operation is uncertain")
	require.True(t, uncertainEffectFromEvents([]harvestToolEvent{started, failedUncertain}))
	require.True(t, uncertainEffectFromEvents([]harvestToolEvent{started, failedLegacy}))
	require.False(t, uncertainEffectFromEvents([]harvestToolEvent{{Type: "tool.started"}}), "events without operation id are ignored")
}

// TestEvalRunStatuses pins which run states are harvestable at all.
func TestEvalRunStatuses(t *testing.T) {
	for _, status := range []string{"completed", "succeeded", "failed", "turn_limit", "time_limit", "token_limit", "budget_limit"} {
		require.True(t, evalRunIsTerminal(status), status)
	}
	for _, status := range []string{"started", "pending", "running", "cancelled", "canceled", ""} {
		require.False(t, evalRunIsTerminal(status), status)
	}
	require.True(t, evalRunSucceeded("completed"))
	require.True(t, evalRunSucceeded("succeeded"))
	require.False(t, evalRunSucceeded("failed"))
}
