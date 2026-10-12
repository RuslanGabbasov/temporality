package workspace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestBuildQualityReportPerVersion pins the §7.1 dashboard math on a fixed
// dataset: run counts and M1 outcome classification per version, best/last
// suite evaluation, goldens, time-to-verified-improvement and the explicit
// has_data=false marker for a version without data (§7.4).
func TestBuildQualityReportPerVersion(t *testing.T) {
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	snapshots := []AgentVersion{
		{AgentID: "agent-a", Version: 1, CreatedAt: base},
		{AgentID: "agent-a", Version: 2, CreatedAt: base.Add(time.Hour)},
		{AgentID: "agent-a", Version: 3, CreatedAt: base.Add(2 * time.Hour)}, // no data yet
	}
	runs := []qualityRun{
		{runID: "r1", status: "completed", agentVersion: 1}, // accepted verdict → confirmed
		{runID: "r2", status: "failed", agentVersion: 1},    // rejected verdict → corrected
		{runID: "r3", status: "completed", agentVersion: 2}, // human approval → corrected
		{runID: "r4", status: "completed", agentVersion: 2}, // no verdicts → confirmed
		{runID: "r5", status: "completed", agentVersion: 0}, // unknown pin stays out (§3.1)
	}
	requests := []HumanRequest{
		{RunID: "r1", Status: HumanStatusAnswered, Response: "yes", Options: []string{"yes", "no"}, AgentVersion: 1},
		{RunID: "r2", Status: HumanStatusRejected, AgentVersion: 1},
		{RunID: "r5", Status: HumanStatusAnswered, Response: "ok", Options: []string{"ok"}, AgentVersion: 0},
	}
	terminal := []qualityTerminalEvent{
		{runID: "r3", event: terminalEvent{
			eventType: "tool.completed", actor: "human",
			data: map[string]any{"operation_id": "op-1"},
		}},
	}
	evaluations := []AgentEvaluationRun{
		{ID: 10, AgentVersion: 1, Passed: 3, Failed: 0, CreatedAt: base.Add(10 * time.Minute)}, // first green
		{ID: 11, AgentVersion: 1, Passed: 2, Failed: 1, CreatedAt: base.Add(40 * time.Minute)}, // last run
		{ID: 12, AgentVersion: 2, Passed: 1, Failed: 1, CreatedAt: base.Add(65 * time.Minute)}, // red
		{ID: 13, AgentVersion: 2, Passed: 2, Failed: 0, CreatedAt: base.Add(80 * time.Minute)}, // green
	}

	report := buildQualityReport("agent-a", snapshots, runs, requests, terminal, evaluations, 2)
	require.Equal(t, "agent-a", report.AgentID)
	require.Len(t, report.Versions, 3)

	v1 := report.Versions[0]
	require.Equal(t, 1, v1.Version)
	require.Equal(t, 2, v1.Runs) // r5 (version 0) is not counted
	require.Equal(t, 1, v1.Confirmed)
	require.Equal(t, 1, v1.Corrected)
	require.NotNil(t, v1.BestPassRate)
	require.Equal(t, 1.0, *v1.BestPassRate)
	require.NotNil(t, v1.LastEvaluation)
	require.Equal(t, int64(11), v1.LastEvaluation.ID)
	require.InDelta(t, 0.6667, v1.LastEvaluation.PassRate, 0.0001)
	require.Equal(t, 2, v1.Goldens)
	require.NotNil(t, v1.TTVIMinutes)
	require.Equal(t, int64(10), *v1.TTVIMinutes) // version created → first green run
	require.True(t, v1.HasData)

	v2 := report.Versions[1]
	require.Equal(t, 2, v2.Version)
	require.Equal(t, 2, v2.Runs)
	require.Equal(t, 1, v2.Confirmed)
	require.Equal(t, 1, v2.Corrected)
	require.NotNil(t, v2.BestPassRate)
	require.Equal(t, 1.0, *v2.BestPassRate)
	require.NotNil(t, v2.LastEvaluation)
	require.Equal(t, int64(13), v2.LastEvaluation.ID)
	require.NotNil(t, v2.TTVIMinutes)
	require.Equal(t, int64(20), *v2.TTVIMinutes)
	require.True(t, v2.HasData)

	v3 := report.Versions[2]
	require.Equal(t, 3, v3.Version)
	require.False(t, v3.HasData) // §7.4: explicit "no data", not a fake 0%
	require.Nil(t, v3.BestPassRate)
	require.Nil(t, v3.LastEvaluation)
	require.Nil(t, v3.TTVIMinutes)
	require.Equal(t, 0, v3.Runs)
}

// TestBuildQualityReportDataOnlyVersions: versions observed only in runs or
// evaluations (no snapshot row) still appear, sorted, with honest metrics.
func TestBuildQualityReportDataOnlyVersions(t *testing.T) {
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	report := buildQualityReport("agent-a",
		[]AgentVersion{{AgentID: "agent-a", Version: 2, CreatedAt: base}},
		[]qualityRun{{runID: "r1", status: "completed", agentVersion: 1}},
		nil, nil,
		[]AgentEvaluationRun{{ID: 7, AgentVersion: 3, Passed: 1, Failed: 0, CreatedAt: base.Add(time.Hour)}},
		0)
	require.Len(t, report.Versions, 3)
	require.Equal(t, 1, report.Versions[0].Version)
	require.True(t, report.Versions[0].HasData)
	require.Nil(t, report.Versions[0].TTVIMinutes) // no snapshot → no TTVI
	require.Equal(t, 2, report.Versions[1].Version)
	require.False(t, report.Versions[1].HasData)
	require.Equal(t, 3, report.Versions[2].Version)
	require.True(t, report.Versions[2].HasData)
	require.NotNil(t, report.Versions[2].LastEvaluation)
}

// TestDefinitionDiffStructuralChanges: §7.2 field set — purpose, constraints,
// completion and capability names; prompt bodies never enter the diff.
func TestDefinitionDiffStructuralChanges(t *testing.T) {
	yes, no := true, false
	from := AgentVersion{
		Version:     3,
		Description: "Old purpose",
		Definition: AgentDefinition{
			Constraints:    []string{"never touch prod", "keep answers short"},
			Completion:     []string{"tests pass"},
			Capabilities:   AgentCapabilities{ModifyFiles: &yes, Delegation: &no},
			PromptOverride: "manual prompt body v3",
		},
	}
	to := AgentVersion{
		Version:     4,
		Description: "New purpose",
		Definition: AgentDefinition{
			Constraints:    []string{"keep answers short", "redact credentials"},
			Completion:     []string{"tests pass", "lint clean"},
			Capabilities:   AgentCapabilities{ModifyFiles: &no, Delegation: &yes},
			PromptOverride: "different manual body v4",
		},
	}
	changes := DefinitionDiff(from, to)
	byKey := map[string]DefinitionChange{}
	for _, change := range changes {
		byKey[change.Field+":"+change.Kind+":"+change.Value] = change
	}
	purpose := byKey["purpose:changed:New purpose"]
	require.Equal(t, "Old purpose", purpose.From)
	require.Contains(t, byKey, "constraints:removed:never touch prod")
	require.Contains(t, byKey, "constraints:added:redact credentials")
	require.NotContains(t, byKey, "constraints:removed:keep answers short")
	require.Contains(t, byKey, "completion:added:lint clean")
	require.Contains(t, byKey, "capabilities:removed:modify_files")
	require.Contains(t, byKey, "capabilities:added:delegation")
	for _, change := range changes {
		require.NotEqual(t, "prompt_override", change.Field, "prompt bodies stay out of the structural diff (§7.2)")
	}
}

// TestVersionDiffOfLineDiffAndRedaction: the §8.5 payload carries the
// changed prompt lines and masks credential-looking text before it leaves
// storage (§10.3).
func TestVersionDiffOfLineDiffAndRedaction(t *testing.T) {
	from := AgentVersion{
		Version:        3,
		Description:    "same",
		CompiledPrompt: "line one\nline two\nline three",
	}
	to := AgentVersion{
		Version:        4,
		Description:    "same",
		CompiledPrompt: "line one\nline two changed\nline three",
	}
	diff := VersionDiffOf(from, to)
	require.Equal(t, 3, diff.From)
	require.Equal(t, 4, diff.To)
	require.Equal(t, []string{"- line two", "+ line two changed"}, diff.PromptDiff)
	require.Empty(t, diff.DefinitionChanges)
	require.False(t, diff.Redacted)

	// §10.3: credential-looking text is masked in both diff layers.
	from.Definition.Constraints = []string{"use token=abc123 only in vault"}
	to.Definition.Constraints = []string{"use token=def456 only in vault"}
	from.CompiledPrompt = "line one\nset password=hunter2"
	to.CompiledPrompt = "line one\nset password=hunter3"
	diff = VersionDiffOf(from, to)
	require.True(t, diff.Redacted)
	require.Len(t, diff.DefinitionChanges, 2)
	kinds := map[string]DefinitionChange{}
	for _, change := range diff.DefinitionChanges {
		require.Equal(t, "constraints", change.Field)
		require.True(t, change.Redacted)
		kinds[change.Kind] = change
	}
	require.Equal(t, "use token=[REDACTED] only in vault", kinds[DefinitionChangeRemoved].Value)
	require.Equal(t, "use token=[REDACTED] only in vault", kinds[DefinitionChangeAdded].Value)
	require.Equal(t, []string{"- set password=[REDACTED]", "+ set password=[REDACTED]"}, diff.PromptDiff)
}

// TestLineDiffChangedLinesOnly: pure line-diff behaviour — unchanged lines
// never appear, removals precede additions at a replacement site.
func TestLineDiffChangedLinesOnly(t *testing.T) {
	require.Empty(t, LineDiff("a\nb", "a\nb"))
	require.Equal(t, []string{"- b", "+ c"}, LineDiff("a\nb", "a\nc"))
	require.Equal(t, []string{"+ b"}, LineDiff("a", "a\nb"))
	require.Equal(t, []string{"- a", "- b", "+ x"}, LineDiff("a\nb", "x"))
	require.Equal(t, []string{"- b"}, LineDiff("a\nb", "a"))
}

// TestCountGoldenCases: the §7.1 goldens column counts suite cases harvested
// from production runs (M2 names them golden/<run-id prefix>).
func TestCountGoldenCases(t *testing.T) {
	require.Equal(t, 0, countGoldenCases(nil))
	require.Equal(t, 2, countGoldenCases([]EvaluationCase{
		{Name: "golden/run-abc123"},
		{Name: "golden/run-def456"},
		{Name: "manual case"},
	}))
}
