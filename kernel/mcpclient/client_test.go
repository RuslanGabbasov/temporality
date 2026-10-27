package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/temporality-project/temporality/kernel/llm"
)

func TestToolAllowlistAndApprovalPolicy(t *testing.T) {
	c := &Client{
		tools:   map[string]string{"mcp__read_file": "read_file", "mcp__write_file": "write_file"},
		defs:    []llm.ToolDef{{Name: "mcp__read_file"}, {Name: "mcp__write_file"}},
		approve: map[string]bool{"mcp__write_file": true},
	}
	if !c.HasTool("mcp__read_file") || c.HasTool("mcp__exec") {
		t.Fatal("only advertised allowlisted MCP tools should be callable")
	}
	if c.RequiresApproval("mcp__read_file") || !c.RequiresApproval("mcp__write_file") {
		t.Fatal("approval policy did not distinguish read and write tools")
	}
	if got := c.ApprovalTools(); len(got) != 1 || got[0] != "mcp__write_file" {
		t.Fatalf("ApprovalTools() = %v", got)
	}
}

func TestStdioMCPServerDiscoveryAndCalls(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "calculator.go"), []byte("func Add(a,b int) int { return a+b }"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KERNEL_MCP_TEST_ROOT", root)
	t.Setenv("KERNEL_MCP_COMMAND", "go")
	args, _ := json.Marshal([]string{"run", "../../examples/test-mcp"})
	t.Setenv("KERNEL_MCP_ARGS", string(args))
	t.Setenv("KERNEL_MCP_ALLOW", "read_file,search,create_issue")
	t.Setenv("KERNEL_MCP_APPROVAL", "create_issue")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := FromEnv(ctx)
	if err != nil {
		t.Fatalf("discover stdio MCP server: %v", err)
	}
	if len(c.ToolDefs()) != 3 || !c.RequiresApproval("mcp__create_issue") {
		t.Fatalf("unexpected discovered tools/policy: %#v", c.ToolDefs())
	}
	got, err := c.Call(ctx, "mcp__read_file", map[string]any{"path": "calculator.go"}, "")
	if err != nil || !strings.Contains(got, "a+b") {
		t.Fatalf("read_file = %q, %v", got, err)
	}
	got, err = c.Call(ctx, "mcp__search", map[string]any{"query": "func Add"}, "")
	if err != nil || got != "calculator.go" {
		t.Fatalf("search = %q, %v", got, err)
	}
	if _, err := c.Call(ctx, "mcp__read_file", map[string]any{"path": "../outside"}, ""); err == nil {
		t.Fatal("workspace escape was accepted")
	}
	if _, err := c.Call(ctx, "mcp__not_allowlisted", nil, ""); err == nil {
		t.Fatal("non-allowlisted tool was callable")
	}
	first, err := c.Call(ctx, "mcp__create_issue", map[string]any{"title": "bug", "body": "repro"}, "run-1/turn/01/call-1")
	if err != nil {
		t.Fatalf("create_issue call: %v", err)
	}
	// Same idempotency key: the server returns the same issue number instead of
	// creating a duplicate — this is the contract retries rely on.
	retried, err := c.Call(ctx, "mcp__create_issue", map[string]any{"title": "bug", "body": "repro"}, "run-1/turn/01/call-1")
	if err != nil {
		t.Fatalf("create_issue retry: %v", err)
	}
	if !strings.Contains(first, "#1") || !strings.Contains(retried, "#1") || strings.Contains(retried, "#2") {
		t.Fatalf("idempotent retry diverged: first=%q retried=%q", first, retried)
	}
	// Different key: a distinct operation creates a distinct issue.
	second, err := c.Call(ctx, "mcp__create_issue", map[string]any{"title": "bug", "body": "repro"}, "run-1/turn/02/call-2")
	if err != nil {
		t.Fatalf("create_issue second call: %v", err)
	}
	if !strings.Contains(second, "#2") {
		t.Fatalf("distinct operations must not collapse: second=%q", second)
	}
	issues, err := os.ReadFile(filepath.Join(root, ".test-issues.log"))
	if err != nil {
		t.Fatalf("create_issue side effect missing: %v", err)
	}
	if got := strings.Count(string(issues), "---"); got != 2 {
		t.Fatalf("expected exactly two persisted issues, got %d", got)
	}
}

func TestApprovalNamesMustBeAllowlisted(t *testing.T) {
	if err := validateApprovalNames([]string{"read_file", "write_file"}, []string{"write_file"}); err != nil {
		t.Fatalf("valid approval policy rejected: %v", err)
	}
	if err := validateApprovalNames([]string{"read_file"}, []string{"write_file"}); err == nil {
		t.Fatal("approval-required tool outside allowlist was accepted")
	}
}

func TestToolResultIsBounded(t *testing.T) {
	got := contentText([]mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", maxResultBytes+20)}})
	if len(got) > maxResultBytes+40 || !strings.Contains(got, "truncated") {
		t.Fatalf("MCP result was not bounded: length=%d", len(got))
	}
}

// A Streamable HTTP server answers the legacy SSE GET with an event-stream
// that never carries SSE-protocol events and then dies. The failure must name
// the likely transport mismatch instead of a bare EOF.
func TestSSEAgainstStreamableServerHintsTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // close the stream mid-handshake
	}))
	defer server.Close()
	c := NewClient(ServerConfig{ID: "x", Name: "StreamableOnly", Type: TypeSSE, URL: server.URL + "/mcp", Enabled: true})
	err := c.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if !strings.Contains(err.Error(), "set the server type to http") {
		t.Fatalf("error should hint at the transport mismatch: %v", err)
	}
}

// A refused connection is a dead server, not a transport mismatch — no hint.
func TestSSEAgainstDeadServerHasNoHint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadURL := "http://" + listener.Addr().String() + "/mcp"
	listener.Close()
	c := NewClient(ServerConfig{ID: "x", Name: "Dead", Type: TypeSSE, URL: deadURL, Enabled: true})
	err = c.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if strings.Contains(err.Error(), "set the server type to http") {
		t.Fatalf("dead server must not produce a transport hint: %v", err)
	}
}

// fakeBackend stands in for a configured MCP server over in-memory transports
// and counts session connects, so tests can pin the lazy-redial behaviour.
type fakeBackend struct {
	connects int32 // accessed atomically

	mu    sync.Mutex
	peers []*mcp.ServerSession // server side of each session; Close kills the transport
}

// newFakeClient returns a Client whose sessions connect to an in-memory MCP
// server exposing one echo tool, plus the backend handle.
func newFakeClient(t *testing.T) (*Client, *fakeBackend) {
	t.Helper()
	backend := &fakeBackend{}
	c := NewClient(ServerConfig{ID: "test", Name: "Test", Type: TypeStdio, Enabled: true})
	c.connectFn = func(ctx context.Context) (*mcp.ClientSession, error) {
		atomic.AddInt32(&backend.connects, 1)
		serverTransport, clientTransport := mcp.NewInMemoryTransports()
		server := mcp.NewServer(&mcp.Implementation{Name: "fake-mcp", Version: "0.0.0"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
			Msg string `json:"msg,omitempty"`
		}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + in.Msg}}}, nil, nil
		})
		peer, err := server.Connect(ctx, serverTransport, nil)
		if err != nil {
			return nil, err
		}
		backend.mu.Lock()
		backend.peers = append(backend.peers, peer)
		backend.mu.Unlock()
		client := mcp.NewClient(&mcp.Implementation{Name: "kernel-test", Version: "0.0.0"}, nil)
		return client.Connect(ctx, clientTransport, nil)
	}
	return c, backend
}

func (f *fakeBackend) count() int32 { return atomic.LoadInt32(&f.connects) }

// killLast closes the server side of the newest session, simulating a server
// whose process died between calls.
func (f *fakeBackend) killLast() {
	f.mu.Lock()
	defer f.mu.Unlock()
	peer := f.peers[len(f.peers)-1]
	f.peers = f.peers[:len(f.peers)-1]
	_ = peer.Close()
}

func TestPersistentSessionReusedAcrossCalls(t *testing.T) {
	c, backend := newFakeClient(t)
	ctx := context.Background()
	if err := c.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got, err := c.Call(ctx, "mcp__test__echo", map[string]any{"msg": "one"}, "")
	if err != nil || got != "echo:one" {
		t.Fatalf("first call = %q, %v", got, err)
	}
	got, err = c.Call(ctx, "mcp__test__echo", map[string]any{"msg": "two"}, "")
	if err != nil || got != "echo:two" {
		t.Fatalf("second call = %q, %v", got, err)
	}
	if n := backend.count(); n != 1 {
		t.Fatalf("two calls and a refresh must share one session, got %d connects", n)
	}
	// Refresh rides the same session instead of opening a second one.
	if err := c.Refresh(ctx); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if n := backend.count(); n != 1 {
		t.Fatalf("refresh must not create a session when one is alive, got %d connects", n)
	}
}

func TestBrokenTransportRedialsOnceAndRetries(t *testing.T) {
	c, backend := newFakeClient(t)
	ctx := context.Background()
	if err := c.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := c.Call(ctx, "mcp__test__echo", map[string]any{"msg": "alive"}, ""); err != nil {
		t.Fatalf("call before crash: %v", err)
	}
	backend.killLast() // the server process died between calls
	got, err := c.Call(ctx, "mcp__test__echo", map[string]any{"msg": "back"}, "")
	if err != nil || got != "echo:back" {
		t.Fatalf("call after crash = %q, %v", got, err)
	}
	if n := backend.count(); n != 2 {
		t.Fatalf("dead session must be redialed exactly once, got %d connects", n)
	}
	// The replacement session is used for subsequent calls without redialing.
	if _, err := c.Call(ctx, "mcp__test__echo", map[string]any{"msg": "again"}, ""); err != nil {
		t.Fatalf("call on redialed session: %v", err)
	}
	if n := backend.count(); n != 2 {
		t.Fatalf("healthy session must not be redialed, got %d connects", n)
	}
}

func TestConcurrentCallsShareSession(t *testing.T) {
	c, backend := newFakeClient(t)
	ctx := context.Background()
	if err := c.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	const workers, calls = 8, 4
	var wg sync.WaitGroup
	errs := make([]error, workers*calls)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < calls; i++ {
				_, err := c.Call(ctx, "mcp__test__echo", map[string]any{"msg": "w"}, "")
				errs[w*calls+i] = err
			}
		}(w)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent call %d: %v", i, err)
		}
	}
	if n := backend.count(); n != 1 {
		t.Fatalf("concurrent calls must share one session, got %d connects", n)
	}
}

func TestCloseIsIdempotentAndReconnects(t *testing.T) {
	c, backend := newFakeClient(t)
	ctx := context.Background()
	if err := c.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second close must be a no-op, got %v", err)
	}
	if _, err := c.Call(ctx, "mcp__test__echo", map[string]any{"msg": "back"}, ""); err != nil {
		t.Fatalf("call after close must lazily reconnect: %v", err)
	}
	if n := backend.count(); n != 2 {
		t.Fatalf("expected exactly one reconnect after close, got %d connects", n)
	}
}

func TestIsBrokenTransport(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadline", context.DeadlineExceeded, false},
		{"canceled", context.Canceled, false},
		{"wrapped deadline", fmt.Errorf("calling \"tools/call\": %w", context.DeadlineExceeded), false},
		{"sdk connection closed", fmt.Errorf("calling \"tools/call\": %w", mcp.ErrConnectionClosed), true},
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"closed pipe", io.ErrClosedPipe, true},
		{"os closed", os.ErrClosed, true},
		{"session closed text", errors.New("session closed by peer"), true},
		{"closed pipe text", errors.New("write |1: broken pipe"), true},
		{"plain failure", errors.New("tool exploded"), false},
	}
	for _, tc := range cases {
		if got := isBrokenTransport(tc.err); got != tc.want {
			t.Errorf("%s: isBrokenTransport(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
