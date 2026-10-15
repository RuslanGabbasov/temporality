package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// The knowledge list is bitemporal: as_of cuts the event stream by occurred_at
// (what had happened yet) and known_at by received_at (what the journal had
// ingested yet). Both must change the projection — a historical slice that
// still returns everything is the classic "filters do nothing" regression.
func TestKnowledgeListBitemporalFilters(t *testing.T) {
	handler := newTestServer(t)

	early := proposedEvent("lighthouse", "evt-early", "K-early", "migrations require docker")
	early.OccurredAt = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	late := proposedEvent("lighthouse", "evt-late", "K-late", "api client must be regenerated")
	late.OccurredAt = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{early, late},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	knowledgeIDs := func(body map[string]any) []string {
		t.Helper()
		items, _ := body["knowledge"].([]any)
		ids := make([]string, 0, len(items))
		for _, item := range items {
			if id, ok := item.(map[string]any)["id"].(string); ok {
				ids = append(ids, id)
			}
		}
		return ids
	}

	// Unfiltered: both items.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unfiltered status = %d", res.StatusCode)
	}
	if ids := knowledgeIDs(body); len(ids) != 2 {
		t.Fatalf("unfiltered knowledge = %v, want both items", ids)
	}

	// as_of between the two proposals: only the early one existed yet.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&as_of=2026-09-25T12:00:00Z", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("as_of between status = %d", res.StatusCode)
	}
	if ids := knowledgeIDs(body); len(ids) != 1 || ids[0] != "K-early" {
		t.Fatalf("as_of between knowledge = %v, want [K-early]", ids)
	}

	// as_of before everything: empty projection, not "everything".
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&as_of=2026-09-01T00:00:00Z", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("as_of before status = %d", res.StatusCode)
	}
	if ids := knowledgeIDs(body); len(ids) != 0 {
		t.Fatalf("as_of before knowledge = %v, want none", ids)
	}

	// known_at before ingest (memory store stamps wall clock at append):
	// nothing was known yet.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&known_at=2026-09-01T00:00:00Z", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("known_at before status = %d", res.StatusCode)
	}
	if ids := knowledgeIDs(body); len(ids) != 0 {
		t.Fatalf("known_at before knowledge = %v, want none", ids)
	}

	// known_at far future: everything was known.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&known_at=2030-01-01T00:00:00Z", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("known_at after status = %d", res.StatusCode)
	}
	if ids := knowledgeIDs(body); len(ids) != 2 {
		t.Fatalf("known_at after knowledge = %v, want both items", ids)
	}

	// Malformed timestamps must be a 400, not a silent no-op.
	res, _ = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&as_of=not-a-date", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed as_of status = %d, want 400", res.StatusCode)
	}
}
