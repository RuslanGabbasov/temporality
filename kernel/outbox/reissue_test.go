package outbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

func reissueTestEvent(sourceID, eventID, marker string) observation.Event {
	return observation.Event{
		Schema:     observation.Schema,
		EventID:    eventID,
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: sourceID, Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Project: "p", Run: "run-x"},
		Type:       "run.completed",
		Data:       map[string]any{"marker": marker},
	}
}

// Enqueue-time conflict re-issue: a replay that regenerates a recorded emit
// with different content must not LOSE the event — it lands under -r2. The
// lost-emit class turned finished runs into journal zombies (their terminal
// event collided with the pre-replay content and was quarantined).
func TestEnqueueConflictReissuesUnderFreshID(t *testing.T) {
	dsn := os.Getenv("LIVE_DSN")
	if dsn == "" {
		t.Skip("no live dsn")
	}
	journal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"results":[{"status":"delivered"}]}`))
	}))
	defer journal.Close()
	store, err := Open(context.Background(), dsn, journal.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	sourceID := "reissue-test-src"
	defer func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM kernel_event_outbox WHERE source_id=$1`, sourceID)
	}()

	first := reissueTestEvent(sourceID, "scope/event/000001", "old")
	if err := store.Enqueue(ctx, first); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// Same id, different content — the nondeterministic replay case.
	conflicting := reissueTestEvent(sourceID, "scope/event/000001", "new")
	if err := store.Enqueue(ctx, conflicting); err != nil {
		t.Fatalf("conflicting enqueue must re-issue, not fail: %v", err)
	}
	var payload string
	err = store.pool.QueryRow(ctx,
		`SELECT payload FROM kernel_event_outbox WHERE source_id=$1 AND event_id='scope/event/000001-r2'`, sourceID).Scan(&payload)
	if err != nil {
		t.Fatalf("re-issued row missing: %v", err)
	}
	if !strings.Contains(payload, `"new"`) {
		t.Fatalf("re-issued row must carry the new content: %s", payload)
	}
	// The original row keeps its frozen payload — append-only history.
	var original string
	err = store.pool.QueryRow(ctx,
		`SELECT payload FROM kernel_event_outbox WHERE source_id=$1 AND event_id='scope/event/000001'`, sourceID).Scan(&original)
	if err != nil {
		t.Fatalf("original row missing: %v", err)
	}
	if !strings.Contains(original, `"old"`) {
		t.Fatalf("original row must keep the frozen content: %s", original)
	}
}

// Budget exhaustion must never kill the run that produced the event (the
// makety incident, 2026-11-06: a workflow task kept regenerating the same id
// with ever-new content, burned all 99 re-issue slots and the workflow FAILED
// on «budget exhausted» mid-model-call). The fact still lands — under a
// guaranteed-unique nano-suffixed id.
func TestEnqueueConflictBudgetExhaustedStillKeepsTheFact(t *testing.T) {
	dsn := os.Getenv("LIVE_DSN")
	if dsn == "" {
		t.Skip("no live dsn")
	}
	journal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"results":[{"status":"delivered"}]}`))
	}))
	defer journal.Close()
	store, err := Open(context.Background(), dsn, journal.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	sourceID := "reissue-exhaust-src"
	defer func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM kernel_event_outbox WHERE source_id=$1`, sourceID)
	}()

	// Fill the base id and EVERY re-issue slot with different content.
	for i := 1; i <= 100; i++ {
		eventID := "scope/event/000001"
		if i > 1 {
			eventID = fmt.Sprintf("scope/event/000001-r%d", i)
		}
		if err := store.Enqueue(ctx, reissueTestEvent(sourceID, eventID, fmt.Sprintf("filler-%d", i))); err != nil {
			t.Fatalf("filler enqueue %s: %v", eventID, err)
		}
	}

	// One more conflict: the budget is exhausted, the fact must still land.
	if err := store.Enqueue(ctx, reissueTestEvent(sourceID, "scope/event/000001", "survivor")); err != nil {
		t.Fatalf("budget exhaustion must not fail the enqueue: %v", err)
	}
	var payload string
	err = store.pool.QueryRow(ctx,
		`SELECT payload FROM kernel_event_outbox WHERE source_id=$1 AND event_id LIKE 'scope/event/000001-n%'`, sourceID).Scan(&payload)
	if err != nil {
		t.Fatalf("nano-suffixed fallback row missing: %v", err)
	}
	if !strings.Contains(payload, `"survivor"`) {
		t.Fatalf("fallback row must carry the fact: %s", payload)
	}
}

// Delivery-time conflict: the journal already holds this id with different
// content (the pre-replay copy landed long ago). The re-issue must deliver
// under -r2, move the row, and mark it delivered — not bury the fact.
func TestDeliveryConflictReissuesAndDelivers(t *testing.T) {
	dsn := os.Getenv("LIVE_DSN")
	if dsn == "" {
		t.Skip("no live dsn")
	}
	var calls int32
	journal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// The conflicted original: rejected with the id-conflict marker.
			_, _ = w.Write([]byte(`{"results":[{"status":"rejected","error":"source event ID already exists with different content"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"status":"accepted"}]}`))
	}))
	defer journal.Close()
	store, err := Open(context.Background(), dsn, journal.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	sourceID := "reissue-delivery-src"
	defer func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM kernel_event_outbox WHERE source_id=$1`, sourceID)
	}()
	if err := store.Enqueue(ctx, reissueTestEvent(sourceID, "scope/event/000042", "v1")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	published, err := store.PublishOne(ctx)
	if err != nil || !published {
		t.Fatalf("the conflicted event must land via re-issue: published=%v err=%v", published, err)
	}
	var lastError string
	var deliveredAt *time.Time
	err = store.pool.QueryRow(ctx, `SELECT last_error, delivered_at FROM kernel_event_outbox WHERE source_id=$1`, sourceID).Scan(&lastError, &deliveredAt)
	if err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if deliveredAt == nil {
		t.Fatalf("the re-issued row must be marked delivered, last_error=%q", lastError)
	}
	if !strings.Contains(lastError, "reissued") && lastError != "" {
		t.Fatalf("unexpected last_error: %q", lastError)
	}
}
