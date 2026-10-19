package httpapi

import (
	"context"
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

func TestGateMiddlewareCountsRejectionsOnly(t *testing.T) {
	counters := newEndpointCounters()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /thing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	// A gate stub: 401 for /denied, pass-through otherwise — mimics the real
	// Authenticate, which wraps the request (WithContext) before the inner
	// counters+mux chain ever sees it.
	inner := counters.middleware(mux)
	gate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/denied" {
			w.WriteHeader(401)
			return
		}
		r = r.WithContext(context.Background()) // like Gate.Authenticate
		inner.ServeHTTP(w, r)
	})
	chain := counters.gateMiddleware(gate)

	chain.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/thing", nil))
	chain.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/denied", nil))

	rows := map[string]routeReport{}
	for _, row := range counters.snapshot()["routes"].([]routeReport) {
		rows[row.Route] = row
	}
	if rows["GET /thing"].Requests != 1 {
		t.Fatalf("inner route accounting lost through the gate copy: %+v", rows["GET /thing"])
	}
	rejected := rows["gate-rejected"]
	if rejected.Requests != 1 || rejected.Errors4x != 1 {
		t.Fatalf("gate-rejected = %+v, want 1 request, 1 4xx", rejected)
	}
}
