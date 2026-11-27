package main

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// routerShadowStore is the shadow-mode persistence surface
// (docs/conversational-runtime.md §23). The production *workspace.Store
// implements it; legacy test fakes opt out, keeping the old intake tests on
// their old paths.
type routerShadowStore interface {
	RecordRouterShadow(context.Context, *workspace.RouterShadowRow) error
}

// shadowExcerptLen caps the stored message text: the shadow table exists for
// agreement statistics, not as a second message store (and inbound bodies
// have their own retention policy).
const shadowExcerptLen = 200

// shadowAgreement maps a legacy intake classification onto the runtime
// execution it should have produced, and compares. The mapping is semantic,
// not literal: legacy «amendment» is served by the runtime as a lead-first
// continuation (direct inside a talk with active work) or as new work
// (agent_execution) — both are agreement; a bare deterministic direct on a
// legacy task means the router would have JUST CHATTED where intake started
// work, which is exactly the disagreement shadow mode exists to find.
func shadowAgreement(classification string, row workspace.RouterShadowRow) string {
	if row.RuntimeLevel == "not_evaluated" {
		return "incomparable" // no runtime half: the message never reached a runtime-first loop
	}
	switch classification {
	case "task":
		if row.RuntimeExecution == "agent_execution" || row.RuntimeExecution == "delegated_execution" {
			return "agree"
		}
	case "amendment":
		if row.RuntimeExecution == "agent_execution" || row.RuntimeExecution == "delegated_execution" {
			return "agree"
		}
		if row.RuntimeExecution == "direct" && row.RuntimeLevel == "lead_first" {
			return "agree"
		}
	case "status", "reply", "chatter", "cancel":
		if row.RuntimeExecution == "direct" || row.RuntimeExecution == "clarification" {
			return "agree"
		}
	default:
		return "incomparable"
	}
	return "disagree"
}

// shadowExcerpt truncates to a fixed rune budget.
func shadowExcerpt(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > shadowExcerptLen {
		return string(runes[:shadowExcerptLen])
	}
	return string(runes)
}

// shadowEnabled reads the per-channel opt-out. Shadow scoring is
// deterministic-only (level 1 + lead-first, no model call), so the default
// is on: the row is one small INSERT, and the whole point of §23 is to
// accumulate agreement data without asking anyone to remember a flag.
func shadowEnabled(channelSettings map[string]string) bool {
	switch strings.ToLower(strings.TrimSpace(channelSettings["conversation_shadow_enabled"])) {
	case "false", "0", "off", "no":
		return false
	default:
		return true
	}
}

// recordRouterShadow persists the runtime half of one shadow row. Failures
// degrade to a one-time disable: a store without the 0046 migration must not
// warn on every message.
func (l *channelInboundLoop) recordRouterShadow(ctx context.Context, channelSettings map[string]string, message agent.InboundMessage, conversation workspace.Conversation, decision ConversationIntentDecision, served bool) {
	if l.shadowBroken == nil {
		l.shadowBroken = new(atomic.Bool)
	}
	if l.shadowBroken.Load() || !shadowEnabled(channelSettings) || message.Encrypted {
		return
	}
	store, ok := l.store.(routerShadowStore)
	if !ok {
		return
	}
	row := &workspace.RouterShadowRow{
		InboundMessageID:  stableConversationMessageID(message.Channel, message.ChatID, message.ID),
		Channel:           message.Channel,
		ChatID:            message.ChatID,
		ConversationID:    conversation.ID,
		OrganizationID:    conversation.OrganizationID,
		TextExcerpt:       shadowExcerpt(message.Text),
		HasAttachments:    len(message.Files) > 0,
		RuntimeIntent:     decision.Intent,
		RuntimeExecution:  decision.Execution,
		RuntimeConfidence: decision.Confidence,
		RuntimeLevel:      decision.RouterLevel,
		RuntimeServed:     served,
	}
	if err := store.RecordRouterShadow(ctx, row); err != nil {
		l.shadowBroken.Store(true)
		l.log.Warn("router shadow record (disabling for this loop)", "channel", message.Channel, "message", message.ID, "error", err)
	}
}
