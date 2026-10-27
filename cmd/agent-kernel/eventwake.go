// Event wake infrastructure for the SSE run streams: a per-project
// multiplexer that turns PG NOTIFY wake-ups (see startEventWakeRelay) into
// bounded sleeps, plus the relay goroutine feeding it from the journal's
// LISTEN subscription. Correctness never depends on a wake-up: the streams
// keep their 1.5s poll as the fallback, a wake-up only lets them poll early.
package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/temporality-project/temporality/observation/postgres"
)

// eventWake multiplexes "the journal changed" signals per project: waiters
// register under their project, Notify fires all waiters of that project
// without blocking (buffer 1 per waiter — a waiter busy with its poll keeps
// one pending signal, duplicates are dropped).
type eventWake struct {
	mu      sync.Mutex
	waiters map[string][]chan struct{}
}

func newEventWake() *eventWake {
	return &eventWake{waiters: make(map[string][]chan struct{})}
}

// Wait blocks until Notify(project) fires (true), maxWait elapses or ctx is
// done (false). An empty project acts as the wildcard subscription — it is
// woken by every notification.
func (w *eventWake) Wait(ctx context.Context, project string, maxWait time.Duration) bool {
	if maxWait <= 0 {
		return false
	}
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	w.waiters[project] = append(w.waiters[project], ch)
	w.mu.Unlock()
	defer w.remove(project, ch)
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// Notify wakes every waiter of project. An empty project is the broadcast
// form (the NOTIFY payload was empty): it wakes everyone, including
// wildcard waiters. Sends happen outside the lock and never block.
func (w *eventWake) Notify(project string) {
	w.mu.Lock()
	var targets []chan struct{}
	if project == "" {
		for _, list := range w.waiters {
			targets = append(targets, list...)
		}
	} else {
		targets = append(targets, w.waiters[project]...)
		targets = append(targets, w.waiters[""]...)
	}
	w.mu.Unlock()
	for _, ch := range targets {
		wakeSignal(ch)
	}
}

// remove unregisters a finished waiter, dropping the project key once its
// last waiter leaves so the map cannot grow with idle projects.
func (w *eventWake) remove(project string, ch chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	list := w.waiters[project]
	for i, c := range list {
		if c == ch {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(w.waiters, project)
		return
	}
	w.waiters[project] = list
}

// wakeSignal delivers one signal without blocking: when a signal is already
// pending the new one is dropped — the waiter wakes once and re-polls.
func wakeSignal(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// startEventWakeRelay subscribes to the journal change channel on a
// dedicated LISTEN connection and feeds the mux: every notification wakes
// the streams of its payload project, an empty payload wakes them all.
// Boot-time only; on failure it logs and returns — the streams fall back to
// plain 1.5s polling and nothing else in the kernel is affected.
func startEventWakeRelay(ctx context.Context, store *postgres.Store, wake *eventWake, log *slog.Logger) {
	projects, unsubscribe, err := store.SubscribeEventProjects(ctx)
	if err != nil {
		log.Warn("event wake: LISTEN unavailable; run streams fall back to 1.5s polling", "error", err)
		return
	}
	go func() {
		defer unsubscribe()
		for project := range projects {
			wake.Notify(project)
		}
	}()
	log.Info("event wake: listening for journal changes", "channel", "observation_events_changed")
}
