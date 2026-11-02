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
// the next poll delay. Position is the human-facing queue place.
type ModelGateVerdictResult struct {
	WaitSeconds int `json:"wait_seconds"`
	Position    int `json:"position"`
}

// ModelGateAdmission: the run's identity + its sandbox requirement + the
// unit quota from its frozen policy snapshot (docs/org-structure.md §24).
type ModelGateAdmission struct {
	RunID     string
	MemoryMB  int
	QuotaMB   int // the unit's concurrent sandbox-memory budget; 0 = none
	QuotaRuns int // the unit's concurrent-runs ceiling; 0 = none
}

func (a *Activities) ModelGateVerdict(ctx context.Context, admission ModelGateAdmission) (ModelGateVerdictResult, error) {
	wait, position := gate().Verdict(admission.RunID, admission.MemoryMB, admission.QuotaMB, admission.QuotaRuns)
	return ModelGateVerdictResult{WaitSeconds: wait, Position: position}, nil
}

// ModelGateBudgetInput carries the run's memory requirement (MB), the unit
// quota, and the operation: acquire (claim the budget), renew (prove the run
// is alive), release (the run finished — hand the budget back).
type ModelGateBudgetInput struct {
	Op        string // acquire | renew | release
	RunID     string
	MemoryMB  int
	QuotaMB   int
	QuotaRuns int
}

type ModelGateBudgetResult struct {
	Admitted bool `json:"admitted"`
}

func (a *Activities) ModelGateBudget(ctx context.Context, input ModelGateBudgetInput) (ModelGateBudgetResult, error) {
	switch input.Op {
	case "acquire":
		return ModelGateBudgetResult{Admitted: gate().Acquire(input.RunID, input.MemoryMB, input.QuotaMB, input.QuotaRuns) == nil}, nil
	case "renew":
		return ModelGateBudgetResult{Admitted: gate().Renew(input.RunID, input.MemoryMB, input.QuotaMB, input.QuotaRuns)}, nil
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
func queueNoticeText(position int) string {
	if position <= 1 {
		return "The server is at capacity: you are next in line — your request starts automatically."
	}
	ahead := position - 1
	return fmt.Sprintf("The server is at capacity: %d request(s) are ahead of yours. Your request starts automatically, no action needed — the answer arrives in this conversation.", ahead)
}
