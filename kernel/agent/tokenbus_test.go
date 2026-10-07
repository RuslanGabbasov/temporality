package agent

import (
	"testing"
	"time"
)

func drainTokenEvents(t *testing.T, ch <-chan TokenEvent) []TokenEvent {
	t.Helper()
	var events []TokenEvent
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		default:
			return events
		}
	}
}

func TestTokenBusPublishSubscribeAndSnapshot(t *testing.T) {
	bus := NewTokenBus()
	bus.Publish("run-1", 1, "Hello", "")
	bus.Publish("run-1", 1, " world", "")

	ch, snapshot, cancel := bus.Subscribe("run-1")
	defer cancel()
	if snapshot.Type != "snapshot" || snapshot.Turn != 1 || snapshot.Text != "Hello world" {
		t.Fatalf("snapshot = %+v, want turn 1 text \"Hello world\"", snapshot)
	}

	bus.Publish("run-1", 1, "!", "")
	events := drainTokenEvents(t, ch)
	if len(events) != 1 || events[0].Text != "!" {
		t.Fatalf("live events = %+v, want one delta with \"!\"", events)
	}
}

func TestTokenBusTurnChangeResetsLiveText(t *testing.T) {
	bus := NewTokenBus()
	bus.Publish("run-1", 1, "first turn answer", "thinking hard")
	bus.Publish("run-1", 2, "second", "")

	_, snapshot, cancel := bus.Subscribe("run-1")
	defer cancel()
	if snapshot.Turn != 2 || snapshot.Text != "second" || snapshot.Reasoning != "" {
		t.Fatalf("snapshot = %+v, want turn 2 with reset text and reasoning", snapshot)
	}
}

func TestTokenBusCloseDeliversDoneAndDropsEntry(t *testing.T) {
	bus := NewTokenBus()
	bus.Publish("run-1", 1, "partial", "")
	ch, _, cancel := bus.Subscribe("run-1")
	defer cancel()

	bus.Close("run-1")
	events := drainTokenEvents(t, ch)
	if len(events) != 1 || events[0].Type != "done" {
		t.Fatalf("events after close = %+v, want done", events)
	}

	_, snapshot, lateCancel := bus.Subscribe("run-1")
	defer lateCancel()
	if snapshot.Type != "done" {
		t.Fatalf("late subscriber snapshot = %+v, want done", snapshot)
	}
}

func TestTokenBusPublishAfterCloseIsDropped(t *testing.T) {
	bus := NewTokenBus()
	bus.Close("run-1")
	bus.Publish("run-1", 1, "late", "")
	_, snapshot, cancel := bus.Subscribe("run-1")
	defer cancel()
	if snapshot.Type != "done" || snapshot.Text != "" {
		t.Fatalf("snapshot = %+v, want empty done", snapshot)
	}
}

func TestTokenBusSweepDropsIdleEntries(t *testing.T) {
	bus := NewTokenBus()
	bus.Publish("run-1", 1, "text", "")
	bus.mu.Lock()
	r := bus.runs["run-1"]
	bus.mu.Unlock()
	r.mu.Lock()
	r.lastSeen = time.Now().Add(-idleSweep - time.Minute)
	r.mu.Unlock()

	bus.mu.Lock()
	if len(bus.runs) != 1 {
		t.Fatalf("precondition: expected one entry, got %d", len(bus.runs))
	}
	bus.mu.Unlock()

	_, snapshot, cancel := bus.Subscribe("run-1")
	defer cancel()
	if snapshot.Type != "snapshot" || snapshot.Text != "" {
		t.Fatalf("snapshot = %+v, want swept empty snapshot", snapshot)
	}
}
