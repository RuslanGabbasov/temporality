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
