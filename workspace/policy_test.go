package workspace

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }

// MergePolicies is the inheritance core of §24: allowlists intersect, caps
// take the minimum, restrictive modes win — a child can never loosen what an
// ancestor set, and absent dimensions never restrict.
func TestMergePolicies(t *testing.T) {
	t.Run("empty input is unrestricted", func(t *testing.T) {
		effective, sources := MergePolicies(nil)
		require.Empty(t, sources)
		require.Nil(t, effective.AllowedModels)
		require.Nil(t, effective.AllowedMCP)
		require.Nil(t, effective.MaxTokens)
		require.Nil(t, effective.MaxBudgetUSD)
		require.Nil(t, effective.TimeoutSeconds)
		require.Empty(t, effective.NetworkMode)
		require.Empty(t, effective.SandboxMode)
		require.Empty(t, effective.ApprovalMode)
		require.Empty(t, effective.IntakeMode)
	})

	t.Run("disabled rows are skipped", func(t *testing.T) {
		effective, sources := MergePolicies([]Policy{
			{ID: "a", Name: "a", Enabled: false, NetworkMode: "deny", MaxTokens: intPtr(100)},
		})
		require.Empty(t, sources)
		require.Empty(t, effective.NetworkMode)
		require.Nil(t, effective.MaxTokens)
	})

	t.Run("allowlists intersect", func(t *testing.T) {
		effective, _ := MergePolicies([]Policy{
			{ID: "a", Name: "a", Enabled: true, AllowedModels: []string{"gpt-4", "claude-3"}},
			{ID: "b", Name: "b", Enabled: true, AllowedModels: []string{"claude-3", "llama"}},
		})
		require.Equal(t, []string{"claude-3"}, effective.AllowedModels)
	})

	t.Run("first list alone applies when others are unrestricted", func(t *testing.T) {
		effective, _ := MergePolicies([]Policy{
			{ID: "a", Name: "a", Enabled: true, AllowedMCP: []string{"graphmap"}},
			{ID: "b", Name: "b", Enabled: true},
		})
		require.Equal(t, []string{"graphmap"}, effective.AllowedMCP)
	})

	t.Run("star means unrestricted even when merged", func(t *testing.T) {
		effective, _ := MergePolicies([]Policy{
			{ID: "a", Name: "a", Enabled: true, AllowedModels: []string{"gpt-4"}},
			{ID: "b", Name: "b", Enabled: true, AllowedModels: []string{"*"}},
		})
		require.Equal(t, []string{"gpt-4"}, effective.AllowedModels)
		// "*" alone with no other list: the list stays ["*"] — PolicyAllowsList
		// treats it as unrestricted.
		effective, _ = MergePolicies([]Policy{{ID: "c", Name: "c", Enabled: true, AllowedModels: []string{"*"}}})
		require.True(t, PolicyAllowsList(effective.AllowedModels, "anything"))
	})

	t.Run("disjoint lists block everything", func(t *testing.T) {
		effective, _ := MergePolicies([]Policy{
			{ID: "a", Name: "a", Enabled: true, AllowedModels: []string{"gpt-4"}},
			{ID: "b", Name: "b", Enabled: true, AllowedModels: []string{"claude-3"}},
		})
		require.Empty(t, effective.AllowedModels)
		require.False(t, PolicyAllowsList(effective.AllowedModels, "gpt-4"))
	})

	t.Run("caps take the minimum", func(t *testing.T) {
		effective, _ := MergePolicies([]Policy{
			{ID: "a", Name: "a", Enabled: true, MaxTokens: intPtr(50000), MaxBudgetUSD: floatPtr(5), TimeoutSeconds: intPtr(1800)},
			{ID: "b", Name: "b", Enabled: true, MaxTokens: intPtr(20000), MaxBudgetUSD: floatPtr(10), TimeoutSeconds: intPtr(3600)},
		})
		require.EqualValues(t, 20000, *effective.MaxTokens)
		require.EqualValues(t, 5, *effective.MaxBudgetUSD)
		require.EqualValues(t, 1800, *effective.TimeoutSeconds)
	})

	t.Run("restrictive modes win and never loosen", func(t *testing.T) {
		effective, _ := MergePolicies([]Policy{
			{ID: "a", Name: "a", Enabled: true, NetworkMode: "deny", SandboxMode: "read_only", ApprovalMode: "tools"},
			{ID: "b", Name: "b", Enabled: true, NetworkMode: "allow", SandboxMode: "standard", ApprovalMode: "auto"},
		})
		require.Equal(t, "deny", effective.NetworkMode)
		require.Equal(t, "read_only", effective.SandboxMode)
		require.Equal(t, "tools", effective.ApprovalMode)
	})

	// Intake is deny-only overlay (docs/inbound-messages.md §12): there is no
	// "allow" — a unit policy can only switch intake off for its subtree, the
	// opt-in itself lives on the project. A child cannot cancel an ancestor's deny.
	t.Run("intake deny is sticky across the chain", func(t *testing.T) {
		effective, _ := MergePolicies([]Policy{
			{ID: "root", Name: "root", Enabled: true, IntakeMode: "deny"},
			{ID: "child", Name: "child", Enabled: true},
		})
		require.Equal(t, "deny", effective.IntakeMode)

		effective, _ = MergePolicies([]Policy{
			{ID: "child", Name: "child", Enabled: true},
			{ID: "root", Name: "root", Enabled: true, IntakeMode: "deny"},
		})
		require.Equal(t, "deny", effective.IntakeMode)
	})
}

func TestPolicyAllowsList(t *testing.T) {
	require.True(t, PolicyAllowsList(nil, "gpt-4"))           // never restricted
	require.False(t, PolicyAllowsList([]string{}, "gpt-4"))   // intersected to nothing
	require.True(t, PolicyAllowsList([]string{"*"}, "gpt-4")) // explicit star
	require.True(t, PolicyAllowsList([]string{"gpt-4", "claude"}, "claude"))
	require.False(t, PolicyAllowsList([]string{"gpt-4"}, "claude"))
}

func TestValidatePolicy(t *testing.T) {
	require.NoError(t, ValidatePolicy(Policy{Name: "base"}))
	require.Error(t, ValidatePolicy(Policy{Name: ""}))
	require.Error(t, ValidatePolicy(Policy{Name: "x", NetworkMode: "off"}))
	require.Error(t, ValidatePolicy(Policy{Name: "x", SandboxMode: "strict"}))
	require.Error(t, ValidatePolicy(Policy{Name: "x", ApprovalMode: "always"}))
	require.NoError(t, ValidatePolicy(Policy{Name: "x", IntakeMode: "deny"}))
	require.Error(t, ValidatePolicy(Policy{Name: "x", IntakeMode: "allow"}))
}

// The memory-quota columns are NOT NULL with 0 = «no restriction»
// (migration 0020_org_policy_memory_quota), while the model's «not set» is
// nil — the restrictive merge treats nil as unlimited. A human setting 0 to
// lift a limit must round-trip: 0 normalizes to nil, nil writes back as the
// stored 0. Passing nil straight to the column used to fail the save with
// «null value in column \"memory_budget_mb\" … violates not-null constraint».
func TestPolicyQuotaZeroRoundTrip(t *testing.T) {
	zero := 0
	p := Policy{Name: "base", MaxConcurrentRuns: &zero, MemoryBudgetMB: &zero}
	normalizePolicyLists(&p)
	require.Nil(t, p.MaxConcurrentRuns, "0 must read as «no restriction»")
	require.Nil(t, p.MemoryBudgetMB)
	require.Equal(t, 0, intOrZero(p.MaxConcurrentRuns), "unset must store as the 0 the NOT NULL column wants")
	require.Equal(t, 0, intOrZero(p.MemoryBudgetMB))

	// And the read boundary: stored zeros are unset again, real caps survive.
	require.Nil(t, zeroAsUnset(&zero))
	require.Equal(t, 5, *zeroAsUnset(intPtr(5)))

	// Unset quotas never restrict the merge (nil = unlimited, not 0).
	effective, _ := MergePolicies([]Policy{{Name: "a", Enabled: true, MemoryBudgetMB: intPtr(5000)}})
	require.NotNil(t, effective.MemoryBudgetMB)
	require.Equal(t, 5000, *effective.MemoryBudgetMB)
	effective, _ = MergePolicies([]Policy{{Name: "a", Enabled: true, MemoryBudgetMB: intPtr(5000)}, {Name: "b", Enabled: true}})
	require.Equal(t, 5000, *effective.MemoryBudgetMB, "an absent cap must not narrow the merge")
}
