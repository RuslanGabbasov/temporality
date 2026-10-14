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

func TestTokenBusPublishToolAccumulatesAndSnapshots(t *testing.T) {
	bus := NewTokenBus()
	bus.PublishTool("run-1", 3, "run_command", `{"command":`)
	bus.PublishTool("run-1", 3, "", `["go","test"]}`)

	ch, snapshot, cancel := bus.Subscribe("run-1")
	defer cancel()
	if snapshot.Type != "snapshot" || snapshot.Turn != 3 || snapshot.Tool != "run_command" {
		t.Fatalf("snapshot = %+v, want turn 3 tool run_command", snapshot)
	}
	if snapshot.Args != `{"command":["go","test"]}` || snapshot.Text != "" {
		t.Fatalf("snapshot = %+v, want accumulated args and no text", snapshot)
	}

	bus.PublishTool("run-1", 3, "", " more")
	events := drainTokenEvents(t, ch)
	if len(events) != 1 || events[0].Tool != "run_command" || events[0].Args != " more" {
		t.Fatalf("live events = %+v, want one tool delta carrying the last name", events)
	}
}

func TestTokenBusTurnChangeResetsToolArgs(t *testing.T) {
	bus := NewTokenBus()
	bus.PublishTool("run-1", 1, "plan", `{"goal":`)
	bus.Publish("run-1", 2, "answer text", "")

	_, snapshot, cancel := bus.Subscribe("run-1")
	defer cancel()
	if snapshot.Turn != 2 || snapshot.Tool != "" || snapshot.Args != "" || snapshot.Text != "answer text" {
		t.Fatalf("snapshot = %+v, want turn 2 with tool args reset", snapshot)
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
