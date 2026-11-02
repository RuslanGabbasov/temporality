package agent

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The memory budget gate (the kernel's OOM guard), v2 — per-run accounting.
// Every run declares its sandbox memory requirement (SandboxSpec.Memory, the
// same --memory its container gets); the gate admits a run when the SUM of
// the admitted runs' requirements plus the newcomer fits the host budget,
// and queues it otherwise. Runs are different: a light chat and a heavy code
// agent with a 4g sandbox must not cost the same — a flat call-count cap
// cannot express that, a ledger can.
//
// The state is in-process on purpose: a worker restart drops the ledger with
// the dead process, and the replaying workflows re-acquire — nothing to
// recover, nothing to leak.
//
// Lifecycle: admitted runs renew on every turn (Renew), and entries that
// have not renewed within the TTL are dropped as dead — a run that died
// mid-work releases its budget through expiry, not through an explicit
// call, because a workflow's exit paths are many.
type ModelGate struct {
	mu       sync.Mutex
	max      int     // absolute concurrent-runs ceiling (runaway guard)
	budgetMB int     // the host's runnable budget in MB
	floorPct float64 // pause admitting below this % of MemAvailable
	memory   func() (uint64, uint64, bool)
	entries  map[string]*budgetEntry
	order    []string // FIFO of queued run ids, for the reported position
	ttl      time.Duration
}

type budgetEntry struct {
	memoryMB  int
	lastRenew time.Time
}

var (
	gateMu    sync.Mutex
	modelGate *ModelGate
)

// gateDefaultRunMemoryMB is the requirement of a run whose sandbox profile
// carries no explicit memory — the kernel's own KERNEL_SANDBOX_MEMORY
// default, mirrored here at boot.
var gateDefaultRunMemoryMB = 1024

// sandboxMemorySpec returns the run's declared sandbox memory spec (the
// same --memory its container gets); empty when the profile says nothing.
func sandboxMemorySpec(input RunInput) string {
	if input.Sandbox != nil {
		return strings.TrimSpace(input.Sandbox.Memory)
	}
	return ""
}

// ConfigureModelGate installs the budget: max concurrent runs (absolute
// runaway guard), the host budget in MB, the memory floor percent, the
// memory probe (nil = no floor check), and the per-run default requirement
// for sandboxes that declare no explicit memory.
func ConfigureModelGate(max int, budgetMB int, floorPct float64, memory func() (uint64, uint64, bool), defaultRunMB int) {
	gateMu.Lock()
	defer gateMu.Unlock()
	if max <= 0 {
		max = 1
	}
	if budgetMB <= 0 {
		budgetMB = 1 << 30 / (1 << 20) // 1 GB in MB, defensive
	}
	if defaultRunMB <= 0 {
		defaultRunMB = 1024
	}
	modelGate = &ModelGate{
		max:      max,
		budgetMB: budgetMB,
		floorPct: floorPct,
		memory:   memory,
		entries:  map[string]*budgetEntry{},
		ttl:      30 * time.Minute,
	}
	gateDefaultRunMemoryMB = defaultRunMB
}

// gate returns the configured gate, or a permissive default when the binary
// never configured one (tests, minimal deployments) — the gate must never
// wedge a run that opted out implicitly.
func gate() *ModelGate {
	gateMu.Lock()
	defer gateMu.Unlock()
	if modelGate == nil {
		modelGate = &ModelGate{
			max:      1 << 30,
			budgetMB: 1 << 30,
			entries:  map[string]*budgetEntry{},
		}
	}
	return modelGate
}

// ErrModelGateBusy is the typed "not yet" answer for the activity path: the
// workflow polls again instead of failing.
var ErrModelGateBusy = errors.New("model gate: at capacity")

// ParseMemoryMB reads a docker-style size ("1g", "512m", "256k", plain
// bytes) into whole megabytes. Zero on anything unparseable — the caller's
// default applies.
func ParseMemoryMB(raw string) int {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return 0
	}
	multiplier := 1
	switch {
	case strings.HasSuffix(s, "g"), strings.HasSuffix(s, "gb"):
		multiplier = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "gb"), "g")
	case strings.HasSuffix(s, "m"), strings.HasSuffix(s, "mb"):
		multiplier = 1
		s = strings.TrimSuffix(strings.TrimSuffix(s, "mb"), "m")
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "kb"):
		multiplier = 0 // sub-MB: rounds to zero, a 256k sandbox cannot count
		s = strings.TrimSuffix(strings.TrimSuffix(s, "kb"), "k")
	}
	value, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return value * multiplier
}

// Verdict answers "is it my turn": zero wait admits a run with the given
// sandbox memory; otherwise WaitSeconds is the next poll delay and Position
// is the human-facing queue place (1 = next).
func (g *ModelGate) Verdict(runID string, memoryMB int) (waitSeconds int, position int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	if memoryMB <= 0 {
		memoryMB = 512
	}
	if g.fitsLocked(memoryMB) {
		return 0, 0
	}
	return 10, len(g.order) + 1
}

// Acquire claims the budget for a run. A lost race with another run comes
// back as ModelGateBusy — the workflow polls again.
func (g *ModelGate) Acquire(runID string, memoryMB int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	if memoryMB <= 0 {
		memoryMB = 512
	}
	if len(g.entries)+1 > g.max || !g.fitsLocked(memoryMB) {
		return ErrModelGateBusy
	}
	g.entries[runID] = &budgetEntry{memoryMB: memoryMB, lastRenew: time.Now()}
	g.order = append(g.order, runID)
	return nil
}

// Renew marks an admitted run alive, extending its ledger entry past the
// TTL. Unknown ids are admitted on the spot (the renewer might be a run
// whose entry expired under a long model call — it is still the run's
// budget to reclaim if the host can carry it; if not, the renew call fails
// and the run keeps polling).
func (g *ModelGate) Renew(runID string, memoryMB int) (admitted bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	if entry, ok := g.entries[runID]; ok {
		entry.lastRenew = time.Now()
		return true
	}
	if memoryMB <= 0 {
		memoryMB = 512
	}
	if len(g.entries)+1 > g.max || !g.fitsLocked(memoryMB) {
		return false
	}
	g.entries[runID] = &budgetEntry{memoryMB: memoryMB, lastRenew: time.Now()}
	g.order = append(g.order, runID)
	return true
}

// Release returns a finished run's budget to the ledger.
func (g *ModelGate) Release(runID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.entries[runID]; !ok {
		return
	}
	delete(g.entries, runID)
	for i, id := range g.order {
		if id == runID {
			g.order = append(g.order[:i], g.order[i+1:]...)
			break
		}
	}
}

func (g *ModelGate) fitsLocked(memoryMB int) bool {
	used := 0
	for _, entry := range g.entries {
		used += entry.memoryMB
	}
	if used+memoryMB > g.budgetMB {
		return false
	}
	if g.floorPct > 0 && g.memory != nil {
		if total, available, ok := g.memory(); ok && total > 0 {
			if float64(available)/float64(total)*100 < g.floorPct {
				return false
			}
		}
	}
	return true
}

// expireLocked drops entries that stopped renewing — a run that died
// mid-work releases its budget through this expiry, not through an explicit
// call (a workflow's exit paths are many).
func (g *ModelGate) expireLocked() {
	deadline := time.Now().Add(-g.ttl)
	for runID, entry := range g.entries {
		if entry.lastRenew.Before(deadline) {
			delete(g.entries, runID)
			for i, id := range g.order {
				if id == runID {
					g.order = append(g.order[:i], g.order[i+1:]...)
					break
				}
			}
		}
	}
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
