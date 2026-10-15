package workspace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEvalSuiteStaleCases pins the §13 option B drift sweep: only cases
// with a passed stale_after count, held-out cases never decide the gate
// (§4.5) and stay out of the list, and the boundary is exclusive — a case
// expiring exactly now is not yet stale.
func TestEvalSuiteStaleCases(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Second)
	future := now.Add(time.Second)
	cases := []EvaluationCase{
		{Name: "expired", StaleAfter: &past},
		{Name: "boundary", StaleAfter: &now},
		{Name: "fresh", StaleAfter: &future},
		{Name: "no-deadline"},
		{Name: "held-out-expired", HeldOut: true, StaleAfter: &past},
	}
	require.Equal(t, []string{"expired"}, EvalSuiteStaleCases(cases, now))
	require.Empty(t, EvalSuiteStaleCases(nil, now))
}

// TestGateSuiteReadiness pins the §7.1 deterministic misconfiguration sweep:
// the same causes ComputeRunVerdict would turn into error verdicts refuse
// the apply up front, held-out cases never count toward readiness, and an
// unconfigured judge wins over size — it is the cause the owner fixes first.
func TestGateSuiteReadiness(t *testing.T) {
	three := []EvaluationCase{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	require.Empty(t, GateSuiteReadiness(three, false)) // pattern suite never needs a judge

	require.Equal(t, CauseSuiteEmpty, GateSuiteReadiness(nil, true))
	require.Equal(t, CauseSuiteEmpty, GateSuiteReadiness([]EvaluationCase{{Name: "held", HeldOut: true}}, true))

	twoPlusHeldOut := []EvaluationCase{{Name: "a"}, {Name: "b"}, {Name: "held", HeldOut: true}}
	require.Equal(t, CauseSuiteTooSmall, GateSuiteReadiness(twoPlusHeldOut, true))

	judged := []EvaluationCase{{Name: "a", Check: "judge"}, {Name: "b"}, {Name: "c"}}
	require.Equal(t, CauseJudgeNotConfigured, GateSuiteReadiness(judged, false))
	require.Equal(t, CauseJudgeNotConfigured, GateSuiteReadiness([]EvaluationCase{{Name: "a", Check: "judge"}, {Name: "b"}}, false)) // judge before size
	require.Empty(t, GateSuiteReadiness(judged, true))
}
