package workspace

// Live-database round-trip for the harness budget overrides
// (docs/harness-settings.md §6): set/reset semantics against the real
// workspace_setting KV. Skipped unless TEMPORALITY_TEST_DATABASE_URL points
// at a migrated schema (same contract as agentevolution_live_test.go).

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHarnessBudgetOverridesLive(t *testing.T) {
	dsn := os.Getenv("TEMPORALITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEMPORALITY_TEST_DATABASE_URL not set")
	}
	store, err := Open(context.Background(), dsn)
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	// Start clean: drop the rows this test may leave from a previous run.
	require.NoError(t, store.SetHarnessBudgetOverrides(ctx, nil,
		[]string{"tool_output_tail_bytes", "compaction_tail_bytes"}))

	overrides, err := store.HarnessBudgetOverrides(ctx)
	require.NoError(t, err)
	require.NotContains(t, overrides, "tool_output_tail_bytes")

	require.NoError(t, store.SetHarnessBudgetOverrides(ctx,
		map[string]int{"tool_output_tail_bytes": 4096, "compaction_tail_bytes": 8192}, nil))
	overrides, err = store.HarnessBudgetOverrides(ctx)
	require.NoError(t, err)
	require.Equal(t, 4096, overrides["tool_output_tail_bytes"])
	require.Equal(t, 8192, overrides["compaction_tail_bytes"])

	// Overwrite one, reset the other.
	require.NoError(t, store.SetHarnessBudgetOverrides(ctx,
		map[string]int{"tool_output_tail_bytes": 2048}, []string{"compaction_tail_bytes"}))
	overrides, err = store.HarnessBudgetOverrides(ctx)
	require.NoError(t, err)
	require.Equal(t, 2048, overrides["tool_output_tail_bytes"])
	require.NotContains(t, overrides, "compaction_tail_bytes")

	// Reset is idempotent.
	require.NoError(t, store.SetHarnessBudgetOverrides(ctx, nil,
		[]string{"tool_output_tail_bytes", "compaction_tail_bytes"}))
	overrides, err = store.HarnessBudgetOverrides(ctx)
	require.NoError(t, err)
	require.NotContains(t, overrides, "tool_output_tail_bytes")
}
