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

func TestModelGateBudgetAccounting(t *testing.T) {
	g := newTestGate(10, gateTestBudget)
	// A heavy 3g agent + a light 512m chat fit in 4g together.
	if err := g.Acquire("heavy", 3072); err != nil {
		t.Fatal(err)
	}
	if err := g.Acquire("light", 512); err != nil {
		t.Fatal(err)
	}
	// 3584 used: another 512 fits, another 1g does not.
	if err := g.Acquire("next", 512); err != nil {
		t.Fatal(err)
	}
	if err := g.Acquire("big", 1024); !errIsBusy(err) {
		t.Fatalf("over-budget run must be refused, got %v", err)
	}
	// Releasing the heavy run frees room for the 1g one.
	g.Release("heavy")
	if err := g.Acquire("big", 1024); err != nil {
		t.Fatalf("freed budget must admit: %v", err)
	}
}

func errIsBusy(err error) bool {
	return err != nil && err.Error() == ErrModelGateBusy.Error()
}

func TestModelGateMaxCeiling(t *testing.T) {
	g := newTestGate(2, gateTestBudget)
	// Tiny sandboxes, but the absolute ceiling still bounds them: 20 x 512m
	// runs must never host-swap just because the budget looked fine.
	for i := 0; i < 2; i++ {
		if err := g.Acquire(string(rune('a'+i)), 256); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Acquire("c", 256); !errIsBusy(err) {
		t.Fatal("the absolute concurrent-runs ceiling must hold")
	}
}

func TestModelGateMemoryFloor(t *testing.T) {
	ConfigureModelGate(8, gateTestBudget, 10, func() (total, available uint64, ok bool) {
		return 100, 5, true // 5% available — under the 10% floor
	}, 1024)
	g := gate()
	if err := g.Acquire("x", 512); !errIsBusy(err) {
		t.Fatalf("memory floor must pause admissions, got %v", err)
	}
	wait, _ := g.Verdict("y", 512)
	if wait <= 0 {
		t.Fatal("memory floor must make the verdict wait")
	}
	// A probe failure never wedges the gate: missing data admits.
	ConfigureModelGate(8, gateTestBudget, 10, func() (total, available uint64, ok bool) { return 0, 0, false }, 1024)
	g = gate()
	if err := g.Acquire("x", 512); err != nil {
		t.Fatalf("unreadable memory must not pause the gate: %v", err)
	}
	ConfigureModelGate(1<<30, gateTestBudget, 0, nil, 1024)
}

func TestGateVerdictDefaultsPermissive(t *testing.T) {
	// A binary that never configured the gate must not wedge runs.
	ConfigureModelGate(1<<30, gateTestBudget, 0, nil, 1024)
	wait, _ := gate().Verdict("z", 512)
	if wait != 0 {
		t.Fatal("unconfigured gate must admit immediately")
	}
}

func TestModelGateRenewKeepsAndReleaseFrees(t *testing.T) {
	g := newTestGate(10, 1024)
	if err := g.Acquire("solo", 1024); err != nil {
		t.Fatal(err)
	}
	// The budget is fully used: a renew (the run is alive) keeps it.
	if !g.Renew("solo", 1024) {
		t.Fatal("a live run must keep its admission")
	}
	if err := g.Acquire("other", 512); !errIsBusy(err) {
		t.Fatal("the full budget must refuse another run")
	}
	// Release frees the budget for the next run.
	g.Release("solo")
	if err := g.Acquire("other", 512); err != nil {
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
