package main

import (
	"context"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// TestChannelInboundIntakeRejectsDisabledChannelWithNotice: the owner writes
// from a channel they DISABLED in their profile. The identity still matches
// (the room stays registered), but no intake run starts — and the refusal is
// LOUD: the user hears exactly why, instead of the silent unregistered-room
// dead-air that read as «the service stopped processing all inbound»
// (the disable-a-channel bug report).
func TestChannelInboundIntakeRejectsDisabledChannelWithNotice(t *testing.T) {
	var sent testSink
	server := matrixIntakeHomeServer(t, "!r1:hs", []any{
		matrixTextEvent("$m1", "!r1:hs", "@human:hs", "подготовь смету на разработку"),
	}, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			// The row exists with the right address + sender identity, but the
			// user toggled it off in their profile settings.
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: false}},
		}},
	}
	emit := &recordedEmit{}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.intake = func(context.Context, agent.InboundMessage, string) (string, error) {
		t.Fatal("intake must not start from a disabled channel")
		return "", nil
	}
	loop.emit = emit.record
	loop.intakeEnabled = true

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, ok := store.inboundRow("$m1")
	if !ok {
		t.Fatal("message not recorded")
	}
	if latest, _ := store.lastUpdate(row.ID); latest.Status != workspace.InboundStatusRejected {
		t.Fatalf("row not rejected: %+v", latest)
	}
	// The rejection names the OWNER (identified despite the disabled row).
	if latest, _ := store.lastUpdate(row.ID); latest.SenderUserID != "u1" {
		t.Fatalf("rejection must attribute the owner, got %+v", latest)
	}
	if emit.count() != 1 || emit.types[0] != "intake.rejected" || emit.rawData[0]["reason"] != "channel_disabled" {
		t.Fatalf("unexpected events: %v %+v", emit.types, emit.rawData)
	}
	if sent.len() != 1 || !strings.Contains(sent.item(0), "отключён") {
		t.Fatalf("expected the disabled-channel notice, got %v", sent.all())
	}
}
