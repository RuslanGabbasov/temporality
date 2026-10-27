package agent

import (
	"fmt"
	"strings"
	"sync"
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

// A publish after Close REVIVES the entry: a publish is proof the run is
// still generating, and a stale close (a terminal-looking describe probe
// during a restart, a previous SSE connection's sweep) must not poison every
// later model call of the run — deltas would drop silently and every
// reconnecting UI would get an instant done forever.
func TestTokenBusPublishAfterCloseRevives(t *testing.T) {
	bus := NewTokenBus()
	bus.Close("run-1")
	// A subscriber connected before the revival sees done-on-subscribe.
	// A subscriber connected before the revival sees done-on-subscribe.
	_, snapshot, cancel := bus.Subscribe("run-1")
	if snapshot.Type != "done" {
		t.Fatalf("pre-revival snapshot = %+v, want done", snapshot)
	}
	cancel()

	// The next publish revives: deltas flow again and new subscribers get
	// live snapshots instead of a terminal done.
	bus.Publish("run-1", 1, "revived", "")
	events, snapshot2, cancel2 := bus.Subscribe("run-1")
	defer cancel2()
	if snapshot2.Type != "snapshot" || snapshot2.Text != "revived" {
		t.Fatalf("post-revival snapshot = %+v, want the revived text", snapshot2)
	}
	bus.Publish("run-1", 1, " more", "")
	if ev := <-events; ev.Type != "delta" || ev.Text != " more" {
		t.Fatalf("delta = %+v", ev)
	}
}

func TestTokenBusSweepDropsIdleEntries(t *testing.T) {
	bus := NewTokenBus()
	bus.Publish("run-1", 1, "text", "")
	s := bus.shardFor("run-1")
	s.mu.Lock()
	r := s.runs["run-1"]
	s.mu.Unlock()
	r.mu.Lock()
	r.lastSeen = time.Now().Add(-idleSweep - time.Minute)
	r.mu.Unlock()

	s.mu.Lock()
	if len(s.runs) != 1 {
		t.Fatalf("precondition: expected one entry, got %d", len(s.runs))
	}
	s.mu.Unlock()

	_, snapshot, cancel := bus.Subscribe("run-1")
	defer cancel()
	if snapshot.Type != "snapshot" || snapshot.Text != "" {
		t.Fatalf("snapshot = %+v, want swept empty snapshot", snapshot)
	}
}

// tokenShardIndex must be a pure function of the runID: always in range and
// stable across calls — otherwise a run's entry would be looked up in one
// shard and created in another.
func TestTokenBusShardIndexDeterministic(t *testing.T) {
	ids := []string{"", "run-1", "run-2", "run-10", "RU", "поток-1", strings.Repeat("long-id", 64)}
	for _, id := range ids {
		first := tokenShardIndex(id)
		if first < 0 || first >= tokenBusShards {
			t.Fatalf("tokenShardIndex(%q) = %d, want within [0,%d)", id, first, tokenBusShards)
		}
		for i := 0; i < 100; i++ {
			if got := tokenShardIndex(id); got != first {
				t.Fatalf("tokenShardIndex(%q) unstable: %d vs %d", id, got, first)
			}
		}
	}
}

func TestTokenBusPublishesLandInDistinctShards(t *testing.T) {
	bus := NewTokenBus()
	first, second := "", ""
	for i := 0; i < 64 && second == ""; i++ {
		id := fmt.Sprintf("run-%d", i)
		if first == "" {
			first = id
			continue
		}
		if tokenShardIndex(id) != tokenShardIndex(first) {
			second = id
		}
	}
	if second == "" {
		t.Fatal("no run pair hashing to distinct shards in 64 ids")
	}

	bus.Publish(first, 1, "A", "")
	bus.Publish(second, 1, "B", "")

	a, b := bus.shardFor(first), bus.shardFor(second)
	a.mu.Lock()
	_, hasFirstInA := a.runs[first]
	_, hasSecondInA := a.runs[second]
	a.mu.Unlock()
	b.mu.Lock()
	_, hasSecondInB := b.runs[second]
	b.mu.Unlock()
	if !hasFirstInA || hasSecondInA || !hasSecondInB {
		t.Fatalf("shard placement wrong: %q in shardA=%v, %q in shardA=%v, %q in shardB=%v",
			first, hasFirstInA, second, hasSecondInA, second, hasSecondInB)
	}
}

// Concurrent publishes across runs must serialize only per run: each run's
// deltas arrive complete, ordered, and never mixed with another run's.
func TestTokenBusConcurrentPublishesAcrossRuns(t *testing.T) {
	bus := NewTokenBus()
	const workers = 8
	const deltas = 200

	// Subscribers attached up front: buffered at 512 they must observe every
	// delta of their own run and nothing from any other run.
	chans := make([]<-chan TokenEvent, workers)
	for w := 0; w < workers; w++ {
		ch, _, cancel := bus.Subscribe(fmt.Sprintf("run-%d", w))
		defer cancel()
		chans[w] = ch
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < deltas; i++ {
				bus.Publish(fmt.Sprintf("run-%d", w), 1, fmt.Sprintf("w%d-d%d ", w, i), "")
			}
		}(w)
	}
	wg.Wait()

	for w := 0; w < workers; w++ {
		events := drainTokenEvents(t, chans[w])
		if len(events) != deltas {
			t.Fatalf("run-%d: %d events, want %d", w, len(events), deltas)
		}
		var want strings.Builder
		for i, ev := range events {
			expect := fmt.Sprintf("w%d-d%d ", w, i)
			if ev.Text != expect {
				t.Fatalf("run-%d event %d = %q, want %q", w, i, ev.Text, expect)
			}
			want.WriteString(expect)
		}
		_, snapshot, cancel := bus.Subscribe(fmt.Sprintf("run-%d", w))
		defer cancel()
		if snapshot.Text != want.String() {
			t.Fatalf("run-%d snapshot = %q, want %q", w, snapshot.Text, want.String())
		}
	}
}

// Sweeps racing publishes must not deadlock: shard.mu -> run.mu is the only
// nesting, and sweeps take shards one at a time. Both the direct sweep and
// the production trigger (Subscribe) run against live publishers.
func TestTokenBusSweepConcurrentWithPublishes(t *testing.T) {
	bus := NewTokenBus()
	const workers = 8
	const deltas = 100

	// An already-idle entry: sweeps must delete it while publishers are live.
	bus.Publish("run-old", 1, "stale", "")
	s := bus.shardFor("run-old")
	s.mu.Lock()
	r := s.runs["run-old"]
	s.mu.Unlock()
	r.mu.Lock()
	r.lastSeen = time.Now().Add(-idleSweep - time.Minute)
	r.mu.Unlock()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < deltas; i++ {
				bus.Publish(fmt.Sprintf("run-%d", w), 1, "x", "")
			}
		}(w)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			bus.sweep()
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, _, cancel := bus.Subscribe(fmt.Sprintf("run-%d", i%workers))
			cancel()
		}
	}()

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("token bus sweep/publish deadlocked")
	}
}
