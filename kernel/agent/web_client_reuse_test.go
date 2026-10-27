package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// countingListener counts accepted TCP connections, so a test can assert that
// the shared web client keeps connections alive across calls.
type countingListener struct {
	net.Listener

	mu       sync.Mutex
	accepted int
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.mu.Lock()
		l.accepted++
		l.mu.Unlock()
	}
	return conn, err
}

func (l *countingListener) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.accepted
}

// newCountingServer serves handler over a connection-counting listener.
func newCountingServer(t *testing.T, handler http.Handler) (*httptest.Server, *countingListener) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingListener{Listener: listener}
	server := &httptest.Server{Listener: counting, Config: &http.Server{Handler: handler}}
	server.Start()
	t.Cleanup(server.Close)
	return server, counting
}

// Both tools must share one process-wide client: a second fetch over the same
// host reuses the pooled TCP connection (keep-alive) instead of dialing again.
func TestWebFetchReusesClientConnection(t *testing.T) {
	webCacheReset(t)
	server, listener := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "keep-alive body "+r.URL.Path)
	}))

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	for i := 0; i < 2; i++ {
		result, err := a.handleWebFetch(context.Background(), ToolRequest{
			Arguments: map[string]any{"url": fmt.Sprintf("%s/page-%d", server.URL, i)},
		})
		if err != nil {
			t.Fatalf("fetch %d: %v", i+1, err)
		}
		if !strings.Contains(result.Content, fmt.Sprintf("page-%d", i)) {
			t.Fatalf("fetch %d: unexpected body:\n%s", i+1, result.Content)
		}
	}
	if got := listener.count(); got != 1 {
		t.Fatalf("expected 1 TCP connection across fetches (keep-alive), got %d", got)
	}
}

func TestFetchImageReusesClientConnection(t *testing.T) {
	payload := encodePNGForTest(t, solidImage(16, 16))
	server, listener := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(payload)
	}))

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	for i := 0; i < 2; i++ {
		if _, err := a.handleFetchImage(context.Background(), ToolRequest{
			Arguments: map[string]any{"url": server.URL + "/pic.png"},
		}); err != nil {
			t.Fatalf("fetch_image %d: %v", i+1, err)
		}
	}
	if got := listener.count(); got != 1 {
		t.Fatalf("expected 1 TCP connection across fetches (keep-alive), got %d", got)
	}
}

// fetch_image rides the same shared client as web_fetch: the second tool call
// reuses the connection the first one pooled.
func TestWebFetchAndFetchImageShareClient(t *testing.T) {
	webCacheReset(t)
	payload := encodePNGForTest(t, solidImage(16, 16))
	server, listener := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/pic") {
			w.Header().Set("Content-Type", "image/png")
			w.Write(payload)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "page body")
	}))

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	if _, err := a.handleWebFetch(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": server.URL + "/page"},
	}); err != nil {
		t.Fatalf("web_fetch: %v", err)
	}
	if _, err := a.handleFetchImage(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": server.URL + "/pic.png"},
	}); err != nil {
		t.Fatalf("fetch_image: %v", err)
	}
	if got := listener.count(); got != 1 {
		t.Fatalf("expected 1 TCP connection shared by both tools, got %d", got)
	}
}
