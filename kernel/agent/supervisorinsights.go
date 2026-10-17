package agent

// Supervisor insights (docs/supervisor-model.md §3, wave A3): deterministic
// rules detect anomalies in the health windows (failure-rate growth, retry
// storms, run-failure spikes, approval backlog, latency regression) and the
// supervisor model explains the finding in plain language for the problem
// queue. Detection never depends on the model — a judge-off installation
// still gets template-worded insights; the model only improves the wording
// and adds a recommendation.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// Insight severity buckets drive the problem-queue styling.
const (
	InsightSeverityWarning = "warning"
	InsightSeverityInfo    = "info"
)

// Anomaly is one detected deviation between the current and previous health
// windows. Current/Baseline carry the pre-rendered numbers the explanation
// quotes, so the wording can never drift from the underlying counters.
type Anomaly struct {
	// Metric is the stable detection key ("model_failures", "retries",
	// "run_failures", "approval_backlog", "latency_p95"); the insight dedup
	// is keyed on it.
	Metric   string
	Severity string
	Title    string
	Current  string
	Baseline string
	// Facts are the raw counter lines the model must ground its wording in.
	Facts []string
	// Template is the deterministic fallback explanation used when the model
	// is unavailable or fails.
	Template string
	// Recommend is the default next step; the model may replace it with a
	// sharper one grounded in the same facts.
	Recommend string
}

// DetectAnomalies compares the current health window against the previous
// one and returns the findings, most severe first. Pure: same inputs yield
// the same anomalies, so tests pin every rule.
func DetectAnomalies(current, baseline *observation.HealthStats) []Anomaly {
	if current == nil {
		return nil
	}
	var found []Anomaly

	// Model failures: rate jump with a meaningful call volume.
	for _, model := range current.Models {
		base := baselineModel(baseline, model.Model)
		rate := func(s observation.HealthModelStats) float64 {
			if s.Calls == 0 {
				return 0
			}
			return float64(s.Failed) / float64(s.Calls)
		}
		fires := (model.Calls >= 5 && rate(model) >= 0.2) ||
			(model.Calls >= 5 && base.Calls >= 5 && rate(base) > 0 && rate(model) >= 2*rate(base))
		if !fires {
			continue
		}
		found = append(found, Anomaly{
			Metric:   "model_failures:" + model.Model,
			Severity: InsightSeverityWarning,
			Title:    fmt.Sprintf("Модель %s стала чаще падать", model.Model),
			Current:  fmt.Sprintf("%d из %d вызовов", model.Failed, model.Calls),
			Baseline: fmt.Sprintf("%d из %d вызовов", base.Failed, base.Calls),
			Facts: []string{
				fmt.Sprintf("current window: model %s: %d calls, %d failed", model.Model, model.Calls, model.Failed),
				fmt.Sprintf("previous window: model %s: %d calls, %d failed", model.Model, base.Calls, base.Failed),
			},
			Template:  fmt.Sprintf("У модели %s выросла доля ошибок: %d из %d вызовов против %d из %d в предыдущий час.", model.Model, model.Failed, model.Calls, base.Failed, base.Calls),
			Recommend: "Проверьте статус провайдера и последние ошибки модели в журнале.",
		})
	}

	// Retry storm: attempts multiplied against the previous window.
	retries := current.Totals.Nudged
	for _, model := range current.Models {
		retries += model.Retries
	}
	baseRetries := 0
	if baseline != nil {
		for _, model := range baseline.Models {
			baseRetries += model.Retries
		}
		baseRetries += baseline.Totals.Nudged
	}
	if retries >= 10 && retries >= 3*max(baseRetries, 1) {
		found = append(found, Anomaly{
			Metric:   "retries",
			Severity: InsightSeverityWarning,
			Title:    "Шторм ретраев вызовов модели",
			Current:  fmt.Sprintf("%d ретраев", retries),
			Baseline: fmt.Sprintf("%d ретраев", baseRetries),
			Facts: []string{
				fmt.Sprintf("current window: %d retries", retries),
				fmt.Sprintf("previous window: %d retries", baseRetries),
			},
			Template:  fmt.Sprintf("Число ретраев выросло до %d против %d в предыдущий час — провайдер или сеть, скорее всего, деградируют.", retries, baseRetries),
			Recommend: "Проверьте доступность провайдеров; при длительном шторме снизьте параллелизм запусков.",
		})
	}

	// Run failure spike: more runs dying than usual.
	baseFailed := 0
	if baseline != nil {
		baseFailed = baseline.Runs.Failed
	}
	if current.Runs.Failed >= 3 && current.Runs.Failed >= 2*max(baseFailed, 1) {
		found = append(found, Anomaly{
			Metric:   "run_failures",
			Severity: InsightSeverityWarning,
			Title:    "Всплеск упавших запусков",
			Current:  fmt.Sprintf("%d ранов упало", current.Runs.Failed),
			Baseline: fmt.Sprintf("%d ранов упало", baseFailed),
			Facts: []string{
				fmt.Sprintf("current window: %d runs failed, %d completed", current.Runs.Failed, current.Runs.Completed),
				fmt.Sprintf("previous window: %d runs failed, %d completed", baseFailed, baselineRunsCompleted(baseline)),
			},
			Template:  fmt.Sprintf("За последний час упало %d ранов против %d в предыдущий — если триаж указывает на один и тот же класс причины, это одна проблема, а не много.", current.Runs.Failed, baseFailed),
			Recommend: "Просмотрите кластеры триажа ниже: однотипные падения сворачиваются в один тикет.",
		})
	}

	// Approval backlog: work blocked on humans right now.
	if current.Approvals.Pending >= 3 {
		found = append(found, Anomaly{
			Metric:   "approval_backlog",
			Severity: InsightSeverityInfo,
			Title:    "Подтверждения копятся без ответа",
			Current:  fmt.Sprintf("%d ожидают", current.Approvals.Pending),
			Facts: []string{
				fmt.Sprintf("current window: %d approvals pending, %d resolved, avg wait %dms", current.Approvals.Pending, current.Approvals.Granted+current.Approvals.Rejected, current.Approvals.WaitMSAvg),
			},
			Template:  fmt.Sprintf("Сейчас %d подтверждений ждут решения человека — раны стоят в очереди (HWT растёт).", current.Approvals.Pending),
			Recommend: "Разберите очередь подтверждений в Запусках, иначе раны простаивают.",
		})
	}

	// Latency regression: p95 multiplied with an absolute floor.
	for _, model := range current.Models {
		base := baselineModel(baseline, model.Model)
		if base.P95LatencyMS <= 0 || model.P95LatencyMS < 30_000 {
			continue
		}
		if model.P95LatencyMS < 3*base.P95LatencyMS {
			continue
		}
		found = append(found, Anomaly{
			Metric:   "latency_p95:" + model.Model,
			Severity: InsightSeverityInfo,
			Title:    fmt.Sprintf("p95 задержки модели %s вырос кратно", model.Model),
			Current:  fmt.Sprintf("p95 %s", fmtDurationMS(model.P95LatencyMS)),
			Baseline: fmt.Sprintf("p95 %s", fmtDurationMS(base.P95LatencyMS)),
			Facts: []string{
				fmt.Sprintf("current window: model %s: p95 %dms, p50 %dms, %d calls", model.Model, model.P95LatencyMS, model.P50LatencyMS, model.Calls),
				fmt.Sprintf("previous window: model %s: p95 %dms, p50 %dms, %d calls", model.Model, base.P95LatencyMS, base.P50LatencyMS, base.Calls),
			},
			Template:  fmt.Sprintf("p95 задержки модели %s вырос с %s до %s — провайдер отвечает медленнее обычного.", model.Model, fmtDurationMS(base.P95LatencyMS), fmtDurationMS(model.P95LatencyMS)),
			Recommend: "Проверьте провайдера; при устойчивой деградации переключите агентов на резервную модель.",
		})
	}

	return found
}

func baselineModel(baseline *observation.HealthStats, name string) observation.HealthModelStats {
	if baseline == nil {
		return observation.HealthModelStats{Model: name}
	}
	for _, model := range baseline.Models {
		if model.Model == name {
			return model
		}
	}
	return observation.HealthModelStats{Model: name}
}

func baselineRunsCompleted(baseline *observation.HealthStats) int {
	if baseline == nil {
		return 0
	}
	return baseline.Runs.Completed
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// fmtDurationMS renders a millisecond count as a human duration ("4.2 мин").
func fmtDurationMS(ms int64) string {
	switch {
	case ms >= 60_000:
		return fmt.Sprintf("%.1f мин", float64(ms)/60_000)
	case ms >= 1_000:
		return fmt.Sprintf("%.1f с", float64(ms)/1_000)
	default:
		return fmt.Sprintf("%d мс", ms)
	}
}

// supervisorInsightPrompt renders the explanation request: the detected facts
// plus the template the model must improve on, never contradict.
func supervisorInsightPrompt(a Anomaly) string {
	var b strings.Builder
	b.WriteString(`You explain a platform-health anomaly to the installation administrator in plain Russian. Ground every claim in the facts; do not invent numbers. 1-3 sentences, no greetings.
Respond with one JSON object:
{"explanation": "<what is happening and why it matters>", "recommendation": "<one concrete next step>"}

`)
	fmt.Fprintf(&b, "anomaly: %s (%s)\n", a.Title, a.Metric)
	fmt.Fprintf(&b, "detected: now %s; previous hour %s\n", a.Current, a.Baseline)
	for _, fact := range a.Facts {
		fmt.Fprintf(&b, "fact: %s\n", fact)
	}
	fmt.Fprintf(&b, "draft wording: %s\n", a.Template)
	return b.String()
}

type supervisorInsightReply struct {
	Explanation    string `json:"explanation"`
	Recommendation string `json:"recommendation"`
}

// ExplainAnomaly asks the supervisor model to word the anomaly; the template
// wording is the fallback when the model is off or fails.
func ExplainAnomaly(ctx context.Context, model *llm.Client, a Anomaly) (explanation, recommendation string) {
	explanation, recommendation = a.Template, a.Recommend
	if model == nil {
		return
	}
	reply, err := completeParsed(ctx, model, []llm.Message{
		{Role: "system", Content: "You explain platform-health anomalies for a dashboard. You answer with JSON only."},
		{Role: "user", Content: supervisorInsightPrompt(a)},
	}, func(content string) (supervisorInsightReply, error) {
		payload, ok := extractJSONObject(content)
		if !ok {
			return supervisorInsightReply{}, fmt.Errorf("model returned no JSON object")
		}
		var reply supervisorInsightReply
		if err := json.Unmarshal([]byte(payload), &reply); err != nil {
			return supervisorInsightReply{}, fmt.Errorf("model returned invalid JSON: %w", err)
		}
		if strings.TrimSpace(reply.Explanation) == "" {
			return supervisorInsightReply{}, fmt.Errorf("model returned an empty explanation")
		}
		return reply, nil
	})
	if err != nil {
		return // keep the deterministic template
	}
	if trimmed := strings.TrimSpace(reply.Explanation); trimmed != "" {
		explanation = trimmed
	}
	if trimmed := strings.TrimSpace(reply.Recommendation); trimmed != "" {
		recommendation = trimmed
	}
	return
}

// PublishSupervisorInsight journals one anomaly explanation as a
// supervisor.insight event; the problem queue lists them beside verdicts.
// Best-effort by the caller.
func (a *Activities) PublishSupervisorInsight(ctx context.Context, metric, severity, title, current, baseline, explanation, recommendation string) error {
	if a.Events == nil {
		return nil
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("supervisor-insight/%s/%d", metric, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: a.SourceID, Integration: "temporality-agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: "supervisor", Type: "system"}},
		Type:       "supervisor.insight",
		Data: map[string]any{
			"metric":         metric,
			"severity":       severity,
			"title":          title,
			"current":        current,
			"baseline":       baseline,
			"explanation":    explanation,
			"recommendation": recommendation,
		},
	}
	if err := event.Validate(); err != nil {
		return err
	}
	return a.Events.Enqueue(ctx, event)
}
