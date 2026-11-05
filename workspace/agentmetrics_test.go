package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// metricsArm builds a single-day rollup arm for the pure comparison tests.
func metricsArm(runs, succeeded, accepted, corrected, rejected, interventions, incidents int) []AgentVersionMetrics {
	return []AgentVersionMetrics{{
		Runs: runs, Succeeded: succeeded,
		VerdictAccepted: accepted, VerdictCorrected: corrected, VerdictRejected: rejected,
		Interventions: interventions, Incidents: incidents,
	}}
}

// TestEvaluateObservation covers the §3.1 honesty rules: the window closes on
// age AND volume, an over-aged window without runs is «недостаточно данных»,
// either arm under the run minimum is never «улучшение», and improved/degraded
// needs at least two primary SLIs signaling one way with none signaling the
// other. With 20 runs per arm the signal threshold is 1/sqrt(20) ≈ 0.224.
func TestEvaluateObservation(t *testing.T) {
	cfg := ObservationConfig{Days: 7, MinRuns: 10}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	at := func(days int) time.Time { return now.AddDate(0, 0, -days) }

	// improved: success +0.30, corrections -0.33, interventions -0.35 signal
	// up; incidents -0.20 stays inside the noise band.
	better := metricsArm(20, 18, 10, 1, 1, 1, 0)
	worse := metricsArm(20, 12, 6, 5, 1, 8, 4)
	// stable (conflicting): success +0.30 signals up, corrections +0.33
	// signals down — no verdict may be pronounced.
	mixedCur := metricsArm(20, 18, 6, 5, 1, 2, 0)
	mixedBase := metricsArm(20, 12, 10, 1, 1, 2, 0)
	// stable (within noise): every rate difference is under 0.224.
	quietCur := metricsArm(20, 17, 10, 1, 1, 2, 0)
	quietBase := metricsArm(20, 18, 10, 1, 1, 2, 0)

	cases := []struct {
		name     string
		created  time.Time
		current  []AgentVersionMetrics
		baseline []AgentVersionMetrics
		state    string
		verdict  string
		elapsed  int
		runs     int
	}{
		{"observing while young", at(3), better, worse, "observing", "", 3, 20},
		{"observing while waiting for runs", at(8), metricsArm(5, 5, 2, 0, 0, 0, 0), worse, "observing", "", 8, 5},
		{"insufficient age: over twice the window without the run minimum", at(15), metricsArm(5, 5, 2, 0, 0, 0, 0), worse, "closed", "insufficient", 15, 5},
		{"insufficient sample: baseline arm under the run minimum", at(8), better, metricsArm(5, 4, 2, 0, 0, 1, 0), "closed", "insufficient", 8, 20},
		{"improved", at(8), better, worse, "closed", "improved", 8, 20},
		{"degraded", at(8), worse, better, "closed", "degraded", 8, 20},
		{"stable on conflicting signals", at(8), mixedCur, mixedBase, "closed", "stable", 8, 20},
		{"stable within noise", at(8), quietCur, quietBase, "closed", "stable", 8, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateObservation(cfg, tc.created, tc.current, tc.baseline, now)
			require.Equal(t, tc.state, got.State)
			require.Equal(t, tc.verdict, got.Verdict)
			require.Equal(t, tc.elapsed, got.ElapsedDays)
			require.Equal(t, tc.runs, got.Runs)
		})
	}
}

// TestSumMetrics folds daily rows into one window summary.
func TestSumMetrics(t *testing.T) {
	sum := SumMetrics([]AgentVersionMetrics{
		{Runs: 3, Succeeded: 2, Failed: 1, VerdictAccepted: 1, Tokens: 100, CostUSD: 0.5, WallMs: 60, UncertainOps: 1},
		{Runs: 2, Succeeded: 2, Cancelled: 0, VerdictCorrected: 1, VerdictRejected: 1, Interventions: 2, Reworks: 1, Tokens: 50, CostUSD: 0.25, WallMs: 40, Incidents: 1},
	})
	require.Equal(t, WindowSummary{
		Runs: 5, Succeeded: 4, Failed: 1,
		VerdictAccepted: 1, VerdictCorrected: 1, VerdictRejected: 1,
		Interventions: 2, Reworks: 1, Incidents: 1,
		Tokens: 150, CostUSD: 0.75, WallMs: 100, UncertainOps: 1,
	}, sum)
}

// TestMetricDeltasSignalVsNoise pins the §3.1 noise threshold
// max(0.05, 1/sqrt(min(n_current, n_baseline))): a difference is a signal only
// with enough observations on both arms and a |Δ| past the threshold; the
// per-run averages are informational and never signal.
func TestMetricDeltasSignalVsNoise(t *testing.T) {
	byName := func(deltas []MetricDelta, name string) MetricDelta {
		t.Helper()
		for _, d := range deltas {
			if d.Metric == name {
				return d
			}
		}
		t.Fatalf("metric %s missing from %v", name, deltas)
		return MetricDelta{}
	}

	// 40 runs per arm → threshold 1/sqrt(40) ≈ 0.158; Δ success = 0.30.
	signals := MetricDeltas(
		WindowSummary{Runs: 40, Succeeded: 36},
		WindowSummary{Runs: 40, Succeeded: 24}, 10)
	success := byName(signals, "success_rate")
	require.InDelta(t, 0.30, success.Delta, 0.0001)
	require.True(t, success.Signal)

	// Δ success = 0.025 — inside the noise band.
	noise := MetricDeltas(
		WindowSummary{Runs: 40, Succeeded: 33},
		WindowSummary{Runs: 40, Succeeded: 32}, 10)
	require.False(t, byName(noise, "success_rate").Signal)

	// 5 runs on one arm — under minRuns, never a signal however large the Δ.
	undersampled := MetricDeltas(
		WindowSummary{Runs: 5, Succeeded: 5},
		WindowSummary{Runs: 40, Succeeded: 10}, 10)
	require.False(t, byName(undersampled, "success_rate").Signal)

	// The per-run averages are informational: big Δ, still no signal.
	averages := MetricDeltas(
		WindowSummary{Runs: 40, Tokens: 400000, CostUSD: 40, WallMs: 400000},
		WindowSummary{Runs: 40, Tokens: 200000, CostUSD: 20, WallMs: 200000}, 10)
	require.False(t, byName(averages, "tokens_avg").Signal)
	require.False(t, byName(averages, "cost_avg").Signal)
	require.False(t, byName(averages, "wall_avg").Signal)
	require.InDelta(t, 5000.0, byName(averages, "tokens_avg").Delta, 0.0001)
}

// TestScanAgentVersionMetricsRoundTrip round-trips one rollup row through the
// shared projection and scan (position-sensitive).
func TestScanAgentVersionMetricsRoundTrip(t *testing.T) {
	day := time.Date(2026, 11, 1, 15, 45, 0, 0, time.UTC)
	values := sliceRow{"agent-a", 3, day, 12, 10, 1, 1, 0, 8, 3, 1, 2, 1,
		int64(41000), 0.21, int64(183000), 4, 0}
	m, err := scanAgentVersionMetrics(values)
	require.NoError(t, err)
	require.Equal(t, "agent-a", m.AgentID)
	require.Equal(t, 3, m.Version)
	require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), m.Day) // truncated to the UTC day
	require.Equal(t, 12, m.Runs)
	require.Equal(t, 10, m.Succeeded)
	require.Equal(t, 3, m.VerdictCorrected)
	require.Equal(t, int64(41000), m.Tokens)
	require.InDelta(t, 0.21, m.CostUSD, 0.0001)
	require.Equal(t, int64(183000), m.WallMs)
	require.Equal(t, 4, m.UncertainOps)
}

// TestUpsertAgentVersionMetricsSQLShape pins the upsert wiring at the source
// level through a fake execer: the PK conflict rewrites every counter and the
// day argument is the UTC calendar day.
func TestUpsertAgentVersionMetricsSQLShape(t *testing.T) {
	fake := &fakeExecer{}
	m := AgentVersionMetrics{AgentID: "agent-a", Version: 2, Day: time.Date(2026, 11, 1, 15, 45, 0, 0, time.UTC),
		Runs: 3, Succeeded: 2, Tokens: 100}
	require.NoError(t, upsertAgentVersionMetricsRow(context.Background(), fake, m))

	require.Contains(t, fake.sql, "ON CONFLICT (agent_id, version, day) DO UPDATE")
	for _, counter := range []string{
		"runs = EXCLUDED.runs", "succeeded = EXCLUDED.succeeded", "failed = EXCLUDED.failed",
		"cancelled = EXCLUDED.cancelled", "turn_limited = EXCLUDED.turn_limited",
		"verdict_accepted = EXCLUDED.verdict_accepted", "verdict_corrected = EXCLUDED.verdict_corrected",
		"verdict_rejected = EXCLUDED.verdict_rejected", "interventions = EXCLUDED.interventions",
		"reworks = EXCLUDED.reworks", "tokens = EXCLUDED.tokens", "cost_usd = EXCLUDED.cost_usd",
		"wall_ms = EXCLUDED.wall_ms", "uncertain_ops = EXCLUDED.uncertain_ops", "incidents = EXCLUDED.incidents",
	} {
		require.Contains(t, fake.sql, counter)
	}
	require.Len(t, fake.args, 18)
	require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), fake.args[2])
	require.Equal(t, 3, fake.args[3])
}

// TestSetAgentObservationValidatesRange: the window bounds are refused as a
// conflict before any SQL runs — the store is never touched.
func TestSetAgentObservationValidatesRange(t *testing.T) {
	s := &Store{}
	err := s.SetAgentObservation(context.Background(), "agent-a", ObservationConfig{Days: 0, MinRuns: 10})
	require.True(t, errors.Is(err, ErrConflict))
	err = s.SetAgentObservation(context.Background(), "agent-a", ObservationConfig{Days: 7, MinRuns: 0})
	require.True(t, errors.Is(err, ErrConflict))
	err = s.SetAgentObservation(context.Background(), "agent-a", ObservationConfig{Days: 91, MinRuns: 10})
	require.True(t, errors.Is(err, ErrConflict))
	err = s.SetAgentObservation(context.Background(), "agent-a", ObservationConfig{Days: 7, MinRuns: 101})
	require.True(t, errors.Is(err, ErrConflict))
}
