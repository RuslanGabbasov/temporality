package cost

import (
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

func TestSagaBindingsDeleteMinimal(t *testing.T) {
	base := time.Now()
	events := []observation.Event{
		{Type: "run.started", OccurredAt: base, Context: observation.Context{Run: "r1", Task: "t1"}, Data: map[string]any{}},
		{Type: "saga.registered", OccurredAt: base.Add(time.Minute), Context: observation.Context{Run: "r1", Task: "t1"}, Data: map[string]any{"saga_id": "saga-x"}},
		{Type: "saga.deleted", OccurredAt: base.Add(10 * time.Minute), Context: observation.Context{}, Data: map[string]any{"saga_id": "saga-x"}},
	}
	done := make(chan struct{})
	go func() {
		bindings, sagas := sagaBindings(events)
		t.Logf("returned: bindings=%v sagas=%v", bindings, len(sagas))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sagaBindings hung on a deleted saga")
	}
}
