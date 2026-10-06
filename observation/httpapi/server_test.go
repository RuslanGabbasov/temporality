package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
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
	res, _ = doJSONWithToken(t, handler, "GET", "/v1/observations/events", "read-token-1234567890", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("reader unscoped list = %d, want 403", res.StatusCode)
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
