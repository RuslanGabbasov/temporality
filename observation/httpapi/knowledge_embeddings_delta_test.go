package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/observation/memory"
)

// embeddingCapableMemory wraps the memory store with the embeddings surface:
// the delta test needs a store that serves knowledge events AND vectors.
type embeddingCapableMemory struct {
	observation.Store
	vectors map[string][]float32
}

func (s *embeddingCapableMemory) ListKnowledgeEmbeddings(_ context.Context, _ string, ids []string) (map[string][]float32, error) {
	out := map[string][]float32{}
	for _, id := range ids {
		if v, ok := s.vectors[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

func (s *embeddingCapableMemory) UpsertKnowledgeEmbeddings(_ context.Context, _ string, items map[string][]float32) error {
	for id, v := range items {
		s.vectors[id] = v
	}
	return nil
}

func TestKnowledgeEmbeddingMapDelta(t *testing.T) {
	store := &embeddingCapableMemory{Store: memory.New(), vectors: map[string][]float32{}}
	handler := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{
		proposedEvent("lighthouse", "evt-1", "K1", "gatekeeper v3 via oauth device flow"),
		proposedEvent("lighthouse", "evt-2", "K2", "migrations run through make migrate"),
	}}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}
	if err := store.UpsertKnowledgeEmbeddings(context.Background(), "test-model", map[string][]float32{
		"K1": {1, 0, 0}, "K2": {0, 1, 0},
	}); err != nil {
		t.Fatalf("embed: %v", err)
	}

	// Full map: two items; grab the max updated_at for the delta query.
	res, body := doJSON(t, handler, "GET", "/v1/observations/knowledge/embeddings/map?project=lighthouse&model=test-model", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("full map status = %d", res.StatusCode)
	}
	if body["total"].(float64) != 2 {
		t.Fatalf("full total = %v, want 2", body["total"])
	}
	if body["full_total"].(float64) != 2 {
		t.Fatalf("full_total = %v, want 2 (present without since too)", body["full_total"])
	}
	var items []struct {
		ID        string `json:"id"`
		UpdatedAt string `json:"updated_at"`
	}
	raw, _ := json.Marshal(body["items"])
	_ = json.Unmarshal(raw, &items)
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	maxUpdated := items[0].UpdatedAt
	for _, it := range items {
		if it.UpdatedAt > maxUpdated {
			maxUpdated = it.UpdatedAt
		}
	}

	// Delta at maxUpdated: nothing changed since → empty items, but the FULL
	// corpus metadata (states, full_total) still rides along — the client
	// replaces its cached counts wholesale.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge/embeddings/map?project=lighthouse&model=test-model&since="+maxUpdated, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delta-at-max status = %d", res.StatusCode)
	}
	if body["total"].(float64) != 0 {
		t.Fatalf("delta total = %v, want 0", body["total"])
	}
	if body["full_total"].(float64) != 2 {
		t.Fatalf("delta full_total = %v, want 2", body["full_total"])
	}
	if body["states"].(map[string]any)["proposed"].(float64) != 2 {
		t.Fatalf("delta states must describe the FULL corpus, got %v", body["states"])
	}

	// Touch K2 (a confirmation bumps updated_at): the delta at the old
	// watermark must return exactly K2.
	confirmedAt := time.Now().UTC().Add(time.Minute)
	if res, body = doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{{
		Schema: observation.Schema, EventID: "evt-3", OccurredAt: confirmedAt,
		Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1", Actor: observation.Actor{ID: "operator", Type: "human"}},
		Type:    "knowledge.confirmed", Data: map[string]any{"knowledge_id": "K2", "reason": "verified"},
	}}}); res.StatusCode != http.StatusOK {
		t.Fatalf("confirm ingest status = %d, body = %v", res.StatusCode, body)
	}
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge/embeddings/map?project=lighthouse&model=test-model&since="+maxUpdated, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delta status = %d", res.StatusCode)
	}
	if body["total"].(float64) != 1 {
		t.Fatalf("delta total = %v, want 1", body["total"])
	}
	deltaItems := body["items"].([]any)
	if deltaItems[0].(map[string]any)["id"] != "K2" {
		t.Fatalf("delta item = %v, want K2", deltaItems[0])
	}
	if body["states"].(map[string]any)["confirmed"].(float64) != 1 {
		t.Fatalf("states must see the confirmation: %v", body["states"])
	}

	// Malformed since → 400.
	res, _ = doJSON(t, handler, "GET", "/v1/observations/knowledge/embeddings/map?since=nonsense", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad since status = %d, want 400", res.StatusCode)
	}
}
