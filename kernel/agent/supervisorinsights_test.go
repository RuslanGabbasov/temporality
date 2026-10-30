package agent

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/observation"
)

func statsWindow(calls, failed, retries, runsFailed, pending int, p95 int64) *observation.HealthStats {
	models := []observation.HealthModelStats{}
	if calls > 0 {
		models = append(models, observation.HealthModelStats{Model: "glm", Calls: calls, Failed: failed, Retries: retries, P95LatencyMS: p95})
	}
	return &observation.HealthStats{
		Models: models,
		Totals: observation.HealthTotals{Nudged: 0},
		Runs:   observation.HealthRuns{Failed: runsFailed},
		Approvals: observation.HealthApprovals{
			Pending:  pending,
			Granted:  2,
			Rejected: 1,
		},
	}
}

func TestDetectAnomaliesRules(t *testing.T) {
	current := statsWindow(24, 8, 0, 4, 4, 40_000) // 8/24 = 33% failure rate
	baseline := statsWindow(24, 0, 0, 0, 0, 10_000)
	anomalies := DetectAnomalies(current, baseline)
	metrics := map[string]bool{}
	for _, a := range anomalies {
		metrics[a.Metric] = true
	}
	for _, want := range []string{"model_failures:glm", "run_failures", "approval_backlog", "latency_p95:glm"} {
		if !metrics[want] {
			t.Errorf("expected anomaly %q, got %v", want, metrics)
		}
	}
}

// A handful of calls cannot say anything about p95: the insight used to
// apologize for a 7-call baseline inside its own text — that is noise, not a
// finding. Latency needs a meaningful sample.
func TestDetectAnomaliesLatencyNeedsSample(t *testing.T) {
	current := statsWindow(10, 4, 0, 4, 4, 40_000)
	baseline := statsWindow(10, 0, 0, 0, 0, 10_000)
	for _, a := range DetectAnomalies(current, baseline) {
		if a.Metric == "latency_p95:glm" {
			t.Fatalf("latency anomaly on a 10-call sample must not fire")
		}
	}
}

// The latency finding carries its raw magnitude — the pass escalation math
// compares it against the last published one.
func TestDetectAnomaliesLatencyCarriesMagnitude(t *testing.T) {
	current := statsWindow(24, 4, 0, 4, 4, 40_000)
	baseline := statsWindow(24, 0, 0, 0, 0, 10_000)
	for _, a := range DetectAnomalies(current, baseline) {
		if a.Metric == "latency_p95:glm" {
			if a.Magnitude != 40_000 {
				t.Fatalf("magnitude = %d, want 40000 (the current p95)", a.Magnitude)
			}
			return
		}
	}
	t.Fatal("latency anomaly expected")
}

func TestDetectAnomaliesQuietWhenSteady(t *testing.T) {
	current := statsWindow(10, 1, 2, 1, 0, 8_000)
	baseline := statsWindow(10, 1, 2, 1, 0, 8_000)
	if anomalies := DetectAnomalies(current, baseline); len(anomalies) != 0 {
		t.Fatalf("steady windows must stay quiet, got %v", anomalies)
	}
}

func TestDetectAnomaliesNilBaselineSafe(t *testing.T) {
	current := statsWindow(10, 5, 0, 0, 0, 0)
	anomalies := DetectAnomalies(current, nil)
	if len(anomalies) == 0 {
		t.Fatal("high failure rate with no baseline must still fire")
	}
}

func TestDetectAnomaliesRetryStormNeedsMultiplier(t *testing.T) {
	// 12 retries vs 4 baseline: 3x — fires.
	fired := DetectAnomalies(statsWindow(20, 0, 12, 0, 0, 0), statsWindow(20, 0, 4, 0, 0, 0))
	storm := false
	for _, a := range fired {
		if a.Metric == "retries" {
			storm = true
		}
	}
	if !storm {
		t.Fatal("3x retry growth must fire")
	}
	// 12 vs 8: below 3x — quiet.
	quiet := DetectAnomalies(statsWindow(20, 0, 12, 0, 0, 0), statsWindow(20, 0, 8, 0, 0, 0))
	for _, a := range quiet {
		if a.Metric == "retries" {
			t.Fatal("sub-threshold retry growth must stay quiet")
		}
	}
}

func TestAnomalyTemplateCarriesNumbers(t *testing.T) {
	current := statsWindow(10, 4, 0, 0, 0, 0)
	anomalies := DetectAnomalies(current, statsWindow(10, 0, 0, 0, 0, 0))
	if len(anomalies) == 0 {
		t.Fatal("expected the failure-rate anomaly")
	}
	a := anomalies[0]
	if !strings.Contains(a.Template, "4") || !strings.Contains(a.Template, "10") {
		t.Errorf("template must quote the counters, got %q", a.Template)
	}
	if a.Current == "" || a.Baseline == "" {
		t.Error("current/baseline must be pre-rendered for the UI")
	}
}

func TestInsightPromptCarriesFactsAndDraft(t *testing.T) {
	a := Anomaly{
		Metric: "retries", Title: "Шторм ретраев",
		Current: "12 ретраев", Baseline: "4 ретраев",
		Facts:    []string{"current window: 12 retries"},
		Template: "Число ретраев выросло.",
	}
	prompt := supervisorInsightPrompt(a)
	for _, want := range []string{"retries", "12 ретраев", "current window: 12 retries", "Число ретраев выросло."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestFmtDurationMS(t *testing.T) {
	cases := map[int64]string{
		500:    "500 мс",
		4_200:  "4.2 с",
		90_000: "1.5 мин",
	}
	for in, want := range cases {
		if got := fmtDurationMS(in); got != want {
			t.Errorf("fmtDurationMS(%d) = %q, want %q", in, got, want)
		}
	}
}
