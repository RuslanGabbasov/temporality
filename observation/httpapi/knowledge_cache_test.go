package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/observation/memory"
)

func newCacheTestServer(t *testing.T) (*Server, *memory.Store) {
	t.Helper()
	store := memory.New()
	server := NewServer(store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return server, store
}

// plantSentinel swaps the project's cache entry for a marker so a test can see
// whether an ingest dropped the entry without re-reading the projection.
func plantSentinel(s *Server, projects ...string) {
	s.knowledgeMu.Lock()
	defer s.knowledgeMu.Unlock()
	s.knowledgeCache = map[string]knowledgeCacheEntry{}
	for _, project := range projects {
		s.knowledgeCache[project] = knowledgeCacheEntry{items: []observation.Knowledge{{ID: "cache-sentinel"}}}
	}
}

func sentinelPresent(s *Server, project string) bool {
	s.knowledgeMu.Lock()
	defer s.knowledgeMu.Unlock()
	entry, ok := s.knowledgeCache[project]
	return ok && len(entry.items) == 1 && entry.items[0].ID == "cache-sentinel"
}

func cacheSize(s *Server) int {
	s.knowledgeMu.Lock()
	defer s.knowledgeMu.Unlock()
	return len(s.knowledgeCache)
}

// The projection consumes only knowledge lifecycle events and the four hint
// telemetry types (observation/knowledge.go applyHint/applyTransition) — every
// other ingest batch must leave the cache warm, or an active run (dozens of
// tool/run batches per turn) keeps it permanently cold. knowledge.extraction.*
// markers are explicitly skipped by the projection and must not invalidate.
func TestIngestInvalidatesKnowledgeCacheOnlyOnRelevantTypes(t *testing.T) {
	s, _ := newCacheTestServer(t)
	if res, body := doJSON(t, s, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{proposedEvent("lighthouse", "evt-k1", "K1", "gatekeeper v2 authenticates via token file")},
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}
	// Warm the cache so the entry exists before the matrix below.
	if res, body := doJSON(t, s, "GET", "/v1/observations/knowledge?project=lighthouse", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, body = %v", res.StatusCode, body)
	}

	ingest := func(eventType string, data map[string]any) *http.Response {
		t.Helper()
		if data == nil {
			data = map[string]any{}
		}
		event := observation.Event{
			Schema: observation.Schema, EventID: "evt-" + eventType, OccurredAt: time.Now().UTC(),
			Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
			Context: observation.Context{Project: "lighthouse", Run: "run-1"},
			Type:    eventType, Data: data,
		}
		res, body := doJSON(t, s, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{event}})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s ingest status = %d, body = %v", eventType, res.StatusCode, body)
		}
		return res
	}

	irrelevant := []struct {
		eventType string
		data      map[string]any
	}{
		{"run.started", map[string]any{"status": "running"}},
		{"tool.called", map[string]any{"tool": "web_search"}},
		{"knowledge.extraction.started", nil},
		{"knowledge.extraction.completed", map[string]any{"items": 3}},
		{"hint.query", map[string]any{"activation_id": "act-1"}},
	}
	for _, tc := range irrelevant {
		plantSentinel(s, "lighthouse")
		ingest(tc.eventType, tc.data)
		if !sentinelPresent(s, "lighthouse") {
			t.Fatalf("%s must not invalidate the knowledge cache", tc.eventType)
		}
	}

	relevant := []struct {
		eventType string
		data      map[string]any
	}{
		{"knowledge.proposed", map[string]any{"knowledge_id": "K2", "proposition": "fresh rule"}},
		{"knowledge.used", map[string]any{"knowledge_id": "K1"}},
		{"knowledge.confirmed", map[string]any{"knowledge_id": "K1"}},
		{"knowledge.invalidated", map[string]any{"knowledge_id": "K1", "reason": "wrong"}},
		{"knowledge.linked", map[string]any{"knowledge_id": "K1", "target_id": "K2", "relation": "related_to"}},
		{"hint.offered", map[string]any{"hint_id": "h1", "knowledge_id": "K1"}},
		{"hint.used", map[string]any{"hint_id": "h1"}},
		{"hint.ignored", map[string]any{"hint_id": "h1"}},
		{"hint.outcome", map[string]any{"hint_id": "h1", "outcome": "helpful"}},
	}
	for _, tc := range relevant {
		plantSentinel(s, "lighthouse")
		ingest(tc.eventType, tc.data)
		if sentinelPresent(s, "lighthouse") {
			t.Fatalf("%s must invalidate the project's knowledge cache: the projection consumes it", tc.eventType)
		}
	}

	// Rejected (conflicting) events do not invalidate: nothing changed.
	plantSentinel(s, "lighthouse")
	conflict := proposedEvent("lighthouse", "evt-k1", "K1", "rewritten proposition")
	if res, body := doJSON(t, s, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{conflict}}); res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("conflicting re-ingest status = %d, want 207, body = %v", res.StatusCode, body)
	}
	if !sentinelPresent(s, "lighthouse") {
		t.Fatal("a rejected event must not invalidate the knowledge cache")
	}

	// Shared-scope knowledge changes what EVERY project sees: one promotion
	// must drop the whole map, not just the origin project.
	plantSentinel(s, "lighthouse", "forge")
	promoted := observation.Event{
		Schema: observation.Schema, EventID: "evt-promote", OccurredAt: time.Now().UTC(),
		Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse"},
		Type:    "knowledge.promoted",
		Data:    map[string]any{"knowledge_id": "K1", "scope_kind": "organization", "reason": "shared platform rule"},
	}
	if res, body := doJSON(t, s, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{promoted}}); res.StatusCode != http.StatusOK {
		t.Fatalf("promote ingest status = %d, body = %v", res.StatusCode, body)
	}
	if got := cacheSize(s); got != 0 {
		t.Fatalf("shared-scope promotion must drop every project's cache, %d entries left", got)
	}
}

// The cached slice is shared between requests: it is sorted exactly once at
// projection time (updated_at DESC, id ASC), and handlers only read it. The
// concurrent readers + invalidating writer below is the -race tripwire for a
// per-handler in-place sort regression.
func TestKnowledgeCacheSortedOnceConcurrentReadSafe(t *testing.T) {
	s, _ := newCacheTestServer(t)
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	event := func(eventID, knowledgeID, proposition string, at time.Time) observation.Event {
		e := proposedEvent("lighthouse", eventID, knowledgeID, proposition)
		e.OccurredAt = at
		return e
	}
	seed := []observation.Event{
		event("evt-a", "K-a", "oldest rule", base),
		event("evt-c", "K-c", "newest rule", base.Add(3*time.Hour)),
		// Tie pair: identical UpdatedAt, broken by ID ascending.
		event("evt-tie-1", "K-tie-1", "tied rule one", base.Add(2*time.Hour)),
		event("evt-tie-2", "K-tie-2", "tied rule two", base.Add(2*time.Hour)),
		event("evt-b", "K-b", "middle rule", base.Add(time.Hour)),
	}
	if res, body := doJSON(t, s, "POST", "/v1/observations/events", map[string]any{"events": seed}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	listIDs := func(query string) []string {
		t.Helper()
		res, body := doJSON(t, s, "GET", "/v1/observations/knowledge?project=lighthouse"+query, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("list status = %d", res.StatusCode)
		}
		raw, _ := json.Marshal(body["knowledge"])
		var items []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatalf("decode knowledge: %v", err)
		}
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		return ids
	}

	// Warm the cache, then pin the deterministic order: updated_at DESC with
	// the ID tie-break inside the tied pair.
	if got := listIDs(""); len(got) != 5 {
		t.Fatalf("warm list = %v, want 5 items", got)
	}
	want := []string{"K-c", "K-tie-1", "K-tie-2", "K-b", "K-a"}
	if got := listIDs(""); !equalStrings(got, want) {
		t.Fatalf("cached list order = %v, want %v (updated_at DESC, id ASC)", got, want)
	}

	var wg sync.WaitGroup
	for reader := 0; reader < 8; reader++ {
		wg.Add(1)
		go func(reader int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if reader%2 == 0 {
					_ = listIDs("") // cache-hit path
				} else {
					// Fresh-compute path (bitemporal slice bypasses the cache).
					_ = listIDs("&as_of=2026-09-26T12:00:00Z")
				}
			}
		}(reader)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		// A live run's batches: irrelevant telemetry leaves the cache alone,
		// knowledge transitions drop and refill it — all while readers run.
		for i := 0; i < 20; i++ {
			noise := observation.Event{
				Schema: observation.Schema, EventID: time.Now().Format("evt-noise-150405.000000000"), OccurredAt: time.Now().UTC(),
				Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
				Context: observation.Context{Project: "lighthouse", Run: "run-1"},
				Type:    "tool.called", Data: map[string]any{"tool": "web_search"},
			}
			doJSON(t, s, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{noise}})
			used := observation.Event{
				Schema: observation.Schema, EventID: time.Now().Format("evt-used-150405.000000000"), OccurredAt: time.Now().UTC(),
				Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
				Context: observation.Context{Project: "lighthouse", Run: "run-1"},
				Type:    "knowledge.used", Data: map[string]any{"knowledge_id": "K-a"},
			}
			doJSON(t, s, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{used}})
		}
	}()
	wg.Wait()
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
