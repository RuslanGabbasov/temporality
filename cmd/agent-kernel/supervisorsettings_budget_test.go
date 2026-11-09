package main

// Unit tests for the harness-budget edit surface of PUT /v1/workspace/supervisor
// (docs/harness-settings.md §6): key/value validation, reset semantics and the
// cross-knob consistency checks.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/harness"
)

func ptr(value int) *int { return &value }

func TestHarnessBudgetEdits(t *testing.T) {
	sets, resets, err := harnessBudgetEdits(map[string]*int{
		"tool_output_tail_bytes": ptr(4096),
		"compaction_tail_bytes":  nil, // null = reset to env/default
	})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"tool_output_tail_bytes": 4096}, sets)
	require.Equal(t, []string{"compaction_tail_bytes"}, resets)

	// Omitted keys are untouched.
	sets, resets, err = harnessBudgetEdits(nil)
	require.NoError(t, err)
	require.Empty(t, sets)
	require.Empty(t, resets)

	// Unknown keys reject the whole payload.
	_, _, err = harnessBudgetEdits(map[string]*int{"model_call_activity_timeout": ptr(60)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown budget knob")

	// Out-of-range values: zero, negative, past the per-knob ceiling.
	_, _, err = harnessBudgetEdits(map[string]*int{"tool_output_tail_bytes": ptr(0)})
	require.Error(t, err)
	_, _, err = harnessBudgetEdits(map[string]*int{"tool_output_tail_bytes": ptr(-5)})
	require.Error(t, err)
	_, _, err = harnessBudgetEdits(map[string]*int{"max_model_request_bytes": ptr(2<<20 + 1)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_model_request_bytes")
	// The hard ceiling still accepts the exact Temporal payload limit.
	sets, _, err = harnessBudgetEdits(map[string]*int{"max_model_request_bytes": ptr(2 << 20)})
	require.NoError(t, err)
	require.Equal(t, 2<<20, sets["max_model_request_bytes"])
	// A count knob has a count ceiling, not the byte one.
	_, _, err = harnessBudgetEdits(map[string]*int{"compaction_keep_groups_max": ptr(65)})
	require.Error(t, err)
}

func TestCheckBudgetConsistency(t *testing.T) {
	require.NoError(t, checkBudgetConsistency(harness.DefaultBudgets()))

	b := harness.DefaultBudgets()
	b.CompactionKeepGroupsMin, b.CompactionKeepGroupsMax = 9, 3
	require.Error(t, checkBudgetConsistency(b))

	b = harness.DefaultBudgets()
	b.CompactionTriggerBytes = b.MaxModelRequestBytes + 1
	require.Error(t, checkBudgetConsistency(b))
}
