package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/observation/memory"
)

// failingBatchStore implements the batch capability and fails it, so the
// hints handler takes the batch path and must swallow the error.
type failingBatchStore struct {
	observation.Store
}

func (failingBatchStore) AppendObservationBatch(context.Context, []observation.Event) error {
	return errors.New("disk on fire")
}

// noBatchStore lacks the batch capability: the hints handler must fall back
// to per-event appends without losing the answer.
type noBatchStore struct {
	observation.Store
}

func seedHintKnowledge(t *testing.T, s http.Handler) {
	t.Helper()
	if res, body := doJSON(t, s, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{proposedEvent("lighthouse", "evt-k1", "K1", "gatekeeper v2 authenticates via token file")},
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}
}

func askHints(t *testing.T, s http.Handler) (activationID string, hintIDs, knowledgeIDs []string) {
	t.Helper()
	res, body := doJSON(t, s, "POST", "/v1/observations/hints", map[string]any{
		"project": "lighthouse", "query": "how do I authenticate to gatekeeper v2",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hints status = %d, body = %v", res.StatusCode, body)
	}
	if id, ok := body["activation_id"].(string); !ok || id == "" {
		t.Fatalf("activation_id missing: %v", body)
	} else {
		activationID = id
	}
	rawHints, ok := body["hints"].([]any)
	if !ok || len(rawHints) == 0 {
		t.Fatalf("hints must be non-empty for the seed knowledge: %v", body)
	}
	for _, raw := range rawHints {
		hint := raw.(map[string]any)
		hintID, _ := hint["hint_id"].(string)
		offerEventID, _ := hint["offer_event_id"].(string)
		if hintID == "" || hintID != offerEventID {
			t.Fatalf("hint ids not framed: hint_id=%v offer_event_id=%v", hintID, offerEventID)
		}
		id, _ := hint["knowledge_id"].(string)
		hintIDs = append(hintIDs, hintID)
		knowledgeIDs = append(knowledgeIDs, id)
	}
	return activationID, hintIDs, knowledgeIDs
}

func storedTelemetry(t *testing.T, store *memory.Store) (queries, offers []observation.Event) {
	t.Helper()
	events, err := store.ListObservations(context.Background(), observation.Filter{
		Project: "lighthouse", Types: []string{"hint.query", "hint.offered"},
	})
	if err != nil {
		t.Fatalf("list telemetry: %v", err)
	}
	for _, event := range events {
		switch event.Type {
		case "hint.query":
			queries = append(queries, event)
		case "hint.offered":
			offers = append(offers, event)
		}
	}
	return queries, offers
}

// The activation writes one frame: hint.query first, then one hint.offered
// per offer, all sharing the activation id — the same framing the pgx batch
// produces in production (the memory store backs the same capability).
func TestHintTelemetryBatchFraming(t *testing.T) {
	store := memory.New()
	s := NewServer(store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	seedHintKnowledge(t, s)

	activationID, hintIDs, knowledgeIDs := askHints(t, s)
	queries, offers := storedTelemetry(t, store)

	if len(queries) != 1 {
		t.Fatalf("hint.query events = %d, want 1", len(queries))
	}
	query := queries[0]
	if query.EventID != activationID || query.Data["activation_id"] != activationID {
		t.Fatalf("activation event %s/%v does not match response activation %s", query.EventID, query.Data["activation_id"], activationID)
	}
	if len(offers) != len(hintIDs) {
		t.Fatalf("hint.offered events = %d, want %d (one per offer)", len(offers), len(hintIDs))
	}
	offeredHints := map[string]string{} // hint_id -> knowledge_id
	for _, offer := range offers {
		if offer.Data["activation_id"] != activationID {
			t.Fatalf("offer %s carries foreign activation %v", offer.EventID, offer.Data["activation_id"])
		}
		hintID, _ := offer.Data["hint_id"].(string)
		if hintID == "" || offer.EventID != hintID {
			t.Fatalf("offer event %s must carry its id as data.hint_id (%v)", offer.EventID, offer.Data["hint_id"])
		}
		offeredHints[hintID], _ = offer.Data["knowledge_id"].(string)
	}
	for i, hintID := range hintIDs {
		if offeredHints[hintID] != knowledgeIDs[i] {
			t.Fatalf("offer for hint %s = %v, want knowledge %s", hintID, offeredHints[hintID], knowledgeIDs[i])
		}
	}
}

// A telemetry write failure must not turn an answered hints call into a 500:
// the offer events are reuse statistics, the agent still needs the hints.
func TestHintTelemetryFailureStillAnswersHints(t *testing.T) {
	store := &failingBatchStore{Store: memory.New()}
	s := NewServer(store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	seedHintKnowledge(t, s)

	if _, hintIDs, _ := askHints(t, s); len(hintIDs) == 0 {
		t.Fatal("hints must be answered despite the telemetry failure")
	}
}

// Stores without the batch capability take the per-event fallback path and
// must land the same telemetry frame.
func TestHintTelemetryFallbackWithoutBatchCapability(t *testing.T) {
	store := &noBatchStore{Store: memory.New()}
	s := NewServer(store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	seedHintKnowledge(t, s)

	activationID, hintIDs, knowledgeIDs := askHints(t, s)
	inner := store.Store.(*memory.Store)
	queries, offers := storedTelemetry(t, inner)
	if len(queries) != 1 || len(offers) != len(hintIDs) {
		t.Fatalf("fallback telemetry = %d queries / %d offers, want 1 / %d", len(queries), len(offers), len(hintIDs))
	}
	if queries[0].Data["activation_id"] != activationID {
		t.Fatalf("fallback activation = %v, want %s", queries[0].Data["activation_id"], activationID)
	}
	for i, offer := range offers {
		if offer.Data["knowledge_id"] != knowledgeIDs[i] {
			t.Fatalf("fallback offer %d = %v, want knowledge %s", i, offer.Data["knowledge_id"], knowledgeIDs[i])
		}
	}
}

// Hint telemetry never leaves the cache cold for the wrong reason: an offered
// hint invalidates via the ingest path (the projection consumes hint.offered),
// verified implicitly by the invalidation matrix in knowledge_cache_test.go.
