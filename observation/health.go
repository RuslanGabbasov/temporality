package observation

import (
	"sort"
	"strconv"
	"time"
)

// HealthEventTypes lists the journal event types ComputeHealthStats consumes
// (docs/system-dashboard.md §3.1). Stores use it to load exactly the
// aggregation window in one pass instead of paging the 500-row listing.
var HealthEventTypes = []string{
	"model.started", "model.completed", "model.failed", "model.attempt", "model.nudged",
	"run.started", "run.completed", "run.failed", "run.cancelled",
	"approval.requested", "approval.granted", "approval.rejected", "approval.timed_out", "approval.auto_granted",
}

// HealthModelStats aggregates one model's calls over the health window. Model
// is the data["model"] value ("unknown" when the event carries none).
type HealthModelStats struct {
	Model        string `json:"model"`
	Provider     string `json:"provider,omitempty"`
	Calls        int    `json:"calls"`
	Completed    int    `json:"completed"`
	Failed       int    `json:"failed"`
	InFlight     int    `json:"in_flight"`
	Retries      int    `json:"retries"`
	Nudged       int    `json:"nudged"`
	P50LatencyMS int64  `json:"p50_latency_ms"`
	P95LatencyMS int64  `json:"p95_latency_ms"`
	TotalTokens  int64  `json:"total_tokens"`
	CachedTokens int64  `json:"cached_tokens"`
}

// HealthTotals is the window-wide rollup of the per-model counters.
type HealthTotals struct {
	ModelCalls    int `json:"model_calls"`
	ModelFailures int `json:"model_failures"`
	InFlight      int `json:"in_flight"`
	Nudged        int `json:"nudged"`
}

// HealthRuns counts run lifecycle transitions; RunningNow is the set of runs
// started in the window without a terminal event.
type HealthRuns struct {
	Started    int `json:"started"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
	Cancelled  int `json:"cancelled"`
	RunningNow int `json:"running_now"`
}

// HealthApprovals mirrors the kernel approval accounting: requests matched to
// resolutions by run + operation_id + arguments_hash.
type HealthApprovals struct {
	Requested   int   `json:"requested"`
	Granted     int   `json:"granted"`
	Rejected    int   `json:"rejected"`
	TimedOut    int   `json:"timed_out"`
	AutoGranted int   `json:"auto_granted"`
	Pending     int   `json:"pending"`
	WaitMSAvg   int64 `json:"wait_ms_avg"`
}

// HealthStats is the system-dashboard health summary of one window
// (docs/system-dashboard.md §3.1).
type HealthStats struct {
	Since       time.Time          `json:"since"`
	GeneratedAt time.Time          `json:"generated_at"`
	Models      []HealthModelStats `json:"models"`
	Totals      HealthTotals       `json:"totals"`
	Runs        HealthRuns         `json:"runs"`
	Approvals   HealthApprovals    `json:"approvals"`
}

// healthModelAccumulator gathers one model's window state before percentile
// finalization.
type healthModelAccumulator struct {
	stats     HealthModelStats
	latencies []int64
}

// ComputeHealthStats aggregates a window of journal events into the health
// summary. It is pure: stores load the window, this function derives every
// counter, so the memory and postgres paths share one semantics. Events may
// arrive in any order — a copy is sorted by (OccurredAt, EventID) so "latest"
// (provider attribution, terminal races) is well defined. Events older than
// since are ignored, matching the stores' window queries.
func ComputeHealthStats(events []Event, since, now time.Time) HealthStats {
	stats := HealthStats{Since: since, GeneratedAt: now, Models: []HealthModelStats{}}
	ordered := make([]Event, len(events))
	copy(ordered, events)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].OccurredAt.Equal(ordered[j].OccurredAt) {
			return ordered[i].OccurredAt.Before(ordered[j].OccurredAt)
		}
		return ordered[i].EventID < ordered[j].EventID
	})

	models := map[string]*healthModelAccumulator{}
	// In-flight calls are matched by (run, turn) across events of any model:
	// terminal events carry the same model as their started event normally, but
	// attribution must not depend on it, so the started event owns the slot.
	inFlight := map[string]string{}
	runningRuns := map[string]bool{}
	pendingApprovals := map[string]time.Time{}
	var totalWaitMS int64
	resolvedApprovals := 0

	accumulator := func(event Event) *healthModelAccumulator {
		model, _ := event.Data["model"].(string)
		if model == "" {
			// Legacy events (pre model-attribution fix) carried only the
			// provider on completed events; keying those by provider keeps
			// their tokens out of "unknown" and matches kernel/cost pricing
			// attribution.
			model, _ = event.Data["provider"].(string)
		}
		if model == "" {
			model = "unknown"
		}
		agg := models[model]
		if agg == nil {
			agg = &healthModelAccumulator{stats: HealthModelStats{Model: model}}
			models[model] = agg
		}
		return agg
	}

	for _, event := range ordered {
		if event.OccurredAt.Before(since) {
			continue
		}
		switch event.Type {
		case "model.started":
			agg := accumulator(event)
			agg.stats.Calls++
			inFlight[healthCallKey(event)] = agg.stats.Model
		case "model.completed":
			agg := accumulator(event)
			agg.stats.Completed++
			delete(inFlight, healthCallKey(event))
			if provider, _ := event.Data["provider"].(string); provider != "" {
				// Latest non-empty provider on completed events wins: fields
				// were added gradually, so early events may carry none.
				agg.stats.Provider = provider
			}
			if latency, ok := healthInt64(event.Data["latency_ms"]); ok && latency >= 0 {
				agg.latencies = append(agg.latencies, latency)
			}
			if tokens, ok := healthInt64(event.Data["total_tokens"]); ok {
				agg.stats.TotalTokens += tokens
			}
			if tokens, ok := healthInt64(event.Data["cached_tokens"]); ok {
				agg.stats.CachedTokens += tokens
			}
		case "model.failed":
			accumulator(event).stats.Failed++
			delete(inFlight, healthCallKey(event))
		case "model.attempt":
			accumulator(event).stats.Retries++
		case "model.nudged":
			accumulator(event).stats.Nudged++
		case "run.started":
			stats.Runs.Started++
			runningRuns[event.Context.Run] = true
		case "run.completed":
			stats.Runs.Completed++
			delete(runningRuns, event.Context.Run)
		case "run.failed":
			stats.Runs.Failed++
			delete(runningRuns, event.Context.Run)
		case "run.cancelled":
			stats.Runs.Cancelled++
			delete(runningRuns, event.Context.Run)
		case "approval.requested":
			stats.Approvals.Requested++
			// A re-request of the same operation restarts the wait clock
			// (kernel/cost approvalAccumulator semantics).
			pendingApprovals[healthApprovalKey(event)] = event.OccurredAt
		case "approval.granted", "approval.rejected", "approval.timed_out":
			switch event.Type {
			case "approval.granted":
				stats.Approvals.Granted++
			case "approval.rejected":
				stats.Approvals.Rejected++
			default:
				stats.Approvals.TimedOut++
			}
			key := healthApprovalKey(event)
			if requestedAt, ok := pendingApprovals[key]; ok {
				waitMS := event.OccurredAt.Sub(requestedAt).Milliseconds()
				if waitMS < 0 {
					waitMS = 0
				}
				totalWaitMS += waitMS
				// Only resolutions that matched a pending request contribute:
				// one without a request in the window has no wait to average in.
				resolvedApprovals++
				delete(pendingApprovals, key)
			}
		case "approval.auto_granted":
			stats.Approvals.AutoGranted++
		}
	}

	for _, model := range inFlight {
		if agg := models[model]; agg != nil {
			agg.stats.InFlight++
		}
	}
	for _, agg := range models {
		sort.Slice(agg.latencies, func(i, j int) bool { return agg.latencies[i] < agg.latencies[j] })
		agg.stats.P50LatencyMS = healthPercentile(agg.latencies, 50)
		agg.stats.P95LatencyMS = healthPercentile(agg.latencies, 95)
		stats.Totals.ModelCalls += agg.stats.Calls
		stats.Totals.ModelFailures += agg.stats.Failed
		stats.Totals.Nudged += agg.stats.Nudged
		stats.Models = append(stats.Models, agg.stats)
	}
	stats.Totals.InFlight = len(inFlight)
	stats.Runs.RunningNow = len(runningRuns)
	stats.Approvals.Pending = len(pendingApprovals)
	if resolvedApprovals > 0 {
		stats.Approvals.WaitMSAvg = totalWaitMS / int64(resolvedApprovals)
	}
	sort.SliceStable(stats.Models, func(i, j int) bool {
		if stats.Models[i].Calls != stats.Models[j].Calls {
			return stats.Models[i].Calls > stats.Models[j].Calls
		}
		return stats.Models[i].Model < stats.Models[j].Model
	})
	return stats
}

// healthCallKey identifies one model call as (run, turn). Turn is normalized
// to its decimal string: JSON round-trips turn numbers as float64 while
// in-memory producers keep int, and "1" must match 1.
func healthCallKey(event Event) string {
	return event.Context.Run + "\x00" + healthTurn(event.Data)
}

// healthApprovalKey mirrors kernel/cost approvalKey: run + operation_id +
// arguments_hash, the identity the kernel uses to dedup repeat approvals.
func healthApprovalKey(event Event) string {
	operationID, _ := event.Data["operation_id"].(string)
	argumentsHash, _ := event.Data["arguments_hash"].(string)
	return event.Context.Run + "\x00" + operationID + "\x00" + argumentsHash
}

func healthTurn(data map[string]any) string {
	switch turn := data["turn"].(type) {
	case string:
		return turn
	case float64:
		return strconv.FormatInt(int64(turn), 10)
	case int:
		return strconv.Itoa(turn)
	case int64:
		return strconv.FormatInt(turn, 10)
	default:
		return ""
	}
}

// healthInt64 tolerantly converts journal data numbers: JSON decoding yields
// float64 while native producers emit int/int64; anything else is absent.
func healthInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		return int64(number), true
	case int:
		return int64(number), true
	case int64:
		return number, true
	default:
		return 0, false
	}
}

// healthPercentile is the nearest-rank percentile of an ascending-sorted
// sample: rank = ceil(p/100*n), 1-based.
func healthPercentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
