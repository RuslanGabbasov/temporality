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
}
