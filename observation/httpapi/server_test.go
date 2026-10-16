package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/observation/memory"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return New(memory.New(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

const testTokens = "read-token-1234567890:alice:reader:lighthouse;write-token-1234567890:kernel:writer:lighthouse;op-token-1234567890:ruslan:operator:lighthouse"

func newAuthTestServer(t *testing.T) http.Handler {
	t.Helper()
	gate, err := controlplane.NewGate(testTokens)
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	return New(memory.New(), slog.New(slog.NewTextHandler(io.Discard, nil)), gate)
}

func doJSONWithToken(t *testing.T, handler http.Handler, method, path, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	payload := map[string]any{}
	if res.ContentLength != 0 {
		raw, _ := io.ReadAll(res.Body)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("decode response %s: %v", path, err)
			}
		}
	}
	return res, payload
}

func doJSON(t *testing.T, handler http.Handler, method, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	payload := map[string]any{}
	if res.ContentLength != 0 {
		raw, _ := io.ReadAll(res.Body)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("decode response %s: %v", path, err)
			}
		}
	}
	return res, payload
}

func proposedEvent(project, eventID, knowledgeID, proposition string) observation.Event {
	return observation.Event{
		Schema: observation.Schema, EventID: eventID, OccurredAt: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: project, Run: "run-1", Actor: observation.Actor{ID: "coder", Type: "agent"}},
		Type:    "knowledge.proposed",
		Data:    map[string]any{"knowledge_id": knowledgeID, "proposition": proposition},
	}
}

func TestJournalFlowIngestListHintsInvalidate(t *testing.T) {
	handler := newTestServer(t)

	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{proposedEvent("lighthouse", "evt-1", "K1", "gatekeeper v2 authenticates via .token-file")},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}
	if body["accepted"].(float64) != 1 {
		t.Fatalf("accepted = %v", body["accepted"])
	}

	res, body = doJSON(t, handler, "GET", "/v1/observations/events?project=lighthouse", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", res.StatusCode)
	}
	events := body["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	first := events[0].(map[string]any)
	if first["event_id"] != "evt-1" {
		t.Fatalf("event_id = %v", first["event_id"])
	}
	if _, ok := first["received_at"]; !ok {
		t.Fatal("server must stamp received_at on ingest")
	}

	res, body = doJSON(t, handler, "POST", "/v1/observations/hints", map[string]any{
		"project": "lighthouse", "query": "how do I authenticate to gatekeeper v2", "actor": map[string]any{"id": "coder", "type": "agent"},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hints status = %d, body = %v", res.StatusCode, body)
	}
	activationID, _ := body["activation_id"].(string)
	if !isDashedUUID(activationID) {
		t.Fatalf("activation_id = %q, want dashed UUID", activationID)
	}
	hints := body["hints"].([]any)
	if len(hints) != 1 {
		t.Fatalf("hints = %d, want 1", len(hints))
	}
	hint := hints[0].(map[string]any)
	if hint["knowledge_id"] != "K1" {
		t.Fatalf("hint knowledge_id = %v", hint["knowledge_id"])
	}
	if body["algorithm_version"] != observation.HintsAlgorithmVersion {
		t.Fatalf("algorithm_version = %v, want %s", body["algorithm_version"], observation.HintsAlgorithmVersion)
	}

	res, body = doJSON(t, handler, "GET", "/v1/observations/events?project=lighthouse&type=hint.query", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hint events list status = %d", res.StatusCode)
	}
	events = body["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("hint.query events = %d, want 1", len(events))
	}
	if source := events[0].(map[string]any)["source"].(map[string]any); source["id"] != "temporality-activation" {
		t.Fatalf("hint.query source.id = %v", source["id"])
	}
	res, body = doJSON(t, handler, "GET", "/v1/observations/events?project=lighthouse&type=hint.offered", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hint.offered list status = %d", res.StatusCode)
	}
	if count := body["count"].(float64); count != 1 {
		t.Fatalf("hint.offered count = %v, want 1", count)
	}
	// The proposition must ride along so run timelines show what was suggested
	// instead of bare hint/knowledge ids.
	offers := body["events"].([]any)
	if len(offers) != 1 {
		t.Fatalf("hint.offered events = %d, want 1", len(offers))
	}
	if prop := offers[0].(map[string]any)["data"].(map[string]any)["proposition"].(string); prop != "gatekeeper v2 authenticates via .token-file" {
		t.Fatalf("hint.offered proposition = %q", prop)
	}

	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("knowledge status = %d", res.StatusCode)
	}
	knowledge := body["knowledge"].([]any)
	if len(knowledge) != 1 || knowledge[0].(map[string]any)["id"] != "K1" {
		t.Fatalf("knowledge = %v", body["knowledge"])
	}

	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/invalidate", map[string]any{
		"knowledge_id": "K1", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"}, "reason": "environment switched to v3",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("invalidate status = %d, body = %v", res.StatusCode, body)
	}

	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("knowledge after invalidate status = %d", res.StatusCode)
	}
	knowledge = body["knowledge"].([]any)
	if state := knowledge[0].(map[string]any)["state"]; state != "invalidated" {
		t.Fatalf("state after invalidate = %v, want invalidated", state)
	}

	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/invalidate", map[string]any{
		"knowledge_id": "K1", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"}, "reason": "again",
	})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("second invalidate status = %d, want 409", res.StatusCode)
	}
}

func TestManualInvalidationWithReplacementSupersedes(t *testing.T) {
	handler := newTestServer(t)
	events := []observation.Event{
		proposedEvent("lighthouse", "evt-1", "K1", "gatekeeper v2 authenticates via .token-file"),
		proposedEvent("lighthouse", "evt-2", "K2", "gatekeeper v3 authenticates via oauth device flow"),
	}
	if res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": events}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	// Invalidation naming a successor becomes a supersession carrying the
	// typed replacement link, not a bare retirement.
	res, body := doJSON(t, handler, "POST", "/v1/observations/knowledge/invalidate", map[string]any{
		"knowledge_id": "K1", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"},
		"reason": "v3 replaced the token-file flow", "replacement_id": "K2",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("supersede status = %d, body = %v", res.StatusCode, body)
	}
	if body["type"] != "knowledge.superseded" {
		t.Fatalf("event type = %v, want knowledge.superseded", body["type"])
	}
	if data := body["data"].(map[string]any); data["replacement_id"] != "K2" {
		t.Fatalf("replacement_id = %v, want K2", data["replacement_id"])
	}

	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&knowledge_id=K1", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("knowledge K1 status = %d", res.StatusCode)
	}
	retired := body["knowledge"].([]any)[0].(map[string]any)
	if retired["state"] != "superseded" || retired["replacement_id"] != "K2" {
		t.Fatalf("retired item = state %v replacement %v, want superseded/K2", retired["state"], retired["replacement_id"])
	}

	// The replacement contract end to end: hints answer with the successor
	// when the task matches the retired rule.
	res, body = doJSON(t, handler, "POST", "/v1/observations/hints", map[string]any{
		"project": "lighthouse", "query": "how do I authenticate to gatekeeper v2", "actor": map[string]any{"id": "coder", "type": "agent"},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hints status = %d", res.StatusCode)
	}
	hints := body["hints"].([]any)
	if len(hints) != 1 {
		t.Fatalf("hints = %d, want the replacement only", len(hints))
	}
	hint := hints[0].(map[string]any)
	if hint["knowledge_id"] != "K2" {
		t.Fatalf("hint knowledge_id = %v, want K2 (the replacement)", hint["knowledge_id"])
	}
	if matched := hint["matched_by"].([]any); len(matched) != 1 || matched[0] != "replaces:K1" {
		t.Fatalf("matched_by = %v, want [replaces:K1]", matched)
	}

	// A replacement that does not exist is rejected, not silently dropped.
	res, _ = doJSON(t, handler, "POST", "/v1/observations/knowledge/invalidate", map[string]any{
		"knowledge_id": "K2", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"},
		"reason": "bad link", "replacement_id": "K404",
	})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown replacement status = %d, want 404", res.StatusCode)
	}
	res, _ = doJSON(t, handler, "POST", "/v1/observations/knowledge/invalidate", map[string]any{
		"knowledge_id": "K2", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"},
		"reason": "self link", "replacement_id": "K2",
	})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("self replacement status = %d, want 422", res.StatusCode)
	}
}

func TestIngestBatchLimits(t *testing.T) {
	handler := newTestServer(t)

	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{}})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty batch status = %d, body = %v", res.StatusCode, body)
	}

	var batch []observation.Event
	for i := 0; i < 101; i++ {
		batch = append(batch, proposedEvent("lighthouse", fmt.Sprintf("evt-%d", i), fmt.Sprintf("K%d", i), "proposition"))
	}
	res, body = doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": batch})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("101 batch status = %d, body = %v", res.StatusCode, body)
	}
}

func TestIngestConflictReturnsMultiStatus(t *testing.T) {
	handler := newTestServer(t)

	_, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{proposedEvent("lighthouse", "evt-1", "K1", "first proposition")},
	})
	if body["accepted"].(float64) != 1 {
		t.Fatalf("accepted = %v", body["accepted"])
	}

	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{
			proposedEvent("lighthouse", "evt-2", "K2", "fresh event"),
			proposedEvent("lighthouse", "evt-1", "K1", "different content same id"),
		},
	})
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("partial failure status = %d, body = %v", res.StatusCode, body)
	}
	results := body["results"].([]any)
	second := results[1].(map[string]any)
	if second["status"] != "rejected" || second["error"] != observation.ErrConflict.Error() {
		t.Fatalf("conflict result = %v", second)
	}
	if body["accepted"].(float64) != 1 {
		t.Fatalf("accepted = %v", body["accepted"])
	}
}

func TestIngestRepeatedIsIdempotent(t *testing.T) {
	handler := newTestServer(t)

	event := proposedEvent("lighthouse", "evt-1", "K1", "same content")
	_, _ = doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{event}})
	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{event}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("repeated status = %d", res.StatusCode)
	}
	if body["repeated"].(float64) != 1 || body["accepted"].(float64) != 0 {
		t.Fatalf("repeated = %v accepted = %v", body["repeated"], body["accepted"])
	}
}

func TestIngestRejectsUnknownFields(t *testing.T) {
	handler := newTestServer(t)

	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{proposedEvent("lighthouse", "evt-1", "K1", "p")}, "unexpected": true,
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, body = %v", res.StatusCode, body)
	}
}

func TestListLimitValidation(t *testing.T) {
	handler := newTestServer(t)

	res, _ := doJSON(t, handler, "GET", "/v1/observations/events?limit=501", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("limit=501 status = %d", res.StatusCode)
	}
	res, _ = doJSON(t, handler, "GET", "/v1/observations/events?limit=0", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("limit=0 status = %d", res.StatusCode)
	}
}

func isDashedUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, char := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

func TestAuthEnforcedOnJournal(t *testing.T) {
	handler := newAuthTestServer(t)

	// No token: everything except healthz is 401.
	res, _ := doJSONWithToken(t, handler, "GET", "/v1/observations/events?project=lighthouse", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", res.StatusCode)
	}
	res, _ = doJSONWithToken(t, handler, "GET", "/healthz", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", res.StatusCode)
	}

	// Reader may list only its project.
	res, _ = doJSONWithToken(t, handler, "GET", "/v1/observations/events?project=lighthouse", "read-token-1234567890", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reader list own project = %d", res.StatusCode)
	}
	res, _ = doJSONWithToken(t, handler, "GET", "/v1/observations/events?project=forge", "read-token-1234567890", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("reader list other project = %d, want 403", res.StatusCode)
	}
	// The unscoped listing is no longer a 403: it returns the intersection
	// with the reader's visible projects (docs/activity-scoping.md §5) — see
	// TestGlobalListingScopedToPrincipal for the content assertions.
	res, _ = doJSONWithToken(t, handler, "GET", "/v1/observations/events", "read-token-1234567890", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reader unscoped list = %d, want 200 (scoped server-side)", res.StatusCode)
	}

	// Reader may not ingest; writer may.
	ingest := map[string]any{"events": []observation.Event{proposedEvent("lighthouse", "auth-1", "K1", "p")}}
	res, _ = doJSONWithToken(t, handler, "POST", "/v1/observations/events", "read-token-1234567890", ingest)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("reader ingest = %d, want 403", res.StatusCode)
	}
	res, _ = doJSONWithToken(t, handler, "POST", "/v1/observations/events", "write-token-1234567890", ingest)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("writer ingest = %d", res.StatusCode)
	}
	// Writer scoped to lighthouse may not ingest into another project.
	foreign := map[string]any{"events": []observation.Event{proposedEvent("forge", "auth-2", "K2", "p")}}
	res, _ = doJSONWithToken(t, handler, "POST", "/v1/observations/events", "write-token-1234567890", foreign)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("writer ingest foreign project = %d, want 403", res.StatusCode)
	}

	// Hints are read-tier: reader allowed, foreign project denied.
	res, _ = doJSONWithToken(t, handler, "POST", "/v1/observations/hints", "read-token-1234567890", map[string]any{"project": "lighthouse", "query": "auth"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reader hints = %d", res.StatusCode)
	}
	res, _ = doJSONWithToken(t, handler, "POST", "/v1/observations/hints", "read-token-1234567890", map[string]any{"project": "forge", "query": "auth"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("reader hints foreign project = %d, want 403", res.StatusCode)
	}

	// Invalidation requires operator.
	invalidate := map[string]any{"knowledge_id": "K1", "project": "lighthouse", "actor": map[string]any{"id": "ruslan", "type": "human"}, "reason": "test"}
	res, _ = doJSONWithToken(t, handler, "POST", "/v1/observations/knowledge/invalidate", "write-token-1234567890", invalidate)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("writer invalidate = %d, want 403", res.StatusCode)
	}
	res, body := doJSONWithToken(t, handler, "POST", "/v1/observations/knowledge/invalidate", "op-token-1234567890", invalidate)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("operator invalidate = %d, body = %v", res.StatusCode, body)
	}
}

func TestGlobalListingScopedToPrincipal(t *testing.T) {
	// docs/activity-scoping.md §5: an empty project filter lists everything the
	// principal may see — no more, no less. Events in other projects and events
	// without a project must stay invisible to a scoped reader.
	store := memory.New()
	gate, err := controlplane.NewGate("scoped-token-1234567890:alice:reader:lighthouse;admin-token-1234567890:root:admin:*")
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	handler := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), gate)
	ingest := func(events ...observation.Event) {
		t.Helper()
		res, body := doJSONWithToken(t, handler, "POST", "/v1/observations/events", "admin-token-1234567890", map[string]any{"events": events})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ingest = %d %v", res.StatusCode, body)
		}
	}
	event := func(project, id string) observation.Event {
		return observation.Event{
			Schema: observation.Schema, EventID: id, OccurredAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
			Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
			Context: observation.Context{Project: project, Run: "run-1", Actor: observation.Actor{ID: "coder", Type: "agent"}},
			Type:    "run.started",
			Data:    map[string]any{"title": id},
		}
	}
	ingest(event("lighthouse", "in-1"), event("forge", "out-1"), event("", "no-project-1"))

	// Scoped reader: sees its own project only.
	res, body := doJSONWithToken(t, handler, "GET", "/v1/observations/events", "scoped-token-1234567890", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("scoped unscoped listing = %d, want 200", res.StatusCode)
	}
	seen := map[string]bool{}
	for _, raw := range body["events"].([]any) {
		e := raw.(map[string]any)
		seen[e["event_id"].(string)] = true
	}
	if !seen["in-1"] || seen["out-1"] || seen["no-project-1"] {
		t.Fatalf("scoped reader saw %v, want only in-1", seen)
	}

	// A run-scoped query from the same reader passes the same constraint: the
	// run's events carry their project, someone else's run comes back empty
	// rather than 403/leaking.
	res, body = doJSONWithToken(t, handler, "GET", "/v1/observations/events?run=run-1", "scoped-token-1234567890", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("run-scoped query = %d, want 200", res.StatusCode)
	}
	if count := body["count"]; count == nil || int(count.(float64)) != 1 {
		t.Fatalf("run-scoped count = %v, want 1 (own project's run only)", body["count"])
	}

	// Admin spans everything, including project-less events.
	res, body = doJSONWithToken(t, handler, "GET", "/v1/observations/events", "admin-token-1234567890", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin listing = %d", res.StatusCode)
	}
	if count := int(body["count"].(float64)); count != 3 {
		t.Fatalf("admin listing count = %d, want 3", count)
	}
}

func TestKnowledgeDiff(t *testing.T) {
	handler := newTestServer(t)
	t1 := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)

	// Ingest: K1 proposed at t1, K2 proposed at t2, K1 invalidated at t3.
	ingest := func(events ...observation.Event) {
		t.Helper()
		res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": events})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ingest failed: %d %v", res.StatusCode, body)
		}
	}
	k1 := proposedEvent("lighthouse", "evt-1", "K1", "gatekeeper v2 authenticates via .token-file")
	k1.OccurredAt = t1
	ingest(k1)
	k2 := proposedEvent("lighthouse", "evt-2", "K2", "pagination uses cursor-based offset")
	k2.OccurredAt = t2
	ingest(k2)
	invalidated := observation.Event{
		Schema: observation.Schema, EventID: "evt-3", OccurredAt: t3,
		Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-2", Actor: observation.Actor{ID: "operator", Type: "human"}},
		Type:    "knowledge.invalidated",
		Data:    map[string]any{"knowledge_id": "K1", "reason": "environment changed", "method": "manual"},
	}
	ingest(invalidated)

	// Diff t1..t2: K2 added.
	res, body := doJSON(t, handler, "GET", "/v1/observations/knowledge/diff?project=lighthouse&from="+t1.Format(time.RFC3339Nano)+"&to="+t2.Format(time.RFC3339Nano), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("diff t1..t2 status = %d", res.StatusCode)
	}
	added, _ := body["added"].([]any)
	if len(added) != 1 || added[0].(map[string]any)["id"] != "K2" {
		t.Fatalf("diff t1..t2 added = %v", added)
	}
	removed, _ := body["removed"].([]any)
	changed, _ := body["changed"].([]any)
	if len(removed) != 0 || len(changed) != 0 {
		t.Fatalf("diff t1..t2 removed=%d changed=%d", len(removed), len(changed))
	}

	// Diff t2..t3: K1 changed (proposed -> invalidated).
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge/diff?project=lighthouse&from="+t2.Format(time.RFC3339Nano)+"&to="+t3.Format(time.RFC3339Nano), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("diff t2..t3 status = %d", res.StatusCode)
	}
	changed, _ = body["changed"].([]any)
	if len(changed) != 1 || changed[0].(map[string]any)["id"] != "K1" {
		t.Fatalf("diff t2..t3 changed = %v", changed)
	}
	if state := changed[0].(map[string]any)["state"]; state != "invalidated" {
		t.Fatalf("diff t2..t3 K1 state = %v, want invalidated", state)
	}
	counts := body["count"].(map[string]any)
	if counts["added"].(float64) != 0 || counts["removed"].(float64) != 0 || counts["changed"].(float64) != 1 {
		t.Fatalf("diff t2..t3 counts = %v", counts)
	}
}

func TestKnowledgeChain(t *testing.T) {
	handler := newTestServer(t)

	// Ingest: K1 proposed, then recalled (hint.offered), then used (hint.used), then outcome.
	ingest := func(events ...observation.Event) {
		t.Helper()
		res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": events})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ingest failed: %d %v", res.StatusCode, body)
		}
	}

	k1 := proposedEvent("lighthouse", "evt-k1", "K1", "gatekeeper v2 authenticates via .token-file")
	ingest(k1)

	// hint.offered
	offered := observation.Event{
		Schema: observation.Schema, EventID: "evt-offer-1", OccurredAt: time.Date(2026, 9, 25, 12, 5, 0, 0, time.UTC),
		Source:  observation.Source{ID: "temporality-activation", Integration: "temporality", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1", Actor: observation.Actor{ID: "coder", Type: "agent"}},
		Type:    "hint.offered",
		Data:    map[string]any{"hint_id": "hint-1", "activation_id": "act-1", "knowledge_id": "K1", "state": "confirmed", "matched_by": "lexical"},
	}
	ingest(offered)

	// hint.used
	used := observation.Event{
		Schema: observation.Schema, EventID: "evt-used-1", OccurredAt: time.Date(2026, 9, 25, 12, 6, 0, 0, time.UTC),
		Source:  observation.Source{ID: "temporality-activation", Integration: "temporality", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1", Actor: observation.Actor{ID: "coder", Type: "agent"}},
		Type:    "hint.used",
		Data:    map[string]any{"hint_id": "hint-1", "knowledge_id": "K1"},
	}
	ingest(used)

	// hint.outcome (helpful)
	outcome := observation.Event{
		Schema: observation.Schema, EventID: "evt-outcome-1", OccurredAt: time.Date(2026, 9, 25, 12, 7, 0, 0, time.UTC),
		Source:  observation.Source{ID: "temporality-activation", Integration: "temporality", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1", Actor: observation.Actor{ID: "coder", Type: "agent"}},
		Type:    "hint.outcome",
		Data:    map[string]any{"hint_id": "hint-1", "knowledge_id": "K1", "outcome": "helpful"},
	}
	ingest(outcome)

	// Query chain
	res, body := doJSON(t, handler, "GET", "/v1/observations/knowledge/chain?project=lighthouse&knowledge_id=K1", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("chain status = %d", res.StatusCode)
	}
	chain, ok := body["chain"].([]any)
	if !ok || len(chain) != 4 {
		t.Fatalf("chain length = %v, want 4", body["chain"])
	}
	// Verify sequence: appeared, recalled, injected, outcome
	types := make([]string, len(chain))
	for i, entry := range chain {
		types[i] = entry.(map[string]any)["type"].(string)
	}
	expected := []string{"appeared", "recalled", "injected", "outcome"}
	for i, want := range expected {
		if types[i] != want {
			t.Fatalf("chain[%d] = %q, want %q (full: %v)", i, types[i], want, types)
		}
	}
	// Verify outcome details
	outcomeEntry := chain[3].(map[string]any)
	if outcomeEntry["details"].(map[string]any)["outcome"] != "helpful" {
		t.Fatalf("outcome details = %v", outcomeEntry["details"])
	}
}

func TestRunState(t *testing.T) {
	handler := newTestServer(t)

	ingest := func(events ...observation.Event) {
		t.Helper()
		res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": events})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ingest failed: %d %v", res.StatusCode, body)
		}
	}

	t1 := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 25, 10, 1, 0, 0, time.UTC)
	t3 := time.Date(2026, 9, 25, 10, 2, 0, 0, time.UTC)

	runStarted := observation.Event{
		Schema: observation.Schema, EventID: "evt-run-1", OccurredAt: t1,
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    "run.started", Data: map[string]any{"turns": 5},
	}
	toolStarted := observation.Event{
		Schema: observation.Schema, EventID: "evt-tool-1", OccurredAt: t2,
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    "tool.started", Data: map[string]any{"tool": "run_command", "operation_id": "op-1"},
	}
	toolCompleted := observation.Event{
		Schema: observation.Schema, EventID: "evt-tool-2", OccurredAt: t3,
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    "tool.completed", Data: map[string]any{"tool": "run_command", "operation_id": "op-1", "exit_code": 0},
	}
	ingest(runStarted, toolStarted, toolCompleted)

	// Full state (no at parameter)
	res, body := doJSON(t, handler, "GET", "/v1/observations/runs/state?project=lighthouse&run=run-1", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("run state status = %d", res.StatusCode)
	}
	entries, ok := body["events"].([]any)
	if !ok || len(entries) != 3 {
		t.Fatalf("entries count = %v, want 3", body["count"])
	}
	if body["status"] != "in_progress" {
		t.Fatalf("status = %v, want in_progress (no run.completed event)", body["status"])
	}

	// State at t2 (only run.started + tool.started)
	res, body = doJSON(t, handler, "GET", "/v1/observations/runs/state?project=lighthouse&run=run-1&at="+t2.Format(time.RFC3339Nano), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("run state at t2 status = %d", res.StatusCode)
	}
	entries, ok = body["events"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("entries at t2 = %v, want 2", body["count"])
	}
	if body["status"] != "in_progress" {
		t.Fatalf("status at t2 = %v, want in_progress", body["status"])
	}
}

func TestCompareRuns(t *testing.T) {
	handler := newTestServer(t)

	ingest := func(events ...observation.Event) {
		t.Helper()
		res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": events})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ingest failed: %d %v", res.StatusCode, body)
		}
	}

	// Run 1: 2 turns, 1 tool call, 1 knowledge used
	run1Started := observation.Event{
		Schema: observation.Schema, EventID: "r1-start", OccurredAt: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    "run.started", Data: map[string]any{},
	}
	run1Tool := observation.Event{
		Schema: observation.Schema, EventID: "r1-tool", OccurredAt: time.Date(2026, 9, 25, 10, 1, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    "tool.started", Data: map[string]any{"tool": "run_command", "operation_id": "op-1"},
	}
	run1Hint := observation.Event{
		Schema: observation.Schema, EventID: "r1-hint", OccurredAt: time.Date(2026, 9, 25, 10, 2, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    "hint.used", Data: map[string]any{"hint_id": "h1", "knowledge_id": "K1"},
	}
	run1Turn1 := observation.Event{
		Schema: observation.Schema, EventID: "r1-turn1", OccurredAt: time.Date(2026, 9, 25, 10, 3, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    "turn.completed", Data: map[string]any{"turn": 1},
	}
	run1Turn2 := observation.Event{
		Schema: observation.Schema, EventID: "r1-turn2", OccurredAt: time.Date(2026, 9, 25, 10, 4, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    "turn.completed", Data: map[string]any{"turn": 2},
	}
	run1Completed := observation.Event{
		Schema: observation.Schema, EventID: "r1-done", OccurredAt: time.Date(2026, 9, 25, 10, 5, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    "run.completed", Data: map[string]any{},
	}
	ingest(run1Started, run1Tool, run1Hint, run1Turn1, run1Turn2, run1Completed)

	// Run 2: 1 turn, 2 tool calls (1 MCP), 0 knowledge used
	run2Started := observation.Event{
		Schema: observation.Schema, EventID: "r2-start", OccurredAt: time.Date(2026, 9, 25, 11, 0, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-2"},
		Type:    "run.started", Data: map[string]any{},
	}
	run2Tool1 := observation.Event{
		Schema: observation.Schema, EventID: "r2-tool1", OccurredAt: time.Date(2026, 9, 25, 11, 1, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-2"},
		Type:    "tool.started", Data: map[string]any{"tool": "run_command", "operation_id": "op-2"},
	}
	run2Tool2 := observation.Event{
		Schema: observation.Schema, EventID: "r2-tool2", OccurredAt: time.Date(2026, 9, 25, 11, 2, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-2"},
		Type:    "tool.started", Data: map[string]any{"tool": "mcp__read_file", "operation_id": "op-3"},
	}
	run2Turn := observation.Event{
		Schema: observation.Schema, EventID: "r2-turn", OccurredAt: time.Date(2026, 9, 25, 11, 3, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-2"},
		Type:    "turn.completed", Data: map[string]any{"turn": 1},
	}
	run2Completed := observation.Event{
		Schema: observation.Schema, EventID: "r2-done", OccurredAt: time.Date(2026, 9, 25, 11, 4, 0, 0, time.UTC),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-2"},
		Type:    "run.completed", Data: map[string]any{},
	}
	ingest(run2Started, run2Tool1, run2Tool2, run2Turn, run2Completed)

	// Compare
	res, body := doJSON(t, handler, "GET", "/v1/observations/runs/compare?project=lighthouse&run1=run-1&run2=run-2", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("compare status = %d", res.StatusCode)
	}
	r1 := body["run1"].(map[string]any)
	r2 := body["run2"].(map[string]any)

	// Run 1: 2 turns, 1 tool, 0 MCP, 1 knowledge
	if r1["turns"].(float64) != 2 {
		t.Fatalf("run1 turns = %v, want 2", r1["turns"])
	}
	if r1["tool_calls"].(float64) != 1 {
		t.Fatalf("run1 tool_calls = %v, want 1", r1["tool_calls"])
	}
	if r1["mcp_requests"].(float64) != 0 {
		t.Fatalf("run1 mcp_requests = %v, want 0", r1["mcp_requests"])
	}
	if r1["knowledge_used"].(float64) != 1 {
		t.Fatalf("run1 knowledge_used = %v, want 1", r1["knowledge_used"])
	}

	// Run 2: 1 turn, 2 tools, 1 MCP, 0 knowledge
	if r2["turns"].(float64) != 1 {
		t.Fatalf("run2 turns = %v, want 1", r2["turns"])
	}
	if r2["tool_calls"].(float64) != 2 {
		t.Fatalf("run2 tool_calls = %v, want 2", r2["tool_calls"])
	}
	if r2["mcp_requests"].(float64) != 1 {
		t.Fatalf("run2 mcp_requests = %v, want 1", r2["mcp_requests"])
	}
	if r2["knowledge_used"].(float64) != 0 {
		t.Fatalf("run2 knowledge_used = %v, want 0", r2["knowledge_used"])
	}
}

func newScopedTestServer(t *testing.T, units map[string][]string, existingUnits map[string]bool) http.Handler {
	t.Helper()
	return New(memory.New(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
		WithProjectUnits(func(_ context.Context, project string) ([]string, error) {
			return units[project], nil
		}),
		WithUnitExists(func(_ context.Context, unitID string) bool {
			return existingUnits[unitID]
		}),
	)
}

func promoteRequest(knowledgeID, project, scopeKind, scopeID, reason string) map[string]any {
	return map[string]any{
		"knowledge_id": knowledgeID,
		"project":      project,
		"actor":        map[string]any{"id": "ruslan", "type": "human"},
		"scope_kind":   scopeKind,
		"scope_id":     scopeID,
		"reason":       reason,
	}
}

func TestKnowledgePromoteToOrganizationSharesAcrossProjects(t *testing.T) {
	handler := newTestServer(t)

	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{proposedEvent("alpha", "evt-1", "K1", "gatekeeper v2 authenticates via helm values auth.tokenFile")},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	// Before promotion the knowledge is project-local: invisible to beta.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=beta", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list beta status = %d", res.StatusCode)
	}
	if body["count"].(float64) != 0 {
		t.Fatalf("beta count before promote = %v, want 0", body["count"])
	}

	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/promote",
		promoteRequest("K1", "alpha", "organization", "", "applies to every service in the installation"))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("promote status = %d, body = %v", res.StatusCode, body)
	}
	if body["type"] != "knowledge.promoted" {
		t.Fatalf("promote event type = %v", body["type"])
	}
	if body["data"].(map[string]any)["from_scope_kind"] != "project" {
		t.Fatalf("from_scope_kind = %v, want project", body["data"].(map[string]any)["from_scope_kind"])
	}

	// The other project now sees the shared knowledge with its scope.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=beta", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list beta status = %d", res.StatusCode)
	}
	if body["count"].(float64) != 1 {
		t.Fatalf("beta count after promote = %v, want 1", body["count"])
	}
	item := body["knowledge"].([]any)[0].(map[string]any)
	if item["id"] != "K1" {
		t.Fatalf("beta knowledge id = %v, want K1", item["id"])
	}
	if item["scope_kind"] != "organization" {
		t.Fatalf("beta knowledge scope_kind = %v, want organization", item["scope_kind"])
	}

	// And hints for the other project surface the shared knowledge.
	res, body = doJSON(t, handler, "POST", "/v1/observations/hints", map[string]any{
		"project": "beta", "query": "how to configure gatekeeper helm auth",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hints status = %d, body = %v", res.StatusCode, body)
	}
	if body["count"].(float64) != 1 {
		t.Fatalf("hints count = %v, want 1", body["count"])
	}
	hint := body["hints"].([]any)[0].(map[string]any)
	if hint["knowledge_id"] != "K1" {
		t.Fatalf("hint knowledge_id = %v, want K1", hint["knowledge_id"])
	}
}

func TestKnowledgePromoteToOrgUnitVisibility(t *testing.T) {
	// alpha and gamma share the dept-dev unit; beta lives under dept-b.
	handler := newScopedTestServer(t,
		map[string][]string{"alpha": {"dept-dev"}, "gamma": {"dept-dev"}, "beta": {"dept-b"}},
		map[string]bool{"dept-dev": true, "dept-b": true})

	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{proposedEvent("alpha", "evt-1", "K1", "temporality journal speaks observation schema v1")},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/promote",
		promoteRequest("K1", "alpha", "org_unit", "dept-dev", "shared across the platform department"))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("promote status = %d, body = %v", res.StatusCode, body)
	}

	// Sibling project in the same unit sees it.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=gamma", nil)
	if res.StatusCode != http.StatusOK || body["count"].(float64) != 1 {
		t.Fatalf("gamma visibility: status=%d count=%v", res.StatusCode, body["count"])
	}
	if item := body["knowledge"].([]any)[0].(map[string]any); item["scope_kind"] != "org_unit" || item["scope_id"] != "dept-dev" {
		t.Fatalf("gamma scope = %v/%v, want org_unit/dept-dev", item["scope_kind"], item["scope_id"])
	}

	// Project outside the unit does not.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=beta", nil)
	if res.StatusCode != http.StatusOK || body["count"].(float64) != 0 {
		t.Fatalf("beta visibility: status=%d count=%v, want 0", res.StatusCode, body["count"])
	}

	// Chain from a project that can see the knowledge includes the promotion.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge/chain?project=gamma&knowledge_id=K1", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("chain gamma status = %d, body = %v", res.StatusCode, body)
	}
	foundPromoted := false
	for _, raw := range body["chain"].([]any) {
		entry := raw.(map[string]any)
		if entry["type"] == "lifecycle" && entry["details"].(map[string]any)["event_type"] == "knowledge.promoted" {
			foundPromoted = true
		}
	}
	if !foundPromoted {
		t.Fatalf("chain for K1 misses the promoted lifecycle entry: %v", body["chain"])
	}

	// Chain from a project that cannot see the knowledge is a 404, not a leak.
	res, body = doJSON(t, handler, "GET", "/v1/observations/knowledge/chain?project=beta&knowledge_id=K1", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("chain beta status = %d, want 404", res.StatusCode)
	}
}

func TestKnowledgePromoteValidation(t *testing.T) {
	handler := newScopedTestServer(t,
		map[string][]string{"alpha": {"dept-dev"}},
		map[string]bool{"dept-dev": true})

	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{
			proposedEvent("alpha", "evt-1", "K1", "postgres partial index keeps scope filters fast"),
			proposedEvent("alpha", "evt-2", "K2", "promote widens visibility only"),
			proposedEvent("alpha", "evt-3", "K3", "retired knowledge stays retired"),
		},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	for _, tc := range []struct {
		name string
		req  map[string]any
		want int
	}{
		{"missing reason", map[string]any{
			"knowledge_id": "K1", "project": "alpha", "actor": map[string]any{"id": "ruslan", "type": "human"}, "scope_kind": "organization",
		}, http.StatusUnprocessableEntity},
		{"invalid scope kind", promoteRequest("K1", "alpha", "project", "", "narrowing is not promote"), http.StatusUnprocessableEntity},
		{"org unit without scope id", promoteRequest("K1", "alpha", "org_unit", "", "must name a unit"), http.StatusUnprocessableEntity},
		{"organization with scope id", promoteRequest("K1", "alpha", "organization", "dept-dev", "organization is global"), http.StatusUnprocessableEntity},
		{"unknown org unit", promoteRequest("K1", "alpha", "org_unit", "dept-nope", "unit must exist"), http.StatusUnprocessableEntity},
		{"unknown knowledge", promoteRequest("nope", "alpha", "organization", "", "nothing to promote"), http.StatusNotFound},
	} {
		res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/promote", tc.req)
		if res.StatusCode != tc.want {
			t.Fatalf("%s: status = %d, want %d, body = %v", tc.name, res.StatusCode, tc.want, body)
		}
	}

	// Same-scope promote conflicts.
	res, _ = doJSON(t, handler, "POST", "/v1/observations/knowledge/promote",
		promoteRequest("K1", "alpha", "org_unit", "dept-dev", "same scope twice"))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("first promote status = %d", res.StatusCode)
	}
	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/promote",
		promoteRequest("K1", "alpha", "org_unit", "dept-dev", "same scope twice"))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("repeat promote status = %d, want 409, body = %v", res.StatusCode, body)
	}

	// Organization is terminal: a second widening conflicts.
	res, _ = doJSON(t, handler, "POST", "/v1/observations/knowledge/promote",
		promoteRequest("K2", "alpha", "organization", "", "widen to everything"))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("promote K2 status = %d", res.StatusCode)
	}
	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/promote",
		promoteRequest("K2", "alpha", "org_unit", "dept-dev", "cannot leave organization"))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("promote after organization status = %d, want 409, body = %v", res.StatusCode, body)
	}

	// Retired knowledge cannot be promoted.
	res, _ = doJSON(t, handler, "POST", "/v1/observations/knowledge/invalidate", map[string]any{
		"knowledge_id": "K3", "project": "alpha", "actor": map[string]any{"id": "ruslan", "type": "human"}, "reason": "obsolete",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("invalidate status = %d", res.StatusCode)
	}
	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/promote",
		promoteRequest("K3", "alpha", "organization", "", "retired stays local"))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("promote retired status = %d, want 409, body = %v", res.StatusCode, body)
	}
}

func TestObservationHintsHonorsAgentContext(t *testing.T) {
	handler := newTestServer(t)

	// Two entity-matched confirmed rules: k-dup restates the attached skill,
	// k-fresh does not. Without context k-dup wins alphabetically.
	ingest := []observation.Event{
		{Schema: observation.Schema, EventID: "evt-1", OccurredAt: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC),
			Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
			Context: observation.Context{Project: "lighthouse", Run: "run-1", Actor: observation.Actor{ID: "coder", Type: "agent"}},
			Type:    "knowledge.proposed",
			Data:    map[string]any{"knowledge_id": "k-dup", "proposition": "The report tool requires the --out flag", "entities": []string{"reporting"}}},
		{Schema: observation.Schema, EventID: "evt-2", OccurredAt: time.Date(2026, 10, 10, 9, 1, 0, 0, time.UTC),
			Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
			Context: observation.Context{Project: "lighthouse", Run: "run-1", Actor: observation.Actor{ID: "coder", Type: "agent"}},
			Type:    "knowledge.confirmed", Data: map[string]any{"knowledge_id": "k-dup"}},
		{Schema: observation.Schema, EventID: "evt-3", OccurredAt: time.Date(2026, 10, 10, 9, 2, 0, 0, time.UTC),
			Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
			Context: observation.Context{Project: "lighthouse", Run: "run-1", Actor: observation.Actor{ID: "coder", Type: "agent"}},
			Type:    "knowledge.proposed",
			Data:    map[string]any{"knowledge_id": "k-fresh", "proposition": "Changelog generation emails subscribers every monday", "entities": []string{"reporting"}}},
		{Schema: observation.Schema, EventID: "evt-4", OccurredAt: time.Date(2026, 10, 10, 9, 3, 0, 0, time.UTC),
			Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
			Context: observation.Context{Project: "lighthouse", Run: "run-1", Actor: observation.Actor{ID: "coder", Type: "agent"}},
			Type:    "knowledge.confirmed", Data: map[string]any{"knowledge_id": "k-fresh"}},
	}
	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": ingest})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	// Skill context demotes the duplicate but keeps it admitted.
	res, body = doJSON(t, handler, "POST", "/v1/observations/hints", map[string]any{
		"project": "lighthouse", "query": "publish the changelog", "entities": []string{"reporting"},
		"actor": map[string]any{"id": "coder", "type": "agent"},
		"active_skills": []map[string]any{
			{"id": "skill-release", "summary": "Release checklist: the report tool requires the --out flag"},
		},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hints status = %d, body = %v", res.StatusCode, body)
	}
	hints := body["hints"].([]any)
	if len(hints) != 2 {
		t.Fatalf("hints = %d, want 2 (duplicate demoted, not excluded)", len(hints))
	}
	if first := hints[0].(map[string]any)["knowledge_id"]; first != "k-fresh" {
		t.Fatalf("first hint = %v, want k-fresh", first)
	}

	// Issued ids suppress re-offering within the same run.
	res, body = doJSON(t, handler, "POST", "/v1/observations/hints", map[string]any{
		"project": "lighthouse", "query": "publish the changelog", "entities": []string{"reporting"},
		"actor":                map[string]any{"id": "coder", "type": "agent"},
		"issued_knowledge_ids": []string{"k-dup", "k-fresh"},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hints status = %d, body = %v", res.StatusCode, body)
	}
	if count := body["count"].(float64); count != 0 {
		t.Fatalf("issued knowledge must be suppressed, got %v hints", body["hints"])
	}

	// Oversized skill context is rejected up front.
	res, body = doJSON(t, handler, "POST", "/v1/observations/hints", map[string]any{
		"project": "lighthouse", "query": "publish the changelog",
		"actor":         map[string]any{"id": "coder", "type": "agent"},
		"active_skills": []map[string]any{{"id": "skill-huge", "summary": strings.Repeat("x", 9000)}},
	})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("oversized active_skills status = %d, body = %v", res.StatusCode, body)
	}
}

// The 500 body of the event listing is what /v1/agent/operations surfaces
// in its error banner (kernel carries the journal body through). A bare
// "could not list" made storage failures undiagnosable from the UI.
type failingListStore struct{ observation.Store }

func (failingListStore) ListObservationPage(context.Context, observation.Filter, string) (observation.Page, error) {
	return observation.Page{}, errors.New("boom: storage unavailable")
}

func TestListObservationsErrorCarriesCause(t *testing.T) {
	handler := New(failingListStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	res, body := doJSON(t, handler, "GET", "/v1/observations/events?project=lighthouse", nil)
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "boom: storage unavailable") {
		t.Fatalf("error body hides the storage cause: %v", body["error"])
	}
}

// proposedAtEvent builds a knowledge.proposed event with an explicit
// occurrence time so the listing order (updated_at DESC) stays deterministic
// in tests that mix proposals with journal-time lifecycle events.
func proposedAtEvent(project, eventID, knowledgeID, proposition string, at time.Time, extra map[string]any) observation.Event {
	event := proposedEvent(project, eventID, knowledgeID, proposition)
	event.OccurredAt = at
	for key, value := range extra {
		event.Data[key] = value
	}
	return event
}

func knowledgeIDs(body map[string]any) []string {
	items, _ := body["knowledge"].([]any)
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if entry, ok := item.(map[string]any); ok {
			ids = append(ids, entry["id"].(string))
		}
	}
	return ids
}

func requireIDs(t *testing.T, body map[string]any, want ...string) {
	t.Helper()
	got := knowledgeIDs(body)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("knowledge ids = %v, want %v", got, want)
	}
}

// The knowledge listing is the working surface for large knowledge bases:
// search, state/scope filters with facet counts, deterministic order and
// cursor pagination — while a plain call still returns the full projection
// for the legacy consumers (run inspector, kernel skill memory).
func TestKnowledgeListSearchFacetsPagination(t *testing.T) {
	handler := newTestServer(t)
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	ingest := []observation.Event{
		proposedAtEvent("lighthouse", "evt-k1", "K1", "gatekeeper v2 authenticates via .token-file", base, nil),
		proposedAtEvent("lighthouse", "evt-k2", "K2", "docker tmpfs must be at least 512 MB", base.Add(5*time.Minute), map[string]any{
			"topics": []string{"docker"}, "entities": []string{"tmpfs"},
		}),
		proposedAtEvent("lighthouse", "evt-k3", "K3", "run make test before push", base.Add(10*time.Minute), nil),
		proposedAtEvent("other", "evt-k4", "K4", "gatekeeper rule of another project", base.Add(15*time.Minute), nil),
	}
	if res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": ingest}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}
	// Retire K3 and widen K2 to organization scope through the real endpoints:
	// both stamp s.now(), which is after every proposal, so the listing order
	// is K2 (promoted last), K3, K1.
	if res, body := doJSON(t, handler, "POST", "/v1/observations/knowledge/invalidate", map[string]any{
		"knowledge_id": "K3", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"}, "reason": "obsolete",
	}); res.StatusCode != http.StatusCreated {
		t.Fatalf("invalidate status = %d, body = %v", res.StatusCode, body)
	}
	if res, body := doJSON(t, handler, "POST", "/v1/observations/knowledge/promote", map[string]any{
		"knowledge_id": "K2", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"},
		"scope_kind": "organization", "reason": "holds everywhere",
	}); res.StatusCode != http.StatusCreated {
		t.Fatalf("promote status = %d, body = %v", res.StatusCode, body)
	}

	t.Run("full projection without limit keeps legacy shape", func(t *testing.T) {
		res, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse", nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", res.StatusCode)
		}
		requireIDs(t, body, "K2", "K3", "K1")
		if body["total"].(float64) != 3 || body["count"].(float64) != 3 {
			t.Fatalf("total/count = %v/%v, want 3/3", body["total"], body["count"])
		}
		if body["next_cursor"] != "" {
			t.Fatalf("next_cursor = %v, want empty", body["next_cursor"])
		}
		facets := body["facets"].(map[string]any)
		states := facets["states"].(map[string]any)
		if states["proposed"].(float64) != 2 || states["invalidated"].(float64) != 1 || states["alive"].(float64) != 2 {
			t.Fatalf("state facets = %v", states)
		}
		scopes := facets["scopes"].(map[string]any)
		if scopes["project"].(float64) != 2 || scopes["organization"].(float64) != 1 || scopes["org_unit"].(float64) != 0 {
			t.Fatalf("scope facets = %v", scopes)
		}
	})

	t.Run("search is case-insensitive, substring and multi-word AND", func(t *testing.T) {
		_, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&q=GATEKEEPER", nil)
		requireIDs(t, body, "K1")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&q=token-file", nil)
		requireIDs(t, body, "K1")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&q=tmpfs+docker", nil)
		requireIDs(t, body, "K2")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&q=docker+nomatch", nil)
		requireIDs(t, body)
		// Search covers topics and entities, not just propositions.
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&q=512", nil)
		requireIDs(t, body, "K2")
	})

	t.Run("state filter supports lifecycle states and pseudo-states", func(t *testing.T) {
		_, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&state=alive", nil)
		requireIDs(t, body, "K2", "K1")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&state=invalidated", nil)
		requireIDs(t, body, "K3")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&state=at_risk", nil)
		requireIDs(t, body)
	})

	t.Run("scope filter", func(t *testing.T) {
		_, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&scope=organization", nil)
		requireIDs(t, body, "K2")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&scope=project", nil)
		requireIDs(t, body, "K3", "K1")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&q=docker&scope=project", nil)
		requireIDs(t, body)
	})

	t.Run("facets respect the other dimensions but not their own filter", func(t *testing.T) {
		_, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&state=invalidated", nil)
		states := body["facets"].(map[string]any)["states"].(map[string]any)
		// The state facet still shows the full split (2 proposed / 1 invalidated)
		// even with the state filter active…
		if states["proposed"].(float64) != 2 || states["invalidated"].(float64) != 1 {
			t.Fatalf("state facets under state filter = %v", states)
		}
		// …while the scope facet applies the state filter.
		scopes := body["facets"].(map[string]any)["scopes"].(map[string]any)
		if scopes["project"].(float64) != 1 || scopes["organization"].(float64) != 0 {
			t.Fatalf("scope facets under state filter = %v", scopes)
		}
	})

	t.Run("cursor pagination follows the deterministic order", func(t *testing.T) {
		_, page1 := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&limit=2", nil)
		requireIDs(t, page1, "K2", "K3")
		if page1["total"].(float64) != 3 {
			t.Fatalf("total = %v, want 3", page1["total"])
		}
		cursor, _ := page1["next_cursor"].(string)
		if cursor == "" {
			t.Fatal("next_cursor is empty with more pages left")
		}
		page2URL := "/v1/observations/knowledge?project=lighthouse&limit=2&cursor=" + cursor
		_, page2 := doJSON(t, handler, "GET", page2URL, nil)
		requireIDs(t, page2, "K1")
		if page2["next_cursor"] != "" {
			t.Fatalf("next_cursor = %v, want empty on last page", page2["next_cursor"])
		}
		// The cursor is opaque but round-trips: limit=1 walks the whole list.
		_, first := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&limit=1", nil)
		requireIDs(t, first, "K2")
		next, _ := first["next_cursor"].(string)
		_, second := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&limit=1&cursor="+next, nil)
		requireIDs(t, second, "K3")
	})

	t.Run("knowledge_id filter composes with pagination", func(t *testing.T) {
		_, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&knowledge_id=K2&limit=1", nil)
		requireIDs(t, body, "K2")
		if body["total"].(float64) != 1 {
			t.Fatalf("total = %v, want 1", body["total"])
		}
	})

	t.Run("invalid parameters are rejected", func(t *testing.T) {
		for _, path := range []string{
			"/v1/observations/knowledge?project=lighthouse&limit=0",
			"/v1/observations/knowledge?project=lighthouse&limit=501",
			"/v1/observations/knowledge?project=lighthouse&state=bogus",
			"/v1/observations/knowledge?project=lighthouse&scope=bogus",
			"/v1/observations/knowledge?project=lighthouse&kind=bogus",
			"/v1/observations/knowledge?project=lighthouse&cursor=not-a-cursor",
		} {
			if res, _ := doJSON(t, handler, "GET", path, nil); res.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s status = %d, want 400", path, res.StatusCode)
			}
		}
	})
}

// The research surface (docs/human-knowledge-explorer.md §6) navigates by
// facet values, not just lifecycle state: topic/entity filters restrict the
// listing, origin separates shared knowledge by birth project, and kind
// separates claims from kernel observations. Facet counters follow the same
// own-filter-exempt rule as states/scopes.
func TestKnowledgeListTopicEntityOriginKindFilters(t *testing.T) {
	handler := newTestServer(t)
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	ingest := []observation.Event{
		proposedAtEvent("lighthouse", "evt-f1", "K1", "deploys run through the docker builder", base, map[string]any{
			"topics": []string{"deploy"}, "entities": []string{"docker"},
		}),
		proposedAtEvent("lighthouse", "evt-f2", "K2", "the registry mirrors images nightly", base.Add(5*time.Minute), map[string]any{
			"kind": "observation", "topics": []string{"deploy"}, "entities": []string{"registry"},
		}),
		proposedAtEvent("lighthouse", "evt-f3", "K3", "tests fail fast on missing fixtures", base.Add(10*time.Minute), map[string]any{
			"topics": []string{"testing"},
		}),
		// Born shared in another project: visible here, origin distinguishes it.
		proposedAtEvent("other", "evt-f4", "K4", "organization-wide deploy checklist", base.Add(15*time.Minute), map[string]any{
			"scope_kind": "organization", "topics": []string{"deploy"},
		}),
	}
	if res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": ingest}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	t.Run("value filters are exact and case-insensitive", func(t *testing.T) {
		_, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&topic=deploy", nil)
		requireIDs(t, body, "K4", "K2", "K1")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&topic=DEPLOY", nil)
		requireIDs(t, body, "K4", "K2", "K1")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&entity=docker", nil)
		requireIDs(t, body, "K1")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&origin=other", nil)
		requireIDs(t, body, "K4")
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&kind=observation", nil)
		requireIDs(t, body, "K2")
		// topic=deploy is a substring of nothing else: exact match means the
		// testing-only K3 never leaks in.
		_, body = doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&topic=deploy&entity=registry", nil)
		requireIDs(t, body, "K2")
	})

	t.Run("facets extend to topics entities projects and kinds", func(t *testing.T) {
		_, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse", nil)
		facets := body["facets"].(map[string]any)
		topics := facets["topics"].(map[string]any)
		if topics["deploy"].(float64) != 3 || topics["testing"].(float64) != 1 {
			t.Fatalf("topic facets = %v", topics)
		}
		entities := facets["entities"].(map[string]any)
		if entities["docker"].(float64) != 1 || entities["registry"].(float64) != 1 {
			t.Fatalf("entity facets = %v", entities)
		}
		projects := facets["projects"].(map[string]any)
		if projects["lighthouse"].(float64) != 3 || projects["other"].(float64) != 1 {
			t.Fatalf("project facets = %v", projects)
		}
		kinds := facets["kinds"].(map[string]any)
		if kinds["claim"].(float64) != 3 || kinds["observation"].(float64) != 1 {
			t.Fatalf("kind facets = %v", kinds)
		}
	})

	t.Run("facet dimensions drop only their own filter", func(t *testing.T) {
		_, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse&topic=deploy", nil)
		facets := body["facets"].(map[string]any)
		// The topic facet ignores the active topic filter (deploy 3, testing 1)…
		topics := facets["topics"].(map[string]any)
		if topics["deploy"].(float64) != 3 || topics["testing"].(float64) != 1 {
			t.Fatalf("topic facets under topic filter = %v", topics)
		}
		// …while kinds, entities and projects apply it.
		kinds := facets["kinds"].(map[string]any)
		if kinds["claim"].(float64) != 2 || kinds["observation"].(float64) != 1 {
			t.Fatalf("kind facets under topic filter = %v", kinds)
		}
		entities := facets["entities"].(map[string]any)
		if entities["docker"].(float64) != 1 || entities["registry"].(float64) != 1 {
			t.Fatalf("entity facets under topic filter = %v", entities)
		}
		projects := facets["projects"].(map[string]any)
		if projects["lighthouse"].(float64) != 2 || projects["other"].(float64) != 1 {
			t.Fatalf("project facets under topic filter = %v", projects)
		}
	})

	t.Run("facet value counters cap at the most frequent values", func(t *testing.T) {
		handler := newTestServer(t)
		events := make([]observation.Event, 0, knowledgeFacetValueCap+10)
		for i := 0; i < knowledgeFacetValueCap+10; i++ {
			topic := fmt.Sprintf("topic-%02d", i)
			extra := map[string]any{"topics": []string{topic}}
			if i < 5 {
				// The five hottest values must survive the cap.
				extra["topics"] = []string{topic, "hot"}
			}
			events = append(events, proposedAtEvent("lighthouse", fmt.Sprintf("evt-cap-%d", i), fmt.Sprintf("KC%d", i), fmt.Sprintf("item %d", i), base.Add(time.Duration(i)*time.Minute), extra))
		}
		if res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": events}); res.StatusCode != http.StatusOK {
			t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
		}
		_, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse", nil)
		topics := body["facets"].(map[string]any)["topics"].(map[string]any)
		if len(topics) != knowledgeFacetValueCap {
			t.Fatalf("topic facet size = %d, want %d", len(topics), knowledgeFacetValueCap)
		}
		if topics["hot"].(float64) != 5 {
			t.Fatalf("hot topic count = %v, want 5", topics["hot"])
		}
	})
}

// GET /v1/observations/events/{id} backs the dossier's evidence jumps
// (docs/human-knowledge-explorer.md §8.3): refs carry only the event id.
func TestGetObservationEventByID(t *testing.T) {
	handler := newTestServer(t)
	events := []observation.Event{
		proposedEvent("lighthouse", "evt-a", "K1", "first knowledge item"),
		proposedEvent("lighthouse", "evt-b", "K2", "second knowledge item"),
	}
	if res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": events}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	res, body := doJSON(t, handler, "GET", "/v1/observations/events/evt-a", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body = %v", res.StatusCode, body)
	}
	event, ok := body["event"].(map[string]any)
	if !ok || event["event_id"] != "evt-a" {
		t.Fatalf("event payload = %v", body)
	}

	if res, _ := doJSON(t, handler, "GET", "/v1/observations/events/missing", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", res.StatusCode)
	}

	// Two producers may reuse an id: source_id disambiguates, project scopes.
	clashing := proposedEvent("lighthouse", "evt-a", "K1", "first knowledge item")
	clashing.Source.ID = "kernel-other"
	clashing.OccurredAt = time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC)
	if res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": []observation.Event{clashing}}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest clash status = %d, body = %v", res.StatusCode, body)
	}
	_, body = doJSON(t, handler, "GET", "/v1/observations/events/evt-a?source_id=kernel-other", nil)
	event = body["event"].(map[string]any)
	if event["source"].(map[string]any)["id"] != "kernel-other" {
		t.Fatalf("disambiguated source = %v", event["source"])
	}
	// The project filter constrains the search space, not the payload.
	_, body = doJSON(t, handler, "GET", "/v1/observations/events/evt-a?project=lighthouse", nil)
	event = body["event"].(map[string]any)
	if event["context"].(map[string]any)["project"] != "lighthouse" {
		t.Fatalf("event context = %v", event["context"])
	}
	if res, _ := doJSON(t, handler, "GET", "/v1/observations/events/evt-a?project=nowhere", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("status with invisible project = %d, want 404", res.StatusCode)
	}
}

// A manual challenge marks knowledge as doubtful without retiring it: hints
// keep offering it (with a caution), and a later confirm/correct still can.
func TestKnowledgeManualChallenge(t *testing.T) {
	handler := newTestServer(t)
	if res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{proposedEvent("lighthouse", "evt-c1", "K1", "gatekeeper v2 authenticates via .token-file")},
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	if res, body := doJSON(t, handler, "POST", "/v1/observations/knowledge/challenge", map[string]any{
		"knowledge_id": "K1", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"}, "reason": "looks wrong on staging",
	}); res.StatusCode != http.StatusCreated {
		t.Fatalf("challenge status = %d, body = %v", res.StatusCode, body)
	}

	_, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse", nil)
	knowledge := body["knowledge"].([]any)
	if state := knowledge[0].(map[string]any)["state"]; state != "challenged" {
		t.Fatalf("state after challenge = %v, want challenged", state)
	}

	// A bare reason is mandatory — a drive-by doubt without justification is
	// worthless in the audit trail.
	if res, _ := doJSON(t, handler, "POST", "/v1/observations/knowledge/challenge", map[string]any{
		"knowledge_id": "K1", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"}, "reason": "  ",
	}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("challenge without reason status = %d, want 422", res.StatusCode)
	}
	if res, _ := doJSON(t, handler, "POST", "/v1/observations/knowledge/challenge", map[string]any{
		"knowledge_id": "K404", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"}, "reason": "missing target",
	}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("challenge of unknown knowledge status = %d, want 404", res.StatusCode)
	}

	// Retired knowledge can no longer be doubted — invalidation wins.
	if res, _ := doJSON(t, handler, "POST", "/v1/observations/knowledge/invalidate", map[string]any{
		"knowledge_id": "K1", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"}, "reason": "confirmed wrong",
	}); res.StatusCode != http.StatusCreated {
		t.Fatalf("invalidate status = %d", res.StatusCode)
	}
	if res, _ := doJSON(t, handler, "POST", "/v1/observations/knowledge/challenge", map[string]any{
		"knowledge_id": "K1", "project": "lighthouse", "actor": map[string]any{"id": "operator", "type": "human"}, "reason": "too late",
	}); res.StatusCode != http.StatusConflict {
		t.Fatalf("challenge after invalidate status = %d, want 409", res.StatusCode)
	}
}

func TestObservationHintsScopedToTeamRunTree(t *testing.T) {
	// Team-run scoping (docs/agent-teams.md §8): a slot asking for hints with
	// its team root sees knowledge produced by any run of that tree, even for
	// an unrelated task; a plain run outside the team does not.
	handler := newTestServer(t)
	teamEvent := proposedEvent("lighthouse", "evt-team-1", "K-team", "the legacy deploy script hangs on cached credentials")
	teamEvent.Context.Run = "team-run-1/plan/01-coder"
	otherEvent := proposedEvent("lighthouse", "evt-other", "K-other", "the release notes live in docs/notes")
	otherEvent.Context.Run = "run-unrelated"
	res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{
		"events": []observation.Event{teamEvent, otherEvent},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	// A later slot of the same team asks about an unrelated task: the
	// mid-run learning is still admitted.
	res, body = doJSON(t, handler, "POST", "/v1/observations/hints", map[string]any{
		"project": "lighthouse", "query": "rename the marketing landing page headline",
		"actor": map[string]any{"id": "qa", "type": "agent"}, "team_root": "team-run-1",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hints status = %d, body = %v", res.StatusCode, body)
	}
	hints := body["hints"].([]any)
	if len(hints) != 1 {
		t.Fatalf("team-scoped hints = %d, want exactly the team-run learning", len(hints))
	}
	hint := hints[0].(map[string]any)
	if hint["knowledge_id"] != "K-team" {
		t.Fatalf("knowledge_id = %v, want K-team", hint["knowledge_id"])
	}

	// The same query without the team root yields an honest zero.
	res, body = doJSON(t, handler, "POST", "/v1/observations/hints", map[string]any{
		"project": "lighthouse", "query": "rename the marketing landing page headline",
		"actor": map[string]any{"id": "qa", "type": "agent"},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hints status = %d", res.StatusCode)
	}
	if hints := body["hints"].([]any); len(hints) != 0 {
		t.Fatalf("unrelated run must not see team-scoped knowledge, got %v", hints)
	}
}

func TestObservationHealthStats(t *testing.T) {
	// The dashboard aggregate (docs/system-dashboard.md §3.1) is computed
	// server-side over the whole window, with the same project scoping as the
	// event listing: a scoped reader sees only its project, a principal with no
	// visible projects gets a zeroed but well-formed answer.
	store := memory.New()
	gate, err := controlplane.NewGate("scoped-token-1234567890:alice:reader:lighthouse;admin-token-1234567890:root:admin:*")
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	// Static token entries require at least one project; workspace DB tokens do
	// not, which is how a zero-visible-project principal exists in production.
	gate.SetDBTokens(map[string]controlplane.Principal{"empty-token-1234567890": {Subject: "nobody", Role: controlplane.RoleReader}})
	handler := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), gate)

	now := time.Now().UTC()
	healthEvent := func(project, id, run, eventType string, offset time.Duration, data map[string]any) observation.Event {
		return observation.Event{
			Schema: observation.Schema, EventID: id, OccurredAt: now.Add(offset),
			Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
			Context: observation.Context{Project: project, Run: run, Actor: observation.Actor{ID: "coder", Type: "agent"}},
			Type:    eventType, Data: data,
		}
	}
	approvalData := func(operationID string) map[string]any {
		return map[string]any{"operation_id": operationID, "arguments_hash": "sha256:" + operationID}
	}
	// lighthouse: one in-flight gpt-5.2 call, one completed claude call with
	// tokens, one running run and one pending approval. forge: noise that must
	// stay invisible to the scoped reader.
	ingest := []observation.Event{
		healthEvent("lighthouse", "h1", "run-1", "run.started", -9*time.Minute, nil),
		healthEvent("lighthouse", "h2", "run-1", "model.started", -8*time.Minute, map[string]any{"turn": 1, "model": "gpt-5.2"}),
		healthEvent("lighthouse", "h3", "run-1", "model.started", -7*time.Minute, map[string]any{"turn": 2, "model": "claude-sonnet"}),
		healthEvent("lighthouse", "h4", "run-1", "model.completed", -6*time.Minute, map[string]any{"turn": float64(2), "model": "claude-sonnet", "provider": "anthropic", "latency_ms": 1500, "total_tokens": 1200}),
		healthEvent("lighthouse", "h5", "run-2", "run.started", -5*time.Minute, nil),
		healthEvent("lighthouse", "h6", "run-2", "run.completed", -4*time.Minute, nil),
		healthEvent("lighthouse", "h7", "run-1", "approval.requested", -3*time.Minute, approvalData("op-1")),
		healthEvent("lighthouse", "h8", "run-1", "model.attempt", -2*time.Minute, map[string]any{"turn": 1}),
		healthEvent("forge", "h9", "run-x", "model.started", -2*time.Minute, map[string]any{"turn": 1, "model": "forge-model"}),
		healthEvent("forge", "h10", "run-x", "model.started", -1*time.Minute, map[string]any{"turn": 2, "model": "forge-model"}),
	}
	res, body := doJSONWithToken(t, handler, "POST", "/v1/observations/events", "admin-token-1234567890", map[string]any{"events": ingest})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ingest = %d %v", res.StatusCode, body)
	}

	modelByName := func(body map[string]any, name string) map[string]any {
		t.Helper()
		for _, raw := range body["models"].([]any) {
			if entry := raw.(map[string]any); entry["model"] == name {
				return entry
			}
		}
		return nil
	}
	intValue := func(body map[string]any, key string) int {
		t.Helper()
		return int(body[key].(float64))
	}

	// Scoped reader: global aggregation constrained to lighthouse.
	res, body = doJSONWithToken(t, handler, "GET", "/v1/observations/stats/health", "scoped-token-1234567890", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("scoped health stats = %d %v", res.StatusCode, body)
	}
	gpt := modelByName(body, "gpt-5.2")
	if gpt == nil || intValue(gpt, "calls") != 1 || intValue(gpt, "in_flight") != 1 {
		t.Fatalf("gpt-5.2 = %v, want calls=1 in_flight=1", gpt)
	}
	claude := modelByName(body, "claude-sonnet")
	if claude == nil || intValue(claude, "completed") != 1 || intValue(claude, "in_flight") != 0 || intValue(claude, "total_tokens") != 1200 {
		t.Fatalf("claude-sonnet = %v, want completed=1 in_flight=0 total_tokens=1200", claude)
	}
	if modelByName(body, "forge-model") != nil {
		t.Fatalf("scoped reader must not see forge models: %v", body["models"])
	}
	totals := body["totals"].(map[string]any)
	if int(totals["model_calls"].(float64)) != 2 || int(totals["in_flight"].(float64)) != 1 {
		t.Fatalf("totals = %v, want model_calls=2 in_flight=1", totals)
	}
	runs := body["runs"].(map[string]any)
	if int(runs["started"].(float64)) != 2 || int(runs["running_now"].(float64)) != 1 {
		t.Fatalf("runs = %v, want started=2 running_now=1", runs)
	}
	approvals := body["approvals"].(map[string]any)
	if int(approvals["requested"].(float64)) != 1 || int(approvals["pending"].(float64)) != 1 {
		t.Fatalf("approvals = %v, want requested=1 pending=1", approvals)
	}

	// Zero visible projects: zeros with the window fields, not an error.
	res, body = doJSONWithToken(t, handler, "GET", "/v1/observations/stats/health", "empty-token-1234567890", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("zero-visible health stats = %d %v", res.StatusCode, body)
	}
	if models := body["models"].([]any); len(models) != 0 {
		t.Fatalf("zero-visible models = %v, want empty", models)
	}
	if body["since"] == nil || body["generated_at"] == nil {
		t.Fatalf("zero-visible answer lost the window fields: %v", body)
	}

	// An explicit project filter behaves like the listing's: allowed project
	// in, foreign project 403.
	res, body = doJSONWithToken(t, handler, "GET", "/v1/observations/stats/health?project=forge", "scoped-token-1234567890", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("scoped foreign project = %d, want 403", res.StatusCode)
	}
	res, body = doJSONWithToken(t, handler, "GET", "/v1/observations/stats/health?project=lighthouse", "scoped-token-1234567890", nil)
	if res.StatusCode != http.StatusOK || intValue(body["totals"].(map[string]any), "model_calls") != 2 {
		t.Fatalf("scoped own project = %d %v, want 200 with model_calls=2", res.StatusCode, body)
	}

	// since older than 31 days is rejected; the 31-day boundary is not.
	ancient := now.Add(-32 * 24 * time.Hour).UTC().Format(time.RFC3339)
	res, body = doJSONWithToken(t, handler, "GET", "/v1/observations/stats/health?since="+ancient, "admin-token-1234567890", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("ancient since = %d %v, want 400", res.StatusCode, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "31 days") {
		t.Fatalf("ancient since error = %q, want the 31-day cap message", msg)
	}
	// RFC3339 truncates sub-second precision, so stay a minute inside the cap
	// to keep the parsed since from tipping over 31 days old.
	boundary := now.Add(-31*24*time.Hour + time.Minute).UTC().Format(time.RFC3339)
	res, _ = doJSONWithToken(t, handler, "GET", "/v1/observations/stats/health?since="+boundary, "admin-token-1234567890", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("31-day boundary since = %d, want 200", res.StatusCode)
	}
	// A malformed since is a 400 too.
	res, _ = doJSONWithToken(t, handler, "GET", "/v1/observations/stats/health?since=yesterday", "admin-token-1234567890", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed since = %d, want 400", res.StatusCode)
	}
}

// The aggregation needs a store that can load a whole window in one pass;
// stores without that capability answer 501 instead of degrading to a page.
func TestObservationHealthStatsUnsupportedStore(t *testing.T) {
	handler := New(failingListStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	res, body := doJSON(t, handler, "GET", "/v1/observations/stats/health", nil)
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("unsupported store = %d %v, want 501", res.StatusCode, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "health stats") {
		t.Fatalf("501 body = %v, want the health stats notice", body)
	}
}
