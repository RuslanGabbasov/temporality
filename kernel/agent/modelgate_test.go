package agent

import (
	"testing"
)

// The gate is the OOM guard: every run declares its sandbox memory (the same
// --memory its container gets), the gate admits when the SUM of admitted
// requirements fits the host budget, and a busy gate is a typed "not yet" —
// the workflow polls again instead of failing.

const gateTestBudget = 4096

func newTestGate(max int, budgetMB int) *ModelGate {
	ConfigureModelGate(max, budgetMB, 0, nil, 1024)
	return gate()
}

func errIsBusy(err error) bool {
	return err != nil && err.Error() == ErrModelGateBusy.Error()
}

func TestModelGateBudgetAccounting(t *testing.T) {
	g := newTestGate(10, gateTestBudget)
	// A heavy 3g agent + a light 512m chat fit in 4g together.
	if err := g.Acquire("heavy", 3072, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := g.Acquire("light", 512, 0, 0); err != nil {
		t.Fatal(err)
	}
	// 3584 used: another 512 fits, another 1g does not.
	if err := g.Acquire("next", 512, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := g.Acquire("big", 1024, 0, 0); !errIsBusy(err) {
		t.Fatalf("over-budget run must be refused, got %v", err)
	}
	// Releasing the heavy run frees room for the 1g one.
	g.Release("heavy")
	if err := g.Acquire("big", 1024, 0, 0); err != nil {
		t.Fatalf("freed budget must admit: %v", err)
	}
}

func TestModelGateUnitQuota(t *testing.T) {
	g := newTestGate(10, gateTestBudget)
	// Unit quota: 2 concurrent runs, 1g of sandbox memory total.
	if err := g.Acquire("u1", 512, 1024, 2); err != nil {
		t.Fatal(err)
	}
	if err := g.Acquire("u2", 512, 1024, 2); err != nil {
		t.Fatal(err)
	}
	// The unit's runs and budget are full: a third run of the same scope is
	// refused even though the HOST budget has room.
	if err := g.Acquire("u3", 256, 1024, 2); !errIsBusy(err) {
		t.Fatalf("the unit quota must hold, got %v", err)
	}
	// A different scope (no quota) is unaffected by the unit's full bucket.
	if err := g.Acquire("free", 1024, 0, 0); err != nil {
		t.Fatalf("another scope must not inherit the unit quota: %v", err)
	}
	// A bigger unit budget would admit: the scope is keyed by the caps.
	if err := g.Acquire("wider", 256, 2048, 2); err != nil {
		t.Fatalf("a different quota scope must have its own bucket: %v", err)
	}
	// Releasing one unit run frees the scope.
	g.Release("u1")
	if err := g.Acquire("u3", 256, 1024, 2); err != nil {
		t.Fatalf("freed unit scope must admit: %v", err)
	}
}

func TestModelGateMaxCeiling(t *testing.T) {
	g := newTestGate(2, gateTestBudget)
	// Tiny sandboxes, but the absolute ceiling still bounds them: 20 x 512m
	// runs must never host-swap just because the budget looked fine.
	for i := 0; i < 2; i++ {
		if err := g.Acquire(string(rune('a'+i)), 256, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Acquire("c", 256, 0, 0); !errIsBusy(err) {
		t.Fatal("the absolute concurrent-runs ceiling must hold")
	}
}

func TestModelGateMemoryFloor(t *testing.T) {
	ConfigureModelGate(8, gateTestBudget, 10, func() (total, available uint64, ok bool) {
		return 100, 5, true // 5% available — under the 10% floor
	}, 1024)
	g := gate()
	if err := g.Acquire("x", 512, 0, 0); !errIsBusy(err) {
		t.Fatalf("memory floor must pause admissions, got %v", err)
	}
	verdict := g.Verdict("y", 512, 0, 0)
	if verdict.WaitSeconds <= 0 {
		t.Fatal("memory floor must make the verdict wait")
	}
	if verdict.Reason != GateBlockedMemoryFloor {
		t.Fatalf("want memory_floor, got %q", verdict.Reason)
	}
	// A probe failure never wedges the gate: missing data admits.
	ConfigureModelGate(8, gateTestBudget, 10, func() (total, available uint64, ok bool) { return 0, 0, false }, 1024)
	g = gate()
	if err := g.Acquire("x", 512, 0, 0); err != nil {
		t.Fatalf("unreadable memory must not pause the gate: %v", err)
	}
	ConfigureModelGate(1<<30, gateTestBudget, 0, nil, 1024)
}

func TestGateVerdictDefaultsPermissive(t *testing.T) {
	// A binary that never configured the gate must not wedge runs.
	ConfigureModelGate(1<<30, gateTestBudget, 0, nil, 1024)
	verdict := gate().Verdict("z", 512, 0, 0)
	if verdict.WaitSeconds != 0 {
		t.Fatal("unconfigured gate must admit immediately")
	}
}

// The blocked reason is the difference between «the quota leaked» and «the
// memory dimension is full»: every queue report names the dimension that is
// actually binding (the count ceiling is rarely it — sandbox memory is).
func TestModelGateBlockedReason(t *testing.T) {
	g := newTestGate(10, 1024)
	if err := g.Acquire("a", 1024, 0, 0); err != nil {
		t.Fatal(err)
	}
	verdict := g.Verdict("b", 512, 0, 0)
	if verdict.WaitSeconds == 0 {
		t.Fatal("a full budget must queue the newcomer")
	}
	if verdict.Reason != GateBlockedMemoryBudget {
		t.Fatalf("want memory_budget, got %q", verdict.Reason)
	}
	if verdict.UsedMB != 1024 || verdict.BudgetMB != 1024 || verdict.Admitted != 1 {
		t.Fatalf("the verdict must carry the ledger numbers: %+v", verdict)
	}

	// The absolute ceiling reports as its own dimension.
	maxed := newTestGate(1, 1<<20)
	if err := maxed.Acquire("a", 1024, 0, 0); err != nil {
		t.Fatal(err)
	}
	verdict = maxed.Verdict("b", 1024, 0, 0)
	if verdict.Reason != GateBlockedCountCeiling {
		t.Fatalf("want count_ceiling, got %q", verdict.Reason)
	}

	// The unit quota names its own dimension.
	quota := newTestGate(10, 1<<20)
	if err := quota.Acquire("a", 1024, 2048, 1); err != nil {
		t.Fatal(err)
	}
	verdict = quota.Verdict("b", 1024, 2048, 1)
	if verdict.Reason != GateBlockedQuotaRuns {
		t.Fatalf("want quota_runs, got %q", verdict.Reason)
	}
}

// A re-acquire of a run already on the ledger must not ghost the queue
// order: duplicates in order inflated the reported positions forever
// (Release and expiry remove one occurrence each).
func TestModelGateReAcquireDoesNotDuplicateOrder(t *testing.T) {
	g := newTestGate(10, 4096)
	if err := g.Acquire("a", 512, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := g.Acquire("a", 512, 0, 0); err != nil {
		t.Fatal(err)
	}
	if len(g.order) != 1 || len(g.entries) != 1 {
		t.Fatalf("re-acquire must keep one ledger row: order=%v entries=%d", g.order, len(g.entries))
	}
	g.Release("a")
	if len(g.order) != 0 {
		t.Fatalf("release must clear the queue order, got %v", g.order)
	}
}

func TestModelGateRenewKeepsAndReleaseFrees(t *testing.T) {
	g := newTestGate(10, 1024)
	if err := g.Acquire("solo", 1024, 0, 0); err != nil {
		t.Fatal(err)
	}
	// The budget is fully used: a renew (the run is alive) keeps it.
	if !g.Renew("solo", 1024, 0, 0) {
		t.Fatal("a live run must keep its admission")
	}
	if err := g.Acquire("other", 512, 0, 0); !errIsBusy(err) {
		t.Fatal("the full budget must refuse another run")
	}
	// Release frees the budget for the next run.
	g.Release("solo")
	if err := g.Acquire("other", 512, 0, 0); err != nil {
		t.Fatalf("released budget must admit: %v", err)
	}
	// Releasing an unknown id is a no-op, not an error.
	g.Release("never-admitted")
}

func TestParseMemoryMB(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"1g", 1024}, {"512m", 512}, {"2gb", 2048}, {"750mb", 750},
		{"256k", 0}, {"", 0}, {" 1G ", 1024}, {"abc", 0}, {"1x", 0},
	} {
		if got := ParseMemoryMB(c.in); got != c.want {
			t.Fatalf("ParseMemoryMB(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSandboxMemorySpec(t *testing.T) {
	if got := sandboxMemorySpec(RunInput{Sandbox: &SandboxSpec{Memory: "2g"}}); got != "2g" {
		t.Fatalf("spec memory must pass through, got %q", got)
	}
	if got := sandboxMemorySpec(RunInput{}); got != "" {
		t.Fatalf("no sandbox must yield empty spec, got %q", got)
	}
}
