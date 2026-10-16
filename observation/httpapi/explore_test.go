package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// The explore endpoint (docs/agent-knowledge-exploration.md §4, §5.1) is the
// transport for the research engine: same visibility path as every knowledge
// endpoint, strict validation, and bitemporal slicing through the shared
// visible-events loader.
func exploreTestEvents(project string) []observation.Event {
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	event := func(eventID, knowledgeID, proposition string, occurredAt time.Time) observation.Event {
		return observation.Event{
			Schema: observation.Schema, EventID: eventID, OccurredAt: occurredAt,
			Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
			Context: observation.Context{Project: project, Run: "run-1", Actor: observation.Actor{ID: "coder", Type: "agent"}},
			Type:    "knowledge.proposed",
			Data:    map[string]any{"knowledge_id": knowledgeID, "proposition": proposition, "topics": []any{"миграции"}, "entities": []any{"postgres"}},
		}
	}
	events := []observation.Event{
		event("evt-1", "K1", "При изменении схемы БД нужно создавать миграцию", base),
		event("evt-2", "K2", "Миграции запускаются через make migrate", base.Add(time.Hour)),
	}
	events = append(events, observation.Event{
		Schema: observation.Schema, EventID: "evt-3", OccurredAt: base.Add(2 * time.Hour),
		Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: project, Run: "run-1", Actor: observation.Actor{ID: "ruslan", Type: "human"}},
		Type:    "knowledge.invalidated", Data: map[string]any{"knowledge_id": "K2", "reason": "replaced by make migrate target"},
	})
	return events
}

func TestKnowledgeExploreOperations(t *testing.T) {
	handler := newTestServer(t)
	res, _ := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": exploreTestEvents("lighthouse")})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d", res.StatusCode)
	}

	// orient + search run off a question.
	res, body := doJSON(t, handler, "POST", "/v1/observations/knowledge/explore", map[string]any{
		"project": "lighthouse", "operation": "search", "question": "как делать миграции",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d, body = %v", res.StatusCode, body)
	}
	if body["algorithm_version"] != observation.ExploreAlgorithmVersion {
		t.Fatalf("algorithm_version = %v", body["algorithm_version"])
	}
	results := body["results"].([]any)
	if len(results) == 0 {
		t.Fatalf("search results empty: %v", body)
	}
	first := results[0].(map[string]any)
	if first["state"] != "confirmed" && first["state"] != "proposed" {
		t.Fatalf("state missing on card: %v", first)
	}
	if _, ok := first["matched_by"]; !ok {
		t.Fatal("matched_by explanations missing")
	}

	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/explore", map[string]any{
		"project": "lighthouse", "operation": "orient", "question": "миграции",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("orient status = %d", res.StatusCode)
	}
	if _, ok := body["areas"]; !ok {
		t.Fatalf("orient areas missing: %v", body)
	}

	// expand + trace run off targets.
	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/explore", map[string]any{
		"project": "lighthouse", "operation": "trace", "targets": []string{"K2"},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("trace status = %d", res.StatusCode)
	}
	traces := body["traces"].([]any)
	if len(traces) != 1 {
		t.Fatalf("traces = %v", body)
	}
	steps := traces[0].(map[string]any)["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("K2 history must include proposal and invalidation, got %d steps", len(steps))
	}

	// compare without targets auto-detects; synthesize accepts a question.
	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/explore", map[string]any{
		"project": "lighthouse", "operation": "compare",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("compare status = %d", res.StatusCode)
	}
	res, body = doJSON(t, handler, "POST", "/v1/observations/knowledge/explore", map[string]any{
		"project": "lighthouse", "operation": "synthesize", "question": "миграции",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("synthesize status = %d", res.StatusCode)
	}
	synthesis, ok := body["synthesis"].(map[string]any)
	if !ok {
		t.Fatalf("synthesis missing: %v", body)
	}
	retired := synthesis["retired"].([]any)
	if len(retired) != 1 || retired[0].(map[string]any)["knowledge_id"] != "K2" {
		t.Fatalf("invalidated K2 must land in retired: %v", retired)
	}
}

func TestKnowledgeExploreBitemporalAsOf(t *testing.T) {
	handler := newTestServer(t)
	res, _ := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": exploreTestEvents("lighthouse")})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d", res.StatusCode)
	}
	// Before the invalidation K2 was alive; as_of slices must not mix states.
	res, body := doJSON(t, handler, "POST", "/v1/observations/knowledge/explore", map[string]any{
		"project": "lighthouse", "operation": "trace", "targets": []string{"K2"},
		"as_of": time.Date(2026, 9, 25, 13, 30, 0, 0, time.UTC).Format(time.RFC3339Nano),
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("trace as_of status = %d", res.StatusCode)
	}
	traces := body["traces"].([]any)
	state := traces[0].(map[string]any)["state"]
	if state != "proposed" {
		t.Fatalf("as_of before invalidation must show K2 live, got %v", state)
	}
}

func TestKnowledgeExploreValidation(t *testing.T) {
	handler := newTestServer(t)
	cases := []struct {
		name    string
		payload map[string]any
		status  int
	}{
		{"unknown operation", map[string]any{"project": "lighthouse", "operation": "guess", "question": "x"}, http.StatusUnprocessableEntity},
		{"search without question", map[string]any{"project": "lighthouse", "operation": "search"}, http.StatusUnprocessableEntity},
		{"expand without targets", map[string]any{"project": "lighthouse", "operation": "expand"}, http.StatusUnprocessableEntity},
		{"missing project", map[string]any{"operation": "search", "question": "x"}, http.StatusBadRequest},
		{"max_items over cap", map[string]any{"project": "lighthouse", "operation": "search", "question": "x", "max_items": 1000}, http.StatusUnprocessableEntity},
		{"max_depth over cap", map[string]any{"project": "lighthouse", "operation": "expand", "targets": []string{"K1"}, "max_depth": 9}, http.StatusUnprocessableEntity},
		{"bad as_of", map[string]any{"project": "lighthouse", "operation": "search", "question": "x", "as_of": "yesterday"}, http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			res, body := doJSON(t, handler, "POST", "/v1/observations/knowledge/explore", testCase.payload)
			if res.StatusCode != testCase.status {
				t.Fatalf("status = %d, want %d, body = %v", res.StatusCode, testCase.status, body)
			}
		})
	}
	res, _ := doJSON(t, handler, "POST", "/v1/observations/knowledge/explore", map[string]any{"project": "lighthouse", "operation": "search", "question": "x", "surprise": true})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown fields must be rejected, status = %d", res.StatusCode)
	}
}

func TestKnowledgeExploreAuthEnforced(t *testing.T) {
	handler := newAuthTestServer(t)
	// reader token of another project must not reach the engine.
	res, body := doJSONWithToken(t, handler, "POST", "/v1/observations/knowledge/explore", "read-token-1234567890", map[string]any{
		"project": "elsewhere", "operation": "search", "question": "миграции",
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-project explore status = %d, body = %v", res.StatusCode, body)
	}
	res, _ = doJSONWithToken(t, handler, "POST", "/v1/observations/knowledge/explore", "", map[string]any{
		"project": "lighthouse", "operation": "search", "question": "миграции",
	})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous explore status = %d", res.StatusCode)
	}
}
