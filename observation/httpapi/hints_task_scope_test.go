package httpapi

import (
	"net/http"
	"testing"

	"github.com/temporality-project/temporality/observation"
)

// primingCount starts a run for task and returns how many hints the start of
// that run was offered.
func primingCount(t *testing.T, s http.Handler, task string) float64 {
	t.Helper()
	res, body := doJSON(t, s, http.MethodPost, "/v1/observations/hints", map[string]any{
		"project": "lighthouse", "query": "how do I authenticate to gatekeeper v2",
		"task": task, "trigger": "task.priming",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hints status = %d, body = %v", res.StatusCode, body)
	}
	count, ok := body["count"].(float64)
	if !ok {
		t.Fatalf("count missing: %v", body)
	}
	return count
}

// A chat task starts one run per message, and the run-scoped issued list dies
// with its run — so every turn re-asked for the same top of the ranking and
// the newest request got nothing fresh (docs/plan-priming-relevance.md §7).
func TestPrimingSuppressesKnowledgeAlreadyOfferedToTheTask(t *testing.T) {
	s := newTestServer(t)
	if res, body := doJSON(t, s, http.MethodPost, "/v1/observations/events", map[string]any{
		"events": []observation.Event{proposedEvent("lighthouse", "evt-task-k1", "K-task", "gatekeeper v2 authenticates via token file")},
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}

	if got := primingCount(t, s, "task-chat"); got != 1 {
		t.Fatalf("first priming offered %v hints, want 1", got)
	}
	if got := primingCount(t, s, "task-chat"); got != 0 {
		t.Fatalf("second priming of the same task offered %v hints, want 0 (already offered)", got)
	}
	// The suppression is task-scoped: another conversation of the same project
	// still sees the knowledge.
	if got := primingCount(t, s, "task-other"); got != 1 {
		t.Fatalf("priming of a fresh task offered %v hints, want 1", got)
	}
}

// The task history only feeds the run start. A dynamic activation inside the
// conversation keeps answering from its own run-scoped issued list, so an
// already-offered knowledge can still be re-confirmed for a tool failure that
// explicitly asks for it.
func TestTaskHistoryDoesNotBlankDynamicActivations(t *testing.T) {
	s := newTestServer(t)
	if res, body := doJSON(t, s, http.MethodPost, "/v1/observations/events", map[string]any{
		"events": []observation.Event{proposedEvent("lighthouse", "evt-dyn-k1", "K-dyn", "gatekeeper v2 authenticates via token file")},
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %v", res.StatusCode, body)
	}
	if got := primingCount(t, s, "task-dyn"); got != 1 {
		t.Fatalf("first priming offered %v hints, want 1", got)
	}

	res, body := doJSON(t, s, http.MethodPost, "/v1/observations/hints", map[string]any{
		"project": "lighthouse", "query": "how do I authenticate to gatekeeper v2",
		"task": "task-dyn", "trigger": "tool.failed", "tool": "mcp__kubectl__apply",
		"tool_result": "error: gatekeeper v2 rejected the token file",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("runtime hints status = %d, body = %v", res.StatusCode, body)
	}
	if count := body["count"].(float64); count != 1 {
		t.Fatalf("dynamic activation offered %v hints, want 1 (task history is priming-only)", count)
	}
}
