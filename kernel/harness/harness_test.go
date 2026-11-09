package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBudgetKeysMatchStruct(t *testing.T) {
	// Every knob the UI can edit must be applied by ApplyBudgetOverrides:
	// a key missing from either side would silently no-op.
	keys := BudgetKeys()
	require.Len(t, keys, 11)
	seen := map[string]bool{}
	for _, key := range keys {
		require.False(t, seen[key], "duplicate key %s", key)
		seen[key] = true
	}
	s := ApplyBudgetOverrides(Defaults(), map[string]int{keys[0]: 1, keys[len(keys)-1]: 2})
	require.Equal(t, 1, s.Budgets.MaxModelRequestBytes)
	require.Equal(t, 2, s.Budgets.ContinueAsNewBytes)
}

func TestApplyBudgetOverrides(t *testing.T) {
	base := Defaults()
	base.ModelCallActivityTimeout = 0 // budgets only; timeouts untouched

	s := ApplyBudgetOverrides(base, map[string]int{
		"tool_output_tail_bytes":     4096,
		"compaction_keep_groups_min": 4,
		"not_a_real_knob":            99,
	})
	require.Equal(t, 4096, s.Budgets.ToolOutputTailBytes)
	require.Equal(t, 4, s.Budgets.CompactionKeepGroupsMin)
	// Unknown keys ignored: a stale row degrades instead of breaking the boot.
	require.Equal(t, DefaultBudgets().ToolOutputMaxBytes, s.Budgets.ToolOutputMaxBytes)
	require.Equal(t, DefaultBudgets().CompactionKeepGroupsMax, s.Budgets.CompactionKeepGroupsMax)
	require.Equal(t, DefaultBudgets().MaxModelRequestBytes, s.Budgets.MaxModelRequestBytes)
	// Non-budget fields ride through unchanged.
	require.Zero(t, s.ModelCallActivityTimeout)

	// Empty overrides are a no-op.
	same := ApplyBudgetOverrides(base, nil)
	require.Equal(t, base.Budgets, same.Budgets)
}
