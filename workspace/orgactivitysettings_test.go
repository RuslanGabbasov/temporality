package workspace

// Settings inheritance tests (docs/temporality-organizational-operations-
// spec.md §5): local / inherited / system / default resolution with source
// attribution — the pure resolver, no database.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func orgSettingsUnits() []OrgUnit {
	return []OrgUnit{
		{ID: "root", Kind: "organization", Name: "Root"},
		{ID: "dept", ParentID: "root", Kind: "department", Name: "Dept", Path: "root"},
		{ID: "team", ParentID: "dept", Kind: "team", Name: "Team", Path: "root.dept"},
	}
}

func TestResolveOrgActivitySettingsDefaults(t *testing.T) {
	values, sources := resolveOrgActivitySettings("team", []string{"root", "dept", "team"}, map[string]map[string]string{})
	require.Equal(t, "10", values[OrgSettingBatchIntervalMinutes])
	require.Equal(t, "default", sources[OrgSettingBatchIntervalMinutes].Origin)
	settings := OrgActivitySettingsFor(values)
	require.True(t, settings.ActivityEnabled)
	require.False(t, settings.AgentAwarenessEnabled) // awareness is opt-in
	require.Equal(t, 10*time.Minute, time.Duration(settings.BatchIntervalMinutes)*time.Minute)
	require.Equal(t, 90*24*time.Hour, settings.ActivityRetention)
	require.Equal(t, OrgAwarenessScopeUnit, settings.AwarenessScope)
}

func TestResolveOrgActivitySettingsLocalWins(t *testing.T) {
	rows := map[string]map[string]string{
		"root": {OrgSettingBatchIntervalMinutes: "60"},
		"dept": {OrgSettingBatchIntervalMinutes: "30"},
		"team": {OrgSettingBatchIntervalMinutes: "5"},
	}
	values, sources := resolveOrgActivitySettings("team", []string{"root", "dept", "team"}, rows)
	require.Equal(t, "5", values[OrgSettingBatchIntervalMinutes])
	require.Equal(t, "local", sources[OrgSettingBatchIntervalMinutes].Origin)
	require.Equal(t, "team", sources[OrgSettingBatchIntervalMinutes].UnitID)
}

func TestResolveOrgActivitySettingsNearestInherited(t *testing.T) {
	rows := map[string]map[string]string{
		"root": {OrgSettingBatchIntervalMinutes: "60"},
		"dept": {OrgSettingBatchIntervalMinutes: "30"},
		// team: no local row → nearest ancestor wins
	}
	values, sources := resolveOrgActivitySettings("team", []string{"root", "dept", "team"}, rows)
	require.Equal(t, "30", values[OrgSettingBatchIntervalMinutes])
	require.Equal(t, "inherited", sources[OrgSettingBatchIntervalMinutes].Origin)
	require.Equal(t, "dept", sources[OrgSettingBatchIntervalMinutes].UnitID)

	// dept itself inherits from root — its own row wins for its own view.
	valuesDept, sourcesDept := resolveOrgActivitySettings("dept", []string{"root", "dept"}, rows)
	require.Equal(t, "30", valuesDept[OrgSettingBatchIntervalMinutes])
	require.Equal(t, "local", sourcesDept[OrgSettingBatchIntervalMinutes].Origin)

	valuesRoot, sourcesRoot := resolveOrgActivitySettings("root", []string{"root"}, rows)
	require.Equal(t, "60", valuesRoot[OrgSettingBatchIntervalMinutes])
	require.Equal(t, "local", sourcesRoot[OrgSettingBatchIntervalMinutes].Origin)
}

func TestResolveOrgActivitySettingsSystemRow(t *testing.T) {
	rows := map[string]map[string]string{
		"": {OrgSettingAgentAwarenessEnabled: "true"},
	}
	values, sources := resolveOrgActivitySettings("team", []string{"root", "dept", "team"}, rows)
	require.Equal(t, "true", values[OrgSettingAgentAwarenessEnabled])
	require.Equal(t, "system", sources[OrgSettingAgentAwarenessEnabled].Origin)

	// A local row overrides the system row.
	rows["team"] = map[string]string{OrgSettingAgentAwarenessEnabled: "false"}
	values, sources = resolveOrgActivitySettings("team", []string{"root", "dept", "team"}, rows)
	require.Equal(t, "false", values[OrgSettingAgentAwarenessEnabled])
	require.Equal(t, "local", sources[OrgSettingAgentAwarenessEnabled].Origin)
}

func TestResolveOrgActivitySettingsUnknownKeyIgnored(t *testing.T) {
	rows := map[string]map[string]string{
		"team": {"not_a_setting": "42"},
	}
	values, sources := resolveOrgActivitySettings("team", []string{"team"}, rows)
	_, hasUnknown := values["not_a_setting"]
	require.False(t, hasUnknown)
	_, hasUnknownSource := sources["not_a_setting"]
	require.False(t, hasUnknownSource)
}

func TestResolveOrgActivitySettingsForUnitsCoversSystem(t *testing.T) {
	units := orgSettingsUnits()
	rows := map[string]map[string]string{
		"": {OrgSettingPublishRunFailures: "false"},
	}
	resolved := ResolveOrgActivitySettingsForUnits(units, rows)
	require.Len(t, resolved, 4) // root, dept, team + the '' system view
	require.False(t, resolved["team"].Settings.PublishRunFailures)
	require.False(t, resolved[""].Settings.PublishRunFailures)
	require.True(t, resolved["root"].Settings.PublishRunStarts)
}

func TestValidateOrgSettingText(t *testing.T) {
	require.NoError(t, validateOrgSettingText(OrgSettingActivityEnabled, "true"))
	require.Error(t, validateOrgSettingText(OrgSettingActivityEnabled, "yes"))
	require.NoError(t, validateOrgSettingText(OrgSettingBatchIntervalMinutes, "15"))
	require.Error(t, validateOrgSettingText(OrgSettingBatchIntervalMinutes, "0"))
	require.Error(t, validateOrgSettingText(OrgSettingBatchIntervalMinutes, "abc"))
	require.NoError(t, validateOrgSettingText(OrgSettingActivityRetention, "720h"))
	require.Error(t, validateOrgSettingText(OrgSettingActivityRetention, "3days"))
	require.NoError(t, validateOrgSettingText(OrgSettingAwarenessScope, OrgAwarenessScopeSubtree))
	require.Error(t, validateOrgSettingText(OrgSettingAwarenessScope, "everywhere"))
	require.Error(t, validateOrgSettingText("unknown", "true"))
}

func TestOrgActivitySettingKeysStable(t *testing.T) {
	keys := OrgActivitySettingKeys()
	require.NotEmpty(t, keys)
	for i := 1; i < len(keys); i++ {
		require.Less(t, keys[i-1], keys[i]) // sorted: stable API grammar
	}
}
