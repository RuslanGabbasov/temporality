package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeCDP is a scripted CDP backend: an HTTP /json/version endpoint plus a
// websocket speaking just enough of the protocol for the browser tool.
type fakeCDP struct {
	server *httptest.Server
	wsURL  string

	mu         sync.Mutex
	targets    int
	sessions   int
	closed     []string
	clicks     []map[string]any
	inserts    []string
	navigated  []string
	lastScript string
}

func newFakeCDP(t *testing.T) *fakeCDP {
	t.Helper()
	fake := &fakeCDP{}
	// One server serves both /json/version and the websocket (like a real
	// CDP backend); the advertised URL points back at this server.
	wsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseRead(context.Background())
		for {
			msgType, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			if msgType != websocket.MessageText {
				continue
			}
			var envelope cdpEnvelope
			if json.Unmarshal(data, &envelope) != nil || envelope.ID == 0 {
				continue
			}
			reply := fake.dispatch(envelope)
			encoded, _ := json.Marshal(reply)
			if err := conn.Write(r.Context(), websocket.MessageText, encoded); err != nil {
				return
			}
		}
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Browser":"FakeCDP/1.0","webSocketDebuggerUrl":"%s/"}`, fake.wsURL)
	})
	mux.Handle("/", wsHandler)
	server := httptest.NewServer(mux)
	fake.wsURL = server.URL
	fake.server = server
	t.Cleanup(server.Close)
	return fake
}

// dispatch answers one CDP command with scripted behavior per method.
func (f *fakeCDP) dispatch(envelope cdpEnvelope) cdpReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch envelope.Method {
	case "Target.createTarget":
		f.targets++
		return cdpReply{ID: envelope.ID, Result: json.RawMessage(fmt.Sprintf(`{"targetId":"T-%d"}`, f.targets))}
	case "Target.attachToTarget":
		f.sessions++
		return cdpReply{ID: envelope.ID, Result: json.RawMessage(fmt.Sprintf(`{"sessionId":"S-%d"}`, f.sessions))}
	case "Target.closeTarget":
		f.closed = append(f.closed, string(envelope.Params))
		return cdpReply{ID: envelope.ID, Result: json.RawMessage(`{"success":true}`)}
	case "Page.navigate":
		var params struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(envelope.Params, &params)
		f.navigated = append(f.navigated, params.URL)
		return cdpReply{ID: envelope.ID, Result: json.RawMessage(`{"frameId":"F1"}`)}
	case "Input.dispatchMouseEvent":
		var params map[string]any
		_ = json.Unmarshal(envelope.Params, &params)
		f.clicks = append(f.clicks, params)
		return cdpReply{ID: envelope.ID}
	case "Input.insertText":
		var params struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(envelope.Params, &params)
		f.inserts = append(f.inserts, params.Text)
		return cdpReply{ID: envelope.ID}
	case "Page.captureScreenshot":
		return cdpReply{ID: envelope.ID, Result: json.RawMessage(fmt.Sprintf(`{"data":%q}`, base64.StdEncoding.EncodeToString(testScreenshotPNG())))}
	case "Runtime.evaluate":
		var params struct {
			Expression string `json:"expression"`
		}
		_ = json.Unmarshal(envelope.Params, &params)
		f.lastScript = params.Expression
		return cdpReply{ID: envelope.ID, Result: json.RawMessage(fakeEval(params.Expression))}
	default:
		return cdpReply{ID: envelope.ID, Error: &cdpCallError{Code: -32601, Message: "method not implemented: " + envelope.Method}}
	}
}

// fakeEval answers Runtime.evaluate by pattern-matching the tool's own
// expressions, so the fake exercises the real parsing paths.
func fakeEval(expression string) string {
	switch {
	case strings.Contains(expression, "document.readyState"):
		return `{"result":{"type":"string","value":"{\"rs\":\"complete\",\"t\":\"Test Page\",\"h\":\"https://example.com/\"}"}}`
	case strings.Contains(expression, "outerHTML"):
		return `{"result":{"type":"string","value":"<html><head><title>Test Page</title></head><body><article><h1>Rendered</h1><p>JS-built content.</p></article></body></html>"}}`
	case strings.Contains(expression, "location.href"):
		return `{"result":{"type":"string","value":"https://example.com/"}}`
	case strings.Contains(expression, `"value" in el`):
		// The fill-focus script: focus + clear succeeded.
		return `{"result":{"type":"string","value":"ok"}}`
	case strings.Contains(expression, "querySelector"):
		if strings.Contains(expression, "missing-selector") {
			return `{"result":{"type":"object","value":null}}`
		}
		return `{"result":{"type":"string","value":"{\"x\":120,\"y\":40,\"w\":60,\"h\":20}"}}`
	case strings.Contains(expression, "throw new Error"):
		return `{"result":{"type":"object"},"exceptionDetails":{"text":"Uncaught","exception":{"description":"Error: boom"}}}`
	case strings.Contains(expression, "42"):
		return `{"result":{"type":"number","value":42}}`
	default:
		return `{"result":{"type":"string","value":"ok"}}`
	}
}

var testScreenshotOnce sync.Once
var testScreenshotData []byte

// testScreenshotPNG renders a deterministic 64x64 PNG once per test binary.
func testScreenshotPNG() []byte {
	testScreenshotOnce.Do(func() {
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				img.Set(x, y, color.RGBA{R: 0x20, G: 0x60, B: 0xc0, A: 0xff})
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			panic(err)
		}
		testScreenshotData = buf.Bytes()
	})
	return testScreenshotData
}

func browserTestActivities(t *testing.T, fake *fakeCDP) *Activities {
	t.Helper()
	// Point the engine at the fake's HTTP base; the ws path is discovered.
	base := strings.TrimPrefix(fake.server.URL, "http://")
	return &Activities{
		NetworkAccess: true,
		BrowserCDPURL: "http://" + base,
		BrowserPerMin: 0, // default budget
	}
}

func browserCall(a *Activities, runID string, args map[string]any) (ToolResult, error) {
	return a.handleBrowser(context.Background(), ToolRequest{
		RunID: runID, Name: "browser", Arguments: args,
	})
}

func TestBrowserOpenNavigatesAndWaits(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)

	result, err := browserCall(a, "run-1", map[string]any{"action": "open", "url": "https://example.com"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !strings.Contains(result.Content, "title: Test Page") || !strings.Contains(result.Content, "https://example.com/") {
		t.Fatalf("open must report the settled page:\n%s", result.Content)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.navigated) != 1 || fake.navigated[0] != "https://example.com" {
		t.Fatalf("navigate calls: %v", fake.navigated)
	}
}

func TestBrowserOpenReusesTabPerRun(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)
	for i := 0; i < 2; i++ {
		if _, err := browserCall(a, "run-1", map[string]any{"action": "open", "url": fmt.Sprintf("https://example.com/%d", i)}); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	if _, err := browserCall(a, "run-2", map[string]any{"action": "open", "url": "https://other.example"}); err != nil {
		t.Fatalf("open run-2: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.targets != 2 {
		t.Fatalf("expected one tab per run (2 total), got %d", fake.targets)
	}
}

func TestBrowserClickDispatchesAtElementCenter(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)
	if _, err := browserCall(a, "run-1", map[string]any{"action": "open", "url": "https://example.com"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	result, err := browserCall(a, "run-1", map[string]any{"action": "click", "selector": "a.more"})
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if !strings.Contains(result.Content, "clicked a.more") {
		t.Fatalf("click result: %s", result.Content)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.clicks) != 2 {
		t.Fatalf("click must press+release, got %d events", len(fake.clicks))
	}
	press, release := fake.clicks[0], fake.clicks[1]
	if press["type"] != "mousePressed" || release["type"] != "mouseReleased" {
		t.Fatalf("wrong event order: %+v %+v", press, release)
	}
	if press["x"].(float64) != 120 || press["y"].(float64) != 40 {
		t.Fatalf("click must target element center, got %v", press)
	}
}

func TestBrowserClickMissingElementFails(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)
	_, err := browserCall(a, "run-1", map[string]any{"action": "click", "selector": "missing-selector"})
	kind, _ := nonRetryableType(t, err)
	if kind != "ElementNotFound" {
		t.Fatalf("expected ElementNotFound, got %s", kind)
	}
}

func TestBrowserFillFocusesClearsAndInserts(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)
	result, err := browserCall(a, "run-1", map[string]any{"action": "fill", "selector": "input[name=q]", "text": "agent browsers"})
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if !strings.Contains(result.Content, "14 characters") {
		t.Fatalf("fill result: %s", result.Content)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.inserts) != 1 || fake.inserts[0] != "agent browsers" {
		t.Fatalf("inserts: %v", fake.inserts)
	}
	if !strings.Contains(fake.lastScript, "el.focus()") {
		t.Fatalf("fill must focus the element first, script: %s", fake.lastScript)
	}
}

func TestBrowserEvaluateReturnsValueAndExceptions(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)
	result, err := browserCall(a, "run-1", map[string]any{"action": "evaluate", "expression": "42"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !strings.Contains(result.Content, "42") {
		t.Fatalf("evaluate value: %s", result.Content)
	}
	thrown, err := browserCall(a, "run-1", map[string]any{"action": "evaluate", "expression": "throw new Error('boom')"})
	if err != nil {
		t.Fatalf("evaluate exception must not fail the tool: %v", err)
	}
	if !strings.Contains(thrown.Content, "exception") || !strings.Contains(thrown.Content, "boom") {
		t.Fatalf("exception report: %s", thrown.Content)
	}
}

func TestBrowserContentExtractsMarkdown(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)
	result, err := browserCall(a, "run-1", map[string]any{"action": "content"})
	if err != nil {
		t.Fatalf("content: %v", err)
	}
	if !strings.Contains(result.Content, "title: Test Page") || !strings.Contains(result.Content, "Rendered") {
		t.Fatalf("content must carry extracted markdown:\n%s", result.Content)
	}
}

func TestBrowserScreenshotAttachesVisionBlock(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)
	result, err := browserCall(a, "run-1", map[string]any{"action": "screenshot"})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if len(result.Images) != 1 || result.Images[0].MIMEType != "image/png" {
		t.Fatalf("screenshot must attach one png block, got %+v", result.Images)
	}
	if !strings.Contains(result.Content, "image/png 64x64") {
		t.Fatalf("screenshot meta: %s", result.Content)
	}
}

func TestBrowserCloseClosesTargetAndRegistry(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)
	if _, err := browserCall(a, "run-1", map[string]any{"action": "open", "url": "https://example.com"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	result, err := browserCall(a, "run-1", map[string]any{"action": "close"})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if result.Content != "tab closed" {
		t.Fatalf("close result: %s", result.Content)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.closed) != 1 {
		t.Fatalf("closeTarget calls: %v", fake.closed)
	}
	// Closing again is a no-op, not an error.
	if _, err := browserCall(a, "run-1", map[string]any{"action": "close"}); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestBrowserSweepsIdleTabs(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)
	if _, err := browserCall(a, "run-1", map[string]any{"action": "open", "url": "https://example.com"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	a.browserTabsMu.Lock()
	for _, tab := range a.browserTabs {
		tab.lastUsed = time.Now().Add(-2 * browserIdleTTL)
	}
	a.browserTabsMu.Unlock()
	if _, err := browserCall(a, "run-2", map[string]any{"action": "open", "url": "https://example.com"}); err != nil {
		t.Fatalf("open run-2: %v", err)
	}
	a.browserTabsMu.Lock()
	_, stale := a.browserTabs["run-1"]
	a.browserTabsMu.Unlock()
	if stale {
		t.Fatal("idle tab must be swept")
	}
}

func TestBrowserRateLimit(t *testing.T) {
	fake := newFakeCDP(t)
	a := browserTestActivities(t, fake)
	a.BrowserPerMin = 2
	for i := 0; i < 2; i++ {
		if _, err := browserCall(a, "run-1", map[string]any{"action": "evaluate", "expression": "1"}); err != nil {
			t.Fatalf("eval %d: %v", i, err)
		}
	}
	_, err := browserCall(a, "run-2", map[string]any{"action": "evaluate", "expression": "1"})
	kind, message := nonRetryableType(t, err)
	if kind != "BrowserRateLimited" || !strings.Contains(message, "retry") {
		t.Fatalf("expected BrowserRateLimited, got %s: %s", kind, message)
	}
}

func TestBrowserGates(t *testing.T) {
	fake := newFakeCDP(t)
	// No network capability: the tool refuses even though the backend exists.
	disabled := browserTestActivities(t, fake)
	disabled.NetworkAccess = false
	_, err := browserCall(disabled, "run-1", map[string]any{"action": "open", "url": "https://example.com"})
	kind, _ := nonRetryableType(t, err)
	if kind != "NetworkDisabled" {
		t.Fatalf("expected NetworkDisabled, got %s", kind)
	}
	// No backend configured: BrowserUnavailable.
	unconfigured := &Activities{NetworkAccess: true}
	_, err = browserCall(unconfigured, "run-1", map[string]any{"action": "open", "url": "https://example.com"})
	kind, _ = nonRetryableType(t, err)
	if kind != "BrowserUnavailable" {
		t.Fatalf("expected BrowserUnavailable, got %s", kind)
	}
	// Bad arguments: non-absolute URL, unknown action, missing selector.
	a := browserTestActivities(t, fake)
	_, err = browserCall(a, "run-1", map[string]any{"action": "open", "url": "ftp://nope"})
	kind, _ = nonRetryableType(t, err)
	if kind != "InvalidToolArguments" {
		t.Fatalf("ftp url must be rejected, got %s", kind)
	}
	_, err = browserCall(a, "run-1", map[string]any{"action": "teleport"})
	kind, _ = nonRetryableType(t, err)
	if kind != "InvalidToolArguments" {
		t.Fatalf("unknown action must be rejected, got %s", kind)
	}
	_, err = browserCall(a, "run-1", map[string]any{"action": "click"})
	kind, _ = nonRetryableType(t, err)
	if kind != "InvalidToolArguments" {
		t.Fatalf("missing selector must be rejected, got %s", kind)
	}
}
