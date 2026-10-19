package observation

import (
	"sort"
	"time"
)

// Per-run human factors (docs/supervisor-model.md §7): the same estimation the
// health aggregation applies to a window (ComputeHealthStats), applied to one
// run's events — so a run, a saga or a project can quote what the session cost
// its humans in attention (HAB) and queue wait (HWT) without rescanning raw
// events at every render. The events are the persistence; this is the reader.
//
// RunHumanFactors carries measured wait plus estimated attention:
//   - HWT: sum of (resolved − requested) over the approval machinery
//     (approvals and ask_human share it), wall-clock, measured;
//   - HAB: decisions × approval coefficient + ask answers (base + typed
//     length) + reading volume of generated conclusions (output tokens).

type RunHumanFactors struct {
	ApprovalDecisions int   `json:"approval_decisions"`
	AskHumanAnswered  int   `json:"ask_human_answered"`
	AutoGranted       int   `json:"auto_granted"`
	HWTSeconds        int64 `json:"hwt_seconds"`
	HWTMSAvg          int64 `json:"hwt_ms_avg"`
	HABSeconds        int64 `json:"hab_seconds"`
}

// ComputeRunHumanFactors mirrors ComputeHealthStats's human-factor block for
// one run. Events may arrive in any order; they are sorted like the health
// aggregation so pending/resolved pairing is stable.
func ComputeRunHumanFactors(events []Event, human HumanFactors) RunHumanFactors {
	if human.ApprovalSeconds <= 0 {
		human = DefaultHumanFactors
	}
	if human.AnswerBaseSeconds <= 0 || human.ReadingWPM <= 0 {
		human = DefaultHumanFactors
	}
	factors := RunHumanFactors{}
	ordered := make([]Event, len(events))
	copy(ordered, events)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].OccurredAt.Equal(ordered[j].OccurredAt) {
			return ordered[i].OccurredAt.Before(ordered[j].OccurredAt)
		}
		return ordered[i].EventID < ordered[j].EventID
	})
	pending := map[string]time.Time{}
	var waitMS int64
	resolved := 0
	outputTokens := int64(0)
	for _, event := range ordered {
		switch event.Type {
		case "approval.requested":
			pending[healthApprovalKey(event)] = event.OccurredAt
		case "approval.granted", "approval.rejected", "approval.timed_out":
			switch event.Type {
			case "approval.granted":
				factors.ApprovalDecisions++
			case "approval.rejected":
				factors.ApprovalDecisions++
			default:
				// A timeout is nobody's attention — no HAB, and the wait does
				// not count either: nobody was waiting by then.
			}
			if requestedAt, ok := pending[healthApprovalKey(event)]; ok {
				if event.Type != "approval.timed_out" {
					waitMS += max64(0, event.OccurredAt.Sub(requestedAt).Milliseconds())
					resolved++
				}
				delete(pending, healthApprovalKey(event))
			}
		case "approval.auto_granted":
			factors.AutoGranted++
		case "human.requested":
			pending[healthApprovalKey(event)] = event.OccurredAt
		case "human.answered":
			factors.ApprovalDecisions++
			factors.AskHumanAnswered++
			if requestedAt, ok := pending[healthApprovalKey(event)]; ok {
				waitMS += max64(0, event.OccurredAt.Sub(requestedAt).Milliseconds())
				resolved++
				delete(pending, healthApprovalKey(event))
			}
			factors.HABSeconds += int64(human.AnswerBaseSeconds)
			if response, _ := event.Data["response"].(string); response != "" {
				factors.HABSeconds += int64(len([]rune(response)) / 5 * 60 / human.ReadingWPM)
			}
		case "human.timed_out", "human.cancelled":
			// Cancelled still cost attention (the human read and decided to
			// cancel); a timeout did not.
			if _, ok := pending[healthApprovalKey(event)]; ok {
				if event.Type == "human.cancelled" {
					factors.ApprovalDecisions++
				}
				delete(pending, healthApprovalKey(event))
			}
		case "model.completed":
			if tokens, _ := event.Data["output_tokens"].(float64); tokens > 0 {
				outputTokens += int64(tokens)
			}
		}
	}
	factors.HWTSeconds = waitMS / 1000
	if resolved > 0 {
		factors.HWTMSAvg = waitMS / int64(resolved)
	}
	factors.HABSeconds += outputTokens * 3 / 4 * 60 / int64(human.ReadingWPM)
	return factors
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
