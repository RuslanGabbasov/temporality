package agent

import (
	"testing"
	"time"
)

// The closed-world check with disabled channel rows (docs/inbound-messages.md
// §5): a disabled row still registers the room and still identifies its
// owner — the caller refuses the work but can say exactly why. Before this
// contract a disabled personal channel made every message of the owner fall
// into the silent unregistered-room path, which read as «the service stopped
// processing all inbound».
func TestIdentifyInboundSenderDisabledChannel(t *testing.T) {
	msg := func(chat, sender string) InboundMessage {
		return InboundMessage{ChatID: chat, Sender: sender, Timestamp: time.Now()}
	}
	users := []UserSenderChannels{
		{UserID: "u1", Channels: []SenderChannel{
			{Address: "!disabled:hs", SenderID: "@human:hs", Enabled: false},
			{Address: "!live:hs", SenderID: "@human:hs", Enabled: true},
		}},
		{UserID: "u2", Channels: []SenderChannel{
			{Address: "!u2:hs", SenderID: "@u2:hs", Enabled: true},
		}},
	}

	// Enabled channel: identified, no disabled flag.
	id, registered, disabled := IdentifyInboundSender(msg("!live:hs", "@human:hs"), users)
	if id != "u1" || !registered || disabled {
		t.Fatalf("enabled: id=%q registered=%v disabled=%v", id, registered, disabled)
	}

	// Disabled channel: the owner is STILL identified and the room STILL
	// registered — the caller sees the disabled flag and answers with the
	// «channel disabled» notice instead of dead-air.
	id, registered, disabled = IdentifyInboundSender(msg("!disabled:hs", "@human:hs"), users)
	if id != "u1" || !registered || !disabled {
		t.Fatalf("disabled: id=%q registered=%v disabled=%v", id, registered, disabled)
	}

	// A foreign sender in the owner's disabled room: registered (the polite
	// rejection reply applies), never identified.
	id, registered, disabled = IdentifyInboundSender(msg("!disabled:hs", "@stranger:hs"), users)
	if id != "" || !registered || disabled {
		t.Fatalf("foreign: id=%q registered=%v disabled=%v", id, registered, disabled)
	}

	// A foreign room stays fully unregistered (the silent anti-spam path).
	id, registered, disabled = IdentifyInboundSender(msg("!elsewhere:hs", "@human:hs"), users)
	if id != "" || registered || disabled {
		t.Fatalf("foreign room: id=%q registered=%v disabled=%v", id, registered, disabled)
	}

	// When one user holds the chat DISABLED and another still holds it
	// enabled, each sender is judged by their own row: the disabled owner is
	// identified with the disabled flag (their row is off), the enabled
	// owner passes normally.
	both := []UserSenderChannels{
		{UserID: "a", Channels: []SenderChannel{{Address: "!shared:hs", SenderID: "@a:hs", Enabled: false}}},
		{UserID: "b", Channels: []SenderChannel{{Address: "!shared:hs", SenderID: "@b:hs", Enabled: true}}},
	}
	id, _, disabled = IdentifyInboundSender(msg("!shared:hs", "@a:hs"), both)
	if id != "a" || !disabled {
		t.Fatalf("shared room, disabled owner: id=%q disabled=%v", id, disabled)
	}
	id, _, disabled = IdentifyInboundSender(msg("!shared:hs", "@b:hs"), both)
	if id != "b" || disabled {
		t.Fatalf("shared room, enabled owner: id=%q disabled=%v", id, disabled)
	}

	// Duplicate rows of the SAME sender (re-registered enabled after a
	// disabled copy): the enabled row wins — the surface is live again.
	dup := []UserSenderChannels{{UserID: "u", Channels: []SenderChannel{
		{Address: "!dup:hs", SenderID: "@u:hs", Enabled: false},
		{Address: "!dup:hs", SenderID: "@u:hs", Enabled: true},
	}}}
	id, _, disabled = IdentifyInboundSender(msg("!dup:hs", "@u:hs"), dup)
	if id != "u" || disabled {
		t.Fatalf("duplicate rows: id=%q disabled=%v — the enabled row must win", id, disabled)
	}

	// No sender identity registered on a disabled row: the room registers,
	// but nobody is identified (the sender-id-missing guard holds).
	noID := []UserSenderChannels{{UserID: "u", Channels: []SenderChannel{{Address: "!r:hs", Enabled: false}}}}
	id, registered, disabled = IdentifyInboundSender(msg("!r:hs", "@anyone:hs"), noID)
	if id != "" || !registered || disabled {
		t.Fatalf("no sender id: id=%q registered=%v disabled=%v", id, registered, disabled)
	}
}
