package agent

// Test-only wiring for the record-event-local hook (RecordEventLocal /
// SetRecordEventLocal). Production wiring happens in the worker bootstrap:
// cmd/agent-kernel/main.go must call
// agent.SetRecordEventLocal(activities.RecordEvent) next to the
// RegisterActivityWithOptions(activities.RecordEvent, ...) call, with the
// same Activities instance, before the worker starts serving.

import (
	"context"
	"testing"

	"github.com/temporality-project/temporality/observation"
)

// setRecordEventLocalHook installs a test RecordEventLocal implementation for
// the duration of the test and restores the previous value via t.Cleanup.
// The hook is package state, so it is deliberately NOT set from an init
// function: a package-level init would reroute every other test in the
// package away from its registered kernel.record_event activity mock, whose
// recorded events those tests assert on. Tests opt in per test instead.
func setRecordEventLocalHook(t *testing.T, hook func(ctx context.Context, item observation.Event) error) {
	t.Helper()
	previous := RecordEventLocal
	t.Cleanup(func() { RecordEventLocal = previous })
	RecordEventLocal = hook
}
