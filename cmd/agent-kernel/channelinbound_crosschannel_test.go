package main

import (
	"context"
	"testing"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// Reproduction of the user-reported scenario (docs/channels.md §10 closed
// world): the telegram transport config was deleted in the admin settings,
// the user's personal telegram row REMAINS in their profile, and a message
// arrives in their MAIN channel (matrix). The matrix loop must process it —
// a foreign channel's leftover personal row must never cross into the
// matrix loop's registry, and the deleted telegram transport must not stall
// the shared settings loader for other channels.
func TestMatrixInboundUnaffectedByDeletedTelegramTransport(t *testing.T) {
	var sent testSink
	server := matrixIntakeHomeServer(t, "!r1:hs", []any{
		matrixTextEvent("$mx1", "!r1:hs", "@human:hs", "подготовь смету на разработку"),
	}, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			// The profile holds BOTH rows: the live matrix one and the leftover
			// telegram one (address + sender id still filled from when the
			// transport existed).
			Channels: []workspace.UserChannel{
				{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true},
				{Type: "telegram", Address: "424242", SenderID: "424242", Enabled: true},
			},
		}},
	}
	intake := &recordedIntake{runID: "intake-mx1"}
	emit := &recordedEmit{}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.intake = intake.record
	loop.emit = emit.record
	loop.intakeEnabled = true
	// The transport settings as they are after the telegram deletion: no
	// telegram key at all, the matrix one intact and pointing at the test
	// homeserver.
	loop.settings = func(context.Context) (agent.ChannelSettings, error) {
		return agent.ChannelSettings{Channels: map[string]map[string]string{
			"matrix": {"homeserver": server.URL, "access_token": "tok"},
		}}, nil
	}

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if intake.count() != 1 {
		t.Fatalf("intake called %d times, want 1 — the matrix message must be processed regardless of the deleted telegram transport", intake.count())
	}
	msg := intake.messages[0]
	if msg.Channel != "matrix" || msg.ChatID != "!r1:hs" {
		t.Fatalf("intake message = %+v", msg)
	}
	if emit.count() == 0 || emit.types[0] != "intake.received" {
		t.Fatalf("events = %v", emit.types)
	}
}
