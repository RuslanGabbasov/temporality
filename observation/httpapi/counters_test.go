package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEndpointCountersAccountByPattern(t *testing.T) {
	counters := newEndpointCounters()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /thing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /thing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	handler := counters.middleware(mux)

	for range 3 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/thing", nil))
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/thing", nil))
	// Unmatched route: pattern is empty, the request must land on "unmatched"
	// instead of vanishing from the books.
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/nowhere", nil))

	snapshot := counters.snapshot()
	routes := map[string]routeReport{}
	for _, row := range snapshot["routes"].([]routeReport) {
		routes[row.Route] = row
	}
	get := routes["GET /thing"]
	if get.Requests != 3 {
		t.Fatalf("GET /thing requests = %d, want 3", get.Requests)
	}
	post := routes["POST /thing"]
	if post.Requests != 1 || post.Errors5x != 1 {
		t.Fatalf("POST /thing = %+v, want 1 request, 1 5xx", post)
	}
	missing := routes["unmatched"]
	if missing.Requests != 1 {
		t.Fatalf("unmatched requests = %d, want 1", missing.Requests)
	}
	// The snapshot must serialize (the debug endpoint just encodes it).
	if _, err := json.Marshal(snapshot); err != nil {
		t.Fatalf("snapshot json: %v", err)
	}
}
