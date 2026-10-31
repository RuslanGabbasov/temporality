package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The pinned-routing silent retry: transport failures (the kernel's own
// :8090 listener down during a stack restart while the Temporal worker
// already polls) must bridge inside the activity — no journal markers, no
// chat noise. A refused connection turns into a routed call once the
// listener is back; a dead endpoint fails with the plain routing error.

func TestPinnedRoutingRetriesTransportErrorsSilently(t *testing.T) {
	var lookups atomic.Int32
	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lookups.Add(1) <= 2 {
			// Two refused windows before the listener is back: hijack and drop
			// the connection — a transport-level failure, not an HTTP answer.
			hijack, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("hijack unsupported")
			}
			conn, _, _ := hijack.Hijack()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider_id":"prov-1","provider_name":"Mimo","base_url":"http://provider","api_key":"k","model":"mimo-v2.6-pro"}`))
	}))
	defer workspace.Close()

	activities := &Activities{HTTP: &http.Client{Timeout: 2 * time.Second}, WorkspaceURL: workspace.URL}
	config, name, err := activities.modelConfigForPinned(context.Background(), "proj-1", "prov-1", "mimo-v2.6-pro")
	if err != nil {
		t.Fatalf("the retry must bridge a transient refusal: %v", err)
	}
	if config.BaseURL != "http://provider" || name != "Mimo" {
		t.Fatalf("the routed config must carry the provider answer, got %+v %q", config, name)
	}
	if lookups.Load() != 3 {
		t.Fatalf("two failures then one success expected, got %d lookups", lookups.Load())
	}
}

func TestPinnedRoutingDefinitiveAnswersDoNotRetry(t *testing.T) {
	var lookups atomic.Int32
	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		http.Error(w, "disabled", http.StatusConflict)
	}))
	defer workspace.Close()

	activities := &Activities{HTTP: &http.Client{}, WorkspaceURL: workspace.URL}
	_, _, err := activities.modelConfigForPinned(context.Background(), "proj-1", "prov-1", "mimo-v2.6-pro")
	if err == nil {
		t.Fatal("a disabled provider must fail loudly")
	}
	if !strings.Contains(err.Error(), "disabled — enable it in Providers") {
		t.Fatalf("the actionable answer must surface verbatim, got %v", err)
	}
	if lookups.Load() != 1 {
		t.Fatalf("a definitive HTTP answer must not be retried, got %d lookups", lookups.Load())
	}
}

func TestPinnedRoutingDeadEndpointFailsInsideWindow(t *testing.T) {
	// A listener that never comes back: the retry window (30s budget, 2s
	// backoff) must bound the wait instead of hanging the model call forever.
	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijack, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijack unsupported")
		}
		conn, _, _ := hijack.Hijack()
		_ = conn.Close() // refuse: close without an answer
	}))
	defer workspace.Close()

	activities := &Activities{HTTP: &http.Client{Timeout: 2 * time.Second}, WorkspaceURL: workspace.URL}
	_, _, err := activities.modelConfigForPinned(context.Background(), "proj-1", "prov-1", "m")
	if err == nil {
		t.Fatal("a dead endpoint must surface the routing failure")
	}
	if !strings.Contains(err.Error(), "routing failed") {
		t.Fatalf("the plain routing error must surface, got %v", err)
	}
}

func TestIsTransportError(t *testing.T) {
	if !isTransportError(context.DeadlineExceeded) && !isTransportError(errors.New("dial tcp: connect: connection refused")) {
		t.Fatal("deadline exceeded / connection refused are transport errors")
	}
	if isTransportError(nil) {
		t.Fatal("nil is not transport")
	}
}
