package cost

// Effort-by-version rollup (docs/agent-quality-loop.md §3.7 follow-up): an
// agent evolution may keep answer quality while quietly making every run
// slower and more expensive. This aggregate folds each run's model.completed
// token totals onto the agent definition version the run executed under
// (journaled on run.started as agent_version), so the evolution view can flag
// «the same work now costs 3× more» next to the pass-rate signal.

import (
	"sort"

	"github.com/temporality-project/temporality/observation"
)

// VersionEffort is one definition version's effort profile.
type VersionEffort struct {
	AgentVersion int     `json:"agent_version"`
	Runs         int     `json:"runs"`
	TotalTokens  int     `json:"total_tokens"`
	AvgTokens    float64 `json:"avg_tokens"`
	AvgTurns     float64 `json:"avg_turns"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
}

// AggregateVersionEffort folds journal events (run.started, run.completed/
// failed/cancelled, model.completed) into per-version effort. Versions with
// no completed runs still appear (runs>0, zeros) so the UI can show thin
// samples honestly. Cancelled/failed runs are excluded from the averages —
// a dead run's token tail is noise, not effort.
func AggregateVersionEffort(events []observation.Event, costPerRun map[string]float64) []VersionEffort {
	type acc struct {
		version   int
		runs      int
		completed int
		tokens    int
		turns     int
		withTurns int
		cost      float64
	}
	versions := map[int]*acc{}
	runVersion := map[string]int{}
	runTurns := map[string]int{}
	runCompleted := map[string]bool{}
	runTokens := map[string]int{}

	for _, ev := range events {
		runID := ev.Context.Run
		switch ev.Type {
		case "run.started":
			if v, ok := ev.Data["agent_version"].(float64); ok && int(v) > 0 {
				runVersion[runID] = int(v)
			}
		case "run.completed", "run.failed", "run.cancelled":
			turns := 0
			if t, ok := ev.Data["turns"].(float64); ok {
				turns = int(t)
			}
			runTurns[runID] = turns
			runCompleted[runID] = ev.Type == "run.completed"
		case "model.completed":
			if total, ok := ev.Data["total_tokens"].(float64); ok {
				runTokens[runID] += int(total)
			}
		}
	}

	for runID, version := range runVersion {
		a := versions[version]
		if a == nil {
			a = &acc{version: version}
			versions[version] = a
		}
		a.runs++
		a.tokens += runTokens[runID]
		if runCompleted[runID] {
			a.completed++
			a.turns += runTurns[runID]
			if runTurns[runID] > 0 {
				a.withTurns++
			}
			a.cost += costPerRun[runID]
		}
	}

	out := make([]VersionEffort, 0, len(versions))
	for _, a := range versions {
		entry := VersionEffort{
			AgentVersion: a.version,
			Runs:         a.runs,
			TotalTokens:  a.tokens,
			TotalCostUSD: a.cost,
		}
		if a.completed > 0 {
			entry.AvgTokens = float64(a.tokens) / float64(a.completed)
			entry.AvgTurns = float64(a.turns) / float64(a.withTurns)
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentVersion < out[j].AgentVersion })
	return out
}
