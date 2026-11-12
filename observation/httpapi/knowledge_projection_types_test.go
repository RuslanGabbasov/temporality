package httpapi

import (
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// The scan allow-list and the projection's consumed vocabulary must move
// together: visibleKnowledgeEvents filters the store reads by
// knowledgeProjectionTypes, so a consumed type missing from the list is
// silently dropped from every projection. The inventory below mirrors the
// type switch in observation/knowledge.go (applyHint, applyTransition,
// relation handling) — update both when ProjectKnowledge learns a new type.
func TestKnowledgeProjectionTypesMatchConsumedVocabulary(t *testing.T) {
	want := []string{
		"hint.ignored", "hint.offered", "hint.outcome", "hint.used",
		"knowledge.challenged", "knowledge.confirmed", "knowledge.corrected",
		"knowledge.disproved", "knowledge.invalidated", "knowledge.linked",
		"knowledge.promoted", "knowledge.proposed", "knowledge.relation_confirmed",
		"knowledge.relation_dismissed", "knowledge.superseded", "knowledge.used",
	}
	got := append([]string(nil), knowledgeProjectionTypes...)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("knowledgeProjectionTypes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("knowledgeProjectionTypes = %v, want %v", got, want)
		}
	}
	// The invalidation heuristic must cover every scanned type, or the cache
	// would go stale while the scan sees the change.
	for _, eventType := range want {
		if !affectsKnowledgeProjection(eventType) {
			t.Fatalf("affectsKnowledgeProjection(%q) = false, want true: the scan reads it", eventType)
		}
	}
	// Types the projection must not pay for — the reason the allow-list
	// exists (a cold scan walked whole model.text_delta streams without it).
	for _, eventType := range []string{"run.started", "tool.called", "model.text_delta", "hint.query", "hint.delivery", "knowledge.extraction.started", "knowledge.extraction.completed"} {
		for _, typ := range knowledgeProjectionTypes {
			if typ == eventType {
				t.Fatalf("knowledgeProjectionTypes must not contain %q", eventType)
			}
		}
		if affectsKnowledgeProjection(eventType) {
			t.Fatalf("affectsKnowledgeProjection(%q) = true, want false", eventType)
		}
	}
}

// End-to-end tripwire for the allow-list: the listing must project the full
// lifecycle vocabulary even when the event stream is dominated by telemetry
// noise. A consumed type missing from knowledgeProjectionTypes shows up here
// as a lost state transition or a zeroed usage counter.
func TestKnowledgeProjectionScanKeepsVocabularyAmongNoise(t *testing.T) {
	handler := newTestServer(t)
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	events := []observation.Event{
		noiseEvent("run.started", "evt-n1", base),
		noiseEvent("model.text_delta", "evt-n2", base.Add(1*time.Second)),
		noiseEvent("tool.called", "evt-n3", base.Add(2*time.Second)),
		noiseEvent("hint.query", "evt-n4", base.Add(3*time.Second)),
		noiseEvent("knowledge.extraction.started", "evt-n5", base.Add(4*time.Second)),
		proposedEvent("lighthouse", "evt-k1", "K1", "gatekeeper v2 authenticates via .token-file"),
		lifecycleEvent("hint.offered", "evt-h1", base.Add(5*time.Second), map[string]any{"hint_id": "h1", "knowledge_id": "K1"}),
		lifecycleEvent("hint.used", "evt-h2", base.Add(6*time.Second), map[string]any{"hint_id": "h1", "knowledge_id": "K1"}),
		lifecycleEvent("knowledge.confirmed", "evt-k2", base.Add(7*time.Second), map[string]any{"knowledge_id": "K1"}),
	}
	if res, body := doJSON(t, handler, "POST", "/v1/observations/events", map[string]any{"events": events}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	res, body := doJSON(t, handler, "GET", "/v1/observations/knowledge?project=lighthouse", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, body = %v", res.StatusCode, body)
	}
	requireIDs(t, body, "K1")
	item := body["knowledge"].([]any)[0].(map[string]any)
	if item["state"] != "confirmed" {
		t.Fatalf("state = %v, want confirmed (knowledge.confirmed must survive the scan)", item["state"])
	}
	for field, want := range map[string]float64{"hint_offers": 1, "hint_uses": 1, "reuse_count": 1} {
		if got, _ := item[field].(float64); got != want {
			t.Fatalf("%s = %v, want %v (hint telemetry must survive the scan)", field, item[field], want)
		}
	}
}

func noiseEvent(eventType, eventID string, at time.Time) observation.Event {
	return observation.Event{
		Schema: observation.Schema, EventID: eventID, OccurredAt: at,
		Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    eventType, Data: map[string]any{"status": "noise"},
	}
}

func lifecycleEvent(eventType, eventID string, at time.Time, data map[string]any) observation.Event {
	return observation.Event{
		Schema: observation.Schema, EventID: eventID, OccurredAt: at,
		Source:  observation.Source{ID: "kernel-test", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{Project: "lighthouse", Run: "run-1"},
		Type:    eventType, Data: data,
	}
}
