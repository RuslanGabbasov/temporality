package agent

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// Model gate (the kernel's memory quota): the model call is the heaviest
// thing a run does — streaming buffers, provider retries, the conversation
// itself. When more runs call the model than the host can carry, the host
// swaps and the OOM killer decides for everyone. The gate is admission
// control with a queue: runs START as usual and are ANSWERED in order —
// a run at the gate polls the verdict, sleeps when it says wait, and the
// wait does not consume the run's wall-clock budget.
//
// The state is in-process on purpose: a worker restart drops the counter
// with the dead process, and the replaying workflows re-acquire — nothing to
// recover, nothing to leak. Capacity is protected by re-checking inside the
// activity: the verdict says "go", CallModel re-verifies with a bounded
// grace, and a lost race just comes back as ModelGateBusy for another poll.

type ModelGate struct {
	mu       sync.Mutex
	active   int
	max      int
	floorPct float64 // pause admitting below this % of MemAvailable; 0 = no check
	memory   func() (total, available uint64, ok bool)
}

func ConfigureModelGate(max int, floorPct float64, memory func() (total, available uint64, ok bool)) {
	gateMu.Lock()
	defer gateMu.Unlock()
	if max <= 0 {
		max = 1
	}
	modelGate = &ModelGate{max: max, floorPct: floorPct, memory: memory}
}

var (
	gateMu    sync.Mutex
	modelGate *ModelGate
)

// gate returns the configured gate, or a permissive default when the binary
// never configured one (tests, minimal deployments) — the gate must never
// wedge a run that opted out implicitly.
func gate() *ModelGate {
	gateMu.Lock()
	defer gateMu.Unlock()
	if modelGate == nil {
		modelGate = &ModelGate{max: 1 << 30}
	}
	return modelGate
}

// ModelGateVerdict is what the polling workflow needs: zero wait means go
// now; otherwise sleep WaitSeconds and ask again. Position is the queue
// place reported to the user ("3rd in line") — 1 means next.
func (g *ModelGate) Verdict() (waitSeconds int, position int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active < g.max && !g.memoryLow() {
		return 0, 0
	}
	// Everyone ahead of the caller is either holding a slot or waiting in
	// front; position counts the callers that will be admitted first.
	return 10, g.active - g.max + 1
}

// Enter claims a slot with a short bounded grace (the activity must not sit
// long — the workflow owns the waiting). ModelGateBusy sends the caller back
// into the poll loop.
func (g *ModelGate) Enter() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active >= g.max {
		return ErrModelGateBusy
	}
	if g.memoryLow() {
		return ErrModelGateBusy
	}
	g.active++
	return nil
}

// Leave releases a claimed slot. Doubt-proof: a double release pins the
// counter at its ceiling rather than pushing it negative.
func (g *ModelGate) Leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active > 0 {
		g.active--
	}
}

// ErrModelGateBusy is the typed "not yet" answer: the workflow polls again
// instead of failing.
var ErrModelGateBusy = errors.New("model gate: at capacity")

// memoryLow reports whether the host is under the configured floor of
// available memory. Missing or unreadable memory data never pauses the gate
// — a probe failure must not wedge every run.
func (g *ModelGate) memoryLow() bool {
	if g.floorPct <= 0 || g.memory == nil {
		return false
	}
	total, available, ok := g.memory()
	if !ok || total == 0 {
		return false
	}
	pct := float64(available) / float64(total) * 100
	return pct < g.floorPct
}

// QueueNoticeMilestones: the queue positions a queued run announces to its
// starter. Entry position + these thresholds — not every poll, that would
// spam the channels ten times a minute.
var QueueNoticeMilestones = []int{3, 1}

// ModelGateQueueTimeout bounds how long a run may wait at the gate: the
// day-scale drain the quota promises. A run queued past it ends honestly
// (nothing ran, nothing uncertain) instead of surfacing on a swapping host.
const ModelGateQueueTimeout = 6 * time.Hour

// sanitizeQueueText keeps channel payloads one-line-safe.
func sanitizeQueueText(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
}
