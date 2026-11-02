package mcpclient

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Registry reconcile semantics: the periodic Apply must refresh the LIVE
// client in place (the old create-and-close churn broke in-flight calls every
// minute and made the tool catalog blink), failed refreshes keep the stale
// tool set, and only a genuinely changed config may retire the old client.

// breakable wraps the in-memory fake with a failure switch, so a test can
// simulate the server going away for good.
func breakable(t *testing.T) (*Client, *fakeBackend, *atomic.Bool) {
	t.Helper()
	c, backend := newFakeClient(t)
	// The fake dials in-memory transports, but the config must still satisfy
	// Validate for Apply's reconciliation paths.
	c.cfg.Command = "fake-mcp"
	broken := &atomic.Bool{}
	original := c.connectFn
	c.connectFn = func(ctx context.Context) (*mcp.ClientSession, error) {
		if broken.Load() {
			return nil, errors.New("connection refused")
		}
		return original(ctx)
	}
	return c, backend, broken
}

func TestApplySameConfigRefreshesInPlaceWithoutSessionChurn(t *testing.T) {
	c, backend, _ := breakable(t)
	cfg := c.cfg
	r := NewRegistry()
	r.mu.Lock()
	r.servers[cfg.ID] = c
	r.mu.Unlock()
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("seed refresh: %v", err)
	}
	if n := backend.count(); n != 1 {
		t.Fatalf("seed connects = %d, want 1", n)
	}

	// Same config, tick after tick: the client instance and its session stay.
	for i := 0; i < 3; i++ {
		if err := r.Apply(context.Background(), cfg); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}
	if n := backend.count(); n != 1 {
		t.Fatalf("reconcile ticks must ride the persistent session, got %d connects", n)
	}
	if got := r.ServerToolCount(cfg.ID); got != 1 {
		t.Fatalf("tool count = %d, want 1", got)
	}
}

func TestApplyFailureKeepsStaleTools(t *testing.T) {
	c, backend, broken := breakable(t)
	cfg := c.cfg
	r := NewRegistry()
	r.mu.Lock()
	r.servers[cfg.ID] = c
	r.mu.Unlock()
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("seed refresh: %v", err)
	}

	// The server dies under the registry: the live session breaks and the
	// redial fails — the reconcile errors but the tool set stays, a transient
	// outage must not silently strip the schema.
	broken.Store(true)
	backend.killLast()
	if err := r.Apply(context.Background(), cfg); err == nil {
		t.Fatal("refresh over a dead server must fail")
	}
	if got := r.ServerToolCount(cfg.ID); got != 1 {
		t.Fatalf("failed refresh must keep the stale tools, got %d", got)
	}
}

func TestApplyChangedConfigFailureRemovesStale(t *testing.T) {
	c, _, _ := breakable(t)
	cfg := c.cfg
	r := NewRegistry()
	r.mu.Lock()
	r.servers[cfg.ID] = c
	r.mu.Unlock()
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("seed refresh: %v", err)
	}

	// A DIFFERENT config that cannot connect is a deliberate re-pointing that
	// failed: the old tools describe a server that no longer exists.
	changed := cfg
	changed.Type = TypeHTTP
	changed.URL = "http://127.0.0.1:1/mcp"
	if err := r.Apply(context.Background(), changed); err == nil {
		t.Fatal("the new endpoint must fail to connect")
	}
	if got := r.ServerToolCount(cfg.ID); got != 0 {
		t.Fatalf("changed-config failure must retire the stale tools, got %d", got)
	}
}

func TestServerToolCountUnknownServerIsZero(t *testing.T) {
	r := NewRegistry()
	if got := r.ServerToolCount("nope"); got != 0 {
		t.Fatalf("unknown server count = %d, want 0", got)
	}
	var nilRegistry *Registry
	if got := nilRegistry.ServerToolCount("nope"); got != 0 {
		t.Fatalf("nil registry count = %d, want 0", got)
	}
}

// A server that accepts the connection and never answers initialize must
// fail its own dial — one hung dial used to stall the reconcile loop and
// bury every other server behind it.
func TestConnectTimesOutOnSilentServer(t *testing.T) {
	defer func(prev time.Duration) { mcpDialTimeout = prev }(mcpDialTimeout)
	mcpDialTimeout = 300 * time.Millisecond
	c := NewClient(ServerConfig{ID: "mute", Name: "Mute", Type: TypeStdio, Command: "sh", Args: []string{"-c", "cat >/dev/null"}, Enabled: true})
	done := make(chan error, 1)
	go func() {
		_, err := c.connect(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no initialize response") {
			t.Fatalf("a silent server must fail its own dial, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a silent server must not stall the caller")
	}
}
