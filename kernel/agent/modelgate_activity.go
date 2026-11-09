package agent

import (
	"context"
	"fmt"
	"strings"
)

// The queue side of the memory budget gate: a fast in-process verdict the
// waiting workflows poll, budget claiming/renewal/release, and a plain-text
// delivery of the queue position to the run starter's channels — every
// enabled channel at once, WITHOUT the human_request row (a queue notice is
// a status, not a question).

const (
	// ActivityModelGateVerdict answers "is it my turn" in microseconds.
	ActivityModelGateVerdict = "kernel.model_gate_verdict"
	// ActivityModelGateBudget claims/renews/releases the run's memory budget
	// (op: acquire | renew | release).
	ActivityModelGateBudget = "kernel.model_gate_budget"
	// ActivityNotifyActorText delivers one plain text to the actor's enabled
	// channels (no inbox row, no answer round-trip).
	ActivityNotifyActorText = "kernel.notify_actor_text"
)

// ModelGateVerdictResult: WaitSeconds == 0 admits the run; otherwise it is
// the next poll delay. Position is the human-facing queue place; Reason and
// the ledger numbers explain the wait («квота утекла» is almost always the
// memory dimension being full, not phantom sessions).
type ModelGateVerdictResult struct {
	WaitSeconds int    `json:"wait_seconds"`
	Position    int    `json:"position"`
	Reason      string `json:"reason,omitempty"`
	UsedMB      int    `json:"used_mb,omitempty"`
	BudgetMB    int    `json:"budget_mb,omitempty"`
	Admitted    int    `json:"admitted,omitempty"`
	MaxRuns     int    `json:"max_runs,omitempty"`
}

// ModelGateAdmission: the run's identity + its sandbox requirement + the
// unit quota from its frozen policy snapshot (docs/org-structure.md §24).
type ModelGateAdmission struct {
	RunID     string
	MemoryMB  int
	QuotaMB   int // the unit's concurrent sandbox-memory budget; 0 = none
	QuotaRuns int // the unit's concurrent-runs ceiling; 0 = none
	// ScratchKey ("project/run") ties the entry to its sandbox container's
	// stats for the actual-usage reconciliation.
	ScratchKey string
}

func (a *Activities) ModelGateVerdict(ctx context.Context, admission ModelGateAdmission) (ModelGateVerdictResult, error) {
	verdict := gate().Verdict(admission.RunID, admission.MemoryMB, admission.QuotaMB, admission.QuotaRuns, admission.ScratchKey)
	return ModelGateVerdictResult{
		WaitSeconds: verdict.WaitSeconds, Position: verdict.Position,
		Reason: verdict.Reason, UsedMB: verdict.UsedMB, BudgetMB: verdict.BudgetMB,
		Admitted: verdict.Admitted, MaxRuns: verdict.MaxRuns,
	}, nil
}

// ModelGateBudgetInput carries the run's memory requirement (MB), the unit
// quota, and the operation: acquire (claim the budget), renew (prove the run
// is alive), release (the run finished — hand the budget back).
type ModelGateBudgetInput struct {
	Op         string // acquire | renew | release
	RunID      string
	MemoryMB   int
	QuotaMB    int
	QuotaRuns  int
	ScratchKey string
}

type ModelGateBudgetResult struct {
	Admitted bool `json:"admitted"`
}

func (a *Activities) ModelGateBudget(ctx context.Context, input ModelGateBudgetInput) (ModelGateBudgetResult, error) {
	switch input.Op {
	case "acquire":
		return ModelGateBudgetResult{Admitted: gate().Acquire(input.RunID, input.MemoryMB, input.QuotaMB, input.QuotaRuns, input.ScratchKey) == nil}, nil
	case "renew":
		return ModelGateBudgetResult{Admitted: gate().Renew(input.RunID, input.MemoryMB, input.QuotaMB, input.QuotaRuns, input.ScratchKey)}, nil
	case "release":
		gate().Release(input.RunID)
		return ModelGateBudgetResult{Admitted: false}, nil
	}
	return ModelGateBudgetResult{}, fmt.Errorf("unknown gate op %q", input.Op)
}

// NotifyActorTextInput: recipient-less by design — the text always goes to
// the run's own starter (the run's actor), on every channel they enabled.
type NotifyActorTextInput struct {
	ActorID string
	Text    string
}

// NotifyActorText fans the text out to every enabled channel of the actor.
// Best-effort by contract: delivery failures never fail the caller — a lost
// queue notice must not fail a queued run.
func (a *Activities) NotifyActorText(ctx context.Context, input NotifyActorTextInput) error {
	text := sanitizeQueueText(input.Text)
	if text == "" || input.ActorID == "" {
		return nil
	}
	users, ok := a.fetchWorkspaceUsers(ctx)
	if !ok {
		return nil
	}
	actor, found := ResolveRecipient(input.ActorID, input.ActorID, users)
	if !found {
		return nil
	}
	settings := a.transports(ctx)
	for _, channel := range actor.Channels {
		if !channel.Enabled || strings.TrimSpace(channel.Address) == "" {
			continue
		}
		driver := channelDriverByType(channel.Type)
		if driver == nil || !driver.IsConfigured(settings.Channels[channel.Type]) {
			continue
		}
		_ = driver.SendText(ctx, a.HTTP, settings.Channels[channel.Type], channel.Address, OutboundText{Body: text})
	}
	return nil
}

// queueNoticeText renders the human-facing queue line. Position 1 reads as
// "next up" — no fake arithmetic about people ahead.
func queueNoticeText(verdict ModelGateVerdictResult) string {
	reason := queueReasonText(verdict)
	if verdict.Position <= 1 {
		return sanitizeQueueText(fmt.Sprintf("The server is at capacity%s: you are next in line — your request starts automatically.", reason))
	}
	ahead := verdict.Position - 1
	return sanitizeQueueText(fmt.Sprintf("The server is at capacity%s: %d request(s) are ahead of yours. Your request starts automatically, no action needed — the answer arrives in this conversation.", reason, ahead))
}

// queueReasonText names the dimension that is full — the sentence that turns
// «why is this queued with 64 allowed sessions?» into an answer instead of a
// bug hunt (the count ceiling is rarely the binding one; sandbox memory is).
func queueReasonText(verdict ModelGateVerdictResult) string {
	switch verdict.Reason {
	case GateBlockedMemoryBudget:
		return fmt.Sprintf(" — the sandbox memory budget is reserved: %d GB of %d GB in use", verdict.UsedMB/1024, verdict.BudgetMB/1024)
	case GateBlockedMemoryFloor:
		return " — the host is low on free memory, admissions pause until it recovers"
	case GateBlockedCountCeiling:
		return fmt.Sprintf(" — the concurrent-runs ceiling is full: %d of %d", verdict.Admitted, verdict.MaxRuns)
	case GateBlockedQuotaRuns:
		return fmt.Sprintf(" — the unit's concurrent-runs quota is full: %d", verdict.Admitted)
	case GateBlockedQuotaMemory:
		return " — the unit's sandbox memory quota is reserved"
	default:
		return ""
	}
}
