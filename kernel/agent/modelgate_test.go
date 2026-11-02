package agent

import (
	"errors"
	"testing"
)

// The gate is the OOM guard: capacity is capped, memory floor pauses new
// admissions, and a busy gate is a typed "not yet" — the workflow polls
// again instead of failing.

func TestModelGateCapacityAndRelease(t *testing.T) {
	ConfigureModelGate(2, 0, nil)
	g := gate()
	if _, pos := g.Verdict(); pos != 0 {
		t.Fatalf("empty gate must admit at position 0, got %d", pos)
	}
	if err := g.Enter(); err != nil {
		t.Fatal(err)
	}
	if err := g.Enter(); err != nil {
		t.Fatal(err)
	}
	if err := g.Enter(); !errors.Is(err, ErrModelGateBusy) {
		t.Fatalf("full gate must return ModelGateBusy, got %v", err)
	}
	wait, pos := g.Verdict()
	if wait <= 0 || pos != 1 {
		// Both slots busy: this caller is next in line (position 1).
		t.Fatalf("queued position must be 1 (next up), got wait=%d pos=%d", wait, pos)
	}
	g.Leave()
	if _, pos := g.Verdict(); pos != 0 {
		t.Fatalf("released slot must admit again, got pos %d", pos)
	}
	if err := g.Enter(); err != nil {
		t.Fatal(err)
	}
	// A double leave must not push the counter negative (a pinned ceiling,
	// never an over-admission).
	g.Leave()
	g.Leave()
	if err := g.Enter(); err != nil || g.active > g.max {
		t.Fatalf("double release must not over-admit: err=%v active=%d", err, g.active)
	}
	ConfigureModelGate(1 << 30, 0, nil) // leave the shared gate permissive
}

func TestModelGateMemoryFloor(t *testing.T) {
	ConfigureModelGate(8, 10, func() (total, available uint64, ok bool) {
		return 100, 5, true // 5% available — under the 10% floor
	})
	g := gate()
	if err := g.Enter(); !errors.Is(err, ErrModelGateBusy) {
		t.Fatalf("memory floor must pause admissions, got %v", err)
	}
	wait, _ := g.Verdict()
	if wait <= 0 {
		t.Fatal("memory floor must make the verdict wait")
	}
	// A probe failure never wedges the gate: missing data admits.
	ConfigureModelGate(8, 10, func() (total, available uint64, ok bool) { return 0, 0, false })
	g = gate()
	if err := g.Enter(); err != nil {
		t.Fatalf("unreadable memory must not pause the gate: %v", err)
	}
	ConfigureModelGate(1<<30, 0, nil)
}

func TestGateVerdictDefaultsPermissive(t *testing.T) {
	// A binary that never configured the gate must not wedge runs.
	ConfigureModelGate(1<<30, 0, nil)
	wait, _ := gate().Verdict()
	if wait != 0 {
		t.Fatal("unconfigured gate must admit immediately")
	}
}
