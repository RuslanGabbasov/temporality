package agent

import (
	"errors"
	"fmt"
	"sort"
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
// On top of the host budget, a run may carry a UNIT quota from its policy
// (docs/org-structure.md §24): max_concurrent_runs + memory_budget_mb — the
// unit's own ceiling, merged restrictively down the org tree and frozen with
// the policy snapshot. Runs sharing one quota scope sum together; a scope is
// keyed by its caps (every run resolved under the same effective policy
// values shares the same bucket).
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
	floorPct float64 // pause admitting below this % of MemAvailable; 0 = no check
	memory   func() (uint64, uint64, bool)
	entries  map[string]*budgetEntry
	order    []string // FIFO of queued run ids, for the reported position
	ttl      time.Duration
}

type budgetEntry struct {
	memoryMB  int
	quotaKey  string
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

// quotaScopeKey scopes the unit-quota accounting: runs resolved under the
// same effective caps share one bucket.
func quotaScopeKey(quotaMB, quotaRuns int) string {
	if quotaMB <= 0 && quotaRuns <= 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", quotaMB, quotaRuns)
}

// sandboxMemorySpec returns the run's declared sandbox memory spec (the
// same --memory its container gets); empty when the profile says nothing.
func sandboxMemorySpec(input RunInput) string {
	if input.Sandbox != nil {
		return strings.TrimSpace(input.Sandbox.Memory)
	}
	return ""
}

// GateVerdict is the full "is it my turn" answer: the wait, the human
// queue position and — while blocked — WHY (the dimension that is full) plus
// the numbers behind it. Without the reason a queued run looks like phantom
// queueing («квота утекла») when the ledger is honest and the memory
// dimension is simply full.
type GateVerdict struct {
	WaitSeconds int
	Position    int
	Reason      string // "" when admitted; see the blocked* constants
	UsedMB      int    // admitted sandbox requirements on the host ledger
	BudgetMB    int    // the host budget those requirements must fit
	Admitted    int    // ledger entries (concurrent runs holding a slot)
	MaxRuns     int    // the absolute concurrent-runs ceiling
}

// Blocked-reason vocabulary for GateVerdict.Reason (stable strings — the
// trace and the channel notices quote them).
const (
	GateBlockedMemoryBudget = "memory_budget" // the host's runnable budget is reserved
	GateBlockedMemoryFloor  = "memory_floor"  // MemAvailable below the admission floor
	GateBlockedCountCeiling = "count_ceiling" // the absolute concurrent-runs guard
	GateBlockedQuotaMemory  = "quota_memory"  // the unit's memory_budget_mb
	GateBlockedQuotaRuns    = "quota_runs"    // the unit's max_concurrent_runs
)

// Verdict answers "is it my turn": a zero WaitSeconds admits a run with the
// given sandbox memory under the given unit quota; otherwise the verdict
// carries the next poll delay, the queue position and the blocking reason.
func (g *ModelGate) Verdict(runID string, memoryMB, quotaMB, quotaRuns int) GateVerdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	if memoryMB <= 0 {
		memoryMB = gateDefaultRunMemoryMB
	}
	verdict := GateVerdict{
		BudgetMB: g.budgetMB,
		Admitted: len(g.entries),
		MaxRuns:  g.max,
	}
	for _, entry := range g.entries {
		verdict.UsedMB += entry.memoryMB
	}
	if reason := g.blockedReasonLocked(runID, memoryMB, quotaScopeKey(quotaMB, quotaRuns), quotaMB, quotaRuns); reason != "" {
		verdict.Reason = reason
		verdict.WaitSeconds = 10
		verdict.Position = len(g.order) + 1
		return verdict
	}
	return verdict
}

// Acquire claims the budget for a run. A lost race with another run comes
// back as ModelGateBusy — the workflow polls again.
func (g *ModelGate) Acquire(runID string, memoryMB, quotaMB, quotaRuns int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	if memoryMB <= 0 {
		memoryMB = gateDefaultRunMemoryMB
	}
	key := quotaScopeKey(quotaMB, quotaRuns)
	if reason := g.blockedReasonLocked(runID, memoryMB, key, quotaMB, quotaRuns); reason != "" {
		return ErrModelGateBusy
	}
	if _, exists := g.entries[runID]; !exists {
		g.order = append(g.order, runID) // re-acquire must not ghost the queue order
	}
	g.entries[runID] = &budgetEntry{memoryMB: memoryMB, quotaKey: key, lastRenew: time.Now()}
	return nil
}

// Renew marks an admitted run alive, extending its ledger entry past the
// TTL. Unknown ids are admitted on the spot (the renewer might be a run
// whose entry expired under a long model call — it is still the run's
// budget to reclaim if the host can carry it; if not, the renew call fails
// and the run keeps polling).
func (g *ModelGate) Renew(runID string, memoryMB, quotaMB, quotaRuns int) (admitted bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	if entry, ok := g.entries[runID]; ok {
		entry.lastRenew = time.Now()
		return true
	}
	if memoryMB <= 0 {
		memoryMB = gateDefaultRunMemoryMB
	}
	key := quotaScopeKey(quotaMB, quotaRuns)
	if reason := g.blockedReasonLocked(runID, memoryMB, key, quotaMB, quotaRuns); reason != "" {
		return false
	}
	if _, exists := g.entries[runID]; !exists {
		g.order = append(g.order, runID)
	}
	g.entries[runID] = &budgetEntry{memoryMB: memoryMB, quotaKey: key, lastRenew: time.Now()}
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

// blockedReasonLocked answers WHY a run cannot enter the ledger right now —
// the empty string when it fits. Every dimension reports separately: the
// host budget (sum of admitted requirements), the unit quota (the runs
// sharing the quota scope), the absolute ceiling and the MemAvailable floor.
// A run's own entry is already on the books (renew of an admitted run): it
// does not compete with itself.
func (g *ModelGate) blockedReasonLocked(runID string, memoryMB int, quotaKey string, quotaMB, quotaRuns int) string {
	used, quotaUsed, quotaCount := 0, 0, 0
	for id, entry := range g.entries {
		if id == runID {
			continue
		}
		used += entry.memoryMB
		if quotaKey != "" && entry.quotaKey == quotaKey {
			quotaUsed += entry.memoryMB
			quotaCount++
		}
	}
	if _, exists := g.entries[runID]; !exists && len(g.entries)+1 > g.max {
		return GateBlockedCountCeiling
	}
	if used+memoryMB > g.budgetMB {
		return GateBlockedMemoryBudget
	}
	if quotaKey != "" {
		if quotaMB > 0 && quotaUsed+memoryMB > quotaMB {
			return GateBlockedQuotaMemory
		}
		if quotaRuns > 0 && quotaCount+1 > quotaRuns {
			return GateBlockedQuotaRuns
		}
	}
	if g.floorPct > 0 && g.memory != nil {
		if total, available, ok := g.memory(); ok && total > 0 {
			if float64(available)/float64(total)*100 < g.floorPct {
				return GateBlockedMemoryFloor
			}
		}
	}
	return ""
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

// Stats reports the gate's live state for operators: the host budget, how
// much of it the admitted runs declare, who holds it (the run ids and their
// declared sandbox memory) and the FIFO queue waiting for a slot. The
// quota panel shows it next to the unit quotas — a run queued on the HOST
// budget while the unit quota reads ∞MB otherwise looks like phantom
// queueing.
func (g *ModelGate) Stats() (usedMB, budgetMB int, admitted int, maxRuns int, holders []string, queued []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	for id, entry := range g.entries {
		usedMB += entry.memoryMB
		holders = append(holders, fmt.Sprintf("%s:%dMB", id, entry.memoryMB))
	}
	sort.Strings(holders)
	return usedMB, g.budgetMB, len(g.entries), g.max, holders, append([]string(nil), g.order...)
}

// GateStats exposes the memory budget gate's live state to the kernel's
// HTTP surface (GET /v1/agent/gate). See ModelGate.Stats.
func GateStats() (usedMB, budgetMB int, admitted int, maxRuns int, holders []string, queued []string) {
	return gate().Stats()
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
