package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.temporal.io/sdk/temporal"

	"github.com/temporality-project/temporality/kernel/llm"
)

// Headless-browser tool (docs/web-research.md §4, phase 3). The contract is a
// Playwright-flavoured subset (open/click/fill/evaluate/content/screenshot/
// close) executed over CDP, so any CDP-compatible backend works — Lightpanda
// is the default (light, fast), a Playwright server or Chrome can stand in.
// The browser runs in its own container on an isolated network: it can reach
// the internet but not the kernel, journal or database, which is the network
// level SSRF answer for a browser that fetches on its own.

const (
	// browserActionTimeout bounds one browser action end-to-end (navigate,
	// evaluate, click). JS-heavy pages get browserReadyTimeout to settle.
	browserActionTimeout = 45 * time.Second
	// browserReadyTimeout is how long open() waits for document.readyState
	// to reach "complete" before returning with whatever rendered.
	browserReadyTimeout = 15 * time.Second
	// browserIdleTTL auto-closes tabs whose run stopped mid-work (failed,
	// cancelled, or just forgot close): the sweep runs inline on every call.
	browserIdleTTL = 10 * time.Minute
	// browserMaxTabs bounds the process-wide tab registry; lightpanda itself
	// caps browser connections at 16, so stay under that.
	browserMaxTabs = 12
	// browserEvalCap bounds the evaluate/content payload returned to the model.
	browserEvalCap = webFetchDefaultOutput
)

var browserTool = llm.ToolDef{
	Name:        "browser",
	Description: "Drive a headless browser for JS-rendered pages and interaction: open a URL (waits for load), click a CSS selector, fill an input, evaluate JavaScript (JSON result), extract the rendered page as markdown, take a screenshot (attached as a vision block), or close the tab. One tab per run persists across calls — state (cookies of the session, JS globals) survives until close. Prefer web_fetch for static pages: this tool is for content that needs real rendering.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":     map[string]any{"type": "string", "enum": []string{"open", "click", "fill", "evaluate", "content", "screenshot", "close"}, "description": "What to do"},
			"url":        map[string]any{"type": "string", "description": "open: absolute http(s) URL to navigate to"},
			"selector":   map[string]any{"type": "string", "description": "click/fill: CSS selector of the element"},
			"text":       map[string]any{"type": "string", "description": "fill: text to type into the focused element"},
			"expression": map[string]any{"type": "string", "description": "evaluate: JavaScript expression, must be JSON-serializable (returnByValue)"},
		},
		"required": []string{"action"},
	},
}

// browserTab is one run's tab: a CDP target plus its flattened session.
type browserTab struct {
	targetID  string
	sessionID string
	lastUsed  time.Time
}

// cdpEnvelope is one CDP frame; sessionId routes commands to a tab.
type cdpEnvelope struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type cdpReply struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *cdpCallError   `json:"error,omitempty"`
}

type cdpCallError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpCallError) Error() string { return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message) }

// browserEngine is the kernel-process CDP connection to the browser backend.
// One websocket carries every run's commands (ids route replies); the dial is
// lazy and re-established after failures.
type browserEngine struct {
	base   string // configured http(s) base, e.g. http://lightpanda:9222
	wsPath string // discovered debugger path from /json/version, e.g. "/"

	mu      sync.Mutex // guards conn/nextID/pending lifecycle
	conn    *websocket.Conn
	nextID  int64
	pending map[int64]chan cdpReply
}

func newBrowserEngine(base string) *browserEngine {
	return &browserEngine{base: strings.TrimRight(base, "/"), pending: map[int64]chan cdpReply{}}
}

// discover fetches /json/version and keeps only the debugger path: the
// advertised absolute URL names the container-internal interface, which is
// unreachable from the kernel, so the configured base wins.
func (e *browserEngine) discover(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+"/json/version", nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("browser backend unreachable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return "", fmt.Errorf("browser backend /json/version: HTTP %d", response.StatusCode)
	}
	var version struct {
		WsURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&version); err != nil {
		return "", fmt.Errorf("decode /json/version: %w", err)
	}
	parsed, err := url.Parse(version.WsURL)
	if err != nil || parsed.Path == "" {
		// Backend without a discoverable path: dial the base as-is.
		return "/", nil
	}
	return parsed.Path, nil
}

// ensure returns a live connection, dialing or redialing as needed.
func (e *browserEngine) ensure(ctx context.Context) (*websocket.Conn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn != nil {
		return e.conn, nil
	}
	if e.wsPath == "" {
		path, err := e.discover(ctx)
		if err != nil {
			return nil, err
		}
		e.wsPath = path
	}
	wsBase := e.base
	if strings.HasPrefix(wsBase, "https://") {
		wsBase = "wss://" + strings.TrimPrefix(wsBase, "https://")
	} else {
		wsBase = "ws://" + strings.TrimPrefix(wsBase, "http://")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsBase+e.wsPath, nil)
	if err != nil {
		e.wsPath = "" // rediscover next time; the backend may have restarted
		return nil, fmt.Errorf("dial browser backend: %w", err)
	}
	e.conn = conn
	// Screenshots arrive as multi-hundred-KB frames; the library default
	// read limit (32 KiB) would kill the connection mid-message.
	conn.SetReadLimit(8 << 20)
	go e.readLoop(conn)
	return conn, nil
}

// readLoop dispatches replies to waiting callers until the connection dies.
func (e *browserEngine) readLoop(conn *websocket.Conn) {
	for {
		_, data, err := conn.Read(context.Background())
		if err != nil {
			e.mu.Lock()
			if e.conn == conn {
				e.conn = nil
			}
			pending := e.pending
			e.pending = map[int64]chan cdpReply{}
			e.mu.Unlock()
			for _, ch := range pending {
				select {
				case ch <- cdpReply{Error: &cdpCallError{Message: "browser connection lost"}}:
				default:
				}
			}
			conn.Close(websocket.StatusInternalError, "read loop ended")
			return
		}
		var reply cdpReply
		if err := json.Unmarshal(data, &reply); err != nil || reply.ID == 0 {
			continue // events are ignored: v1 polls state instead of subscribing
		}
		e.mu.Lock()
		ch := e.pending[reply.ID]
		delete(e.pending, reply.ID)
		e.mu.Unlock()
		if ch != nil {
			ch <- reply
		}
	}
}

// call sends one CDP command and awaits its reply.
func (e *browserEngine) call(ctx context.Context, method string, params any, sessionID string) (json.RawMessage, error) {
	conn, err := e.ensure(ctx)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.nextID++
	id := e.nextID
	ch := make(chan cdpReply, 1)
	e.pending[id] = ch
	e.mu.Unlock()

	envelope := cdpEnvelope{ID: id, Method: method, Params: encoded, SessionID: sessionID}
	frame, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, frame); err != nil {
		// A wedged write kills the connection; the next call redials.
		e.drop(conn)
		return nil, fmt.Errorf("send %s: %w", method, err)
	}
	select {
	case reply := <-ch:
		if reply.Error != nil {
			return nil, reply.Error
		}
		return reply.Result, nil
	case <-ctx.Done():
		e.mu.Lock()
		delete(e.pending, id)
		e.mu.Unlock()
		return nil, ctx.Err()
	}
}

// drop retires a broken connection and fails its waiters.
func (e *browserEngine) drop(conn *websocket.Conn) {
	e.mu.Lock()
	if e.conn == conn {
		e.conn = nil
	}
	e.mu.Unlock()
	conn.Close(websocket.StatusInternalError, "dropped")
}

// browserTabFor returns the run's tab, creating one when missing.
func (a *Activities) browserTabFor(ctx context.Context, runID, startURL string) (*browserTab, error) {
	a.browserTabsMu.Lock()
	defer a.browserTabsMu.Unlock()
	a.browserSweepLocked(ctx)
	if tab, ok := a.browserTabs[runID]; ok {
		tab.lastUsed = time.Now()
		return tab, nil
	}
	if len(a.browserTabs) >= browserMaxTabs {
		return nil, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("browser tab budget exhausted (%d tabs); close finished runs first", browserMaxTabs), "BrowserTabBudget", nil)
	}
	created, err := a.browserEngine().call(ctx, "Target.createTarget", map[string]any{"url": startURL}, "")
	if err != nil {
		return nil, fmt.Errorf("create tab: %w", err)
	}
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(created, &target); err != nil || target.TargetID == "" {
		return nil, fmt.Errorf("create tab: unexpected reply %s", string(created))
	}
	attached, err := a.browserEngine().call(ctx, "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, "")
	if err != nil {
		return nil, fmt.Errorf("attach tab: %w", err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(attached, &session); err != nil || session.SessionID == "" {
		return nil, fmt.Errorf("attach tab: unexpected reply %s", string(attached))
	}
	tab := &browserTab{targetID: target.TargetID, sessionID: session.SessionID, lastUsed: time.Now()}
	if a.browserTabs == nil {
		a.browserTabs = map[string]*browserTab{}
	}
	a.browserTabs[runID] = tab
	return tab, nil
}

// browserSweepLocked closes idle tabs; call under browserTabsMu.
func (a *Activities) browserSweepLocked(ctx context.Context) {
	now := time.Now()
	for runID, tab := range a.browserTabs {
		if now.Sub(tab.lastUsed) < browserIdleTTL {
			continue
		}
		a.browserEngine().call(ctx, "Target.closeTarget", map[string]any{"targetId": tab.targetID}, "")
		delete(a.browserTabs, runID)
	}
}

func (a *Activities) browserEngine() *browserEngine {
	a.browserOnce.Do(a.browserInitLocked)
	return a.browser
}

// browserInitLocked materializes the engine and the action budget together.
func (a *Activities) browserInitLocked() {
	if a.BrowserCDPURL == "" {
		return
	}
	a.browser = newBrowserEngine(a.BrowserCDPURL)
	budget := a.BrowserPerMin
	if budget <= 0 {
		budget = 60
	}
	a.browserLimiter = newWebSearchLimiter(budget, time.Minute)
}

// browserLimiterFor returns the process-global action budget (nil when the
// browser backend is off, meaning no cap needed).
func (a *Activities) browserLimiterFor() *webSearchLimiter {
	a.browserOnce.Do(a.browserInitLocked)
	return a.browserLimiter
}

func (a *Activities) handleBrowser(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if !a.NetworkAccess {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("network access is disabled for this agent", "NetworkDisabled", nil)
	}
	if a.BrowserCDPURL == "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("browser backend is not configured", "BrowserUnavailable", nil)
	}
	action, _ := request.Arguments["action"].(string)
	action = strings.TrimSpace(strings.ToLower(action))
	if action == "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("action is required (open, click, fill, evaluate, content, screenshot, close)", "InvalidToolArguments", nil)
	}
	if limiter := a.browserLimiterFor(); limiter != nil {
		if ok, retryIn := limiter.allow(); !ok {
			return ToolResult{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("browser action budget exhausted for this minute; retry in %d seconds", int(retryIn.Seconds())+1), "BrowserRateLimited", nil)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()

	switch action {
	case "open":
		return a.browserOpen(ctx, request)
	case "click":
		return a.browserClick(ctx, request)
	case "fill":
		return a.browserFill(ctx, request)
	case "evaluate":
		return a.browserEvaluate(ctx, request)
	case "content":
		return a.browserContent(ctx, request)
	case "screenshot":
		return a.browserScreenshot(ctx, request)
	case "close":
		return a.browserClose(ctx, request)
	default:
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("unknown action %q (open, click, fill, evaluate, content, screenshot, close)", action), "InvalidToolArguments", nil)
	}
}

func browserArgument(request ToolRequest, name string) (string, error) {
	value, _ := request.Arguments[name].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("%s is required for this action", name), "InvalidToolArguments", nil)
	}
	return value, nil
}

func (a *Activities) browserOpen(ctx context.Context, request ToolRequest) (ToolResult, error) {
	rawURL, err := browserArgument(request, "url")
	if err != nil {
		return ToolResult{}, err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("url must be an absolute http(s) URL", "InvalidToolArguments", nil)
	}
	tab, err := a.browserTabFor(ctx, request.RunID, "about:blank")
	if err != nil {
		return ToolResult{}, err
	}
	result, err := a.browserEngine().call(ctx, "Page.navigate", map[string]any{"url": rawURL}, tab.sessionID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("navigate: %w", err)
	}
	var navigation struct {
		ErrorText string `json:"errorText"`
	}
	_ = json.Unmarshal(result, &navigation)
	if navigation.ErrorText != "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			"navigate: "+navigation.ErrorText, "NavigationFailed", nil)
	}
	title, href := a.browserWaitReady(ctx, tab)
	return ToolResult{Content: fmt.Sprintf("opened: %s\ntitle: %s", href, title)}, nil
}

// browserWaitReady polls until the load completes (or the ready budget runs
// out) and reports the settled title and URL.
func (a *Activities) browserWaitReady(ctx context.Context, tab *browserTab) (title, href string) {
	readyDeadline := time.Now().Add(browserReadyTimeout)
	expression := `JSON.stringify({rs: document.readyState, t: document.title, h: location.href})`
	for {
		state := a.browserEvalString(ctx, tab, expression)
		var parsed struct {
			RS string `json:"rs"`
			T  string `json:"t"`
			H  string `json:"h"`
		}
		if json.Unmarshal([]byte(state), &parsed) == nil && parsed.RS != "" {
			title, href = parsed.T, parsed.H
			if parsed.RS == "complete" {
				return title, href
			}
		}
		if time.Now().After(readyDeadline) {
			return title, href
		}
		select {
		case <-ctx.Done():
			return title, href
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// browserEvalString evaluates an expression expecting a string value.
func (a *Activities) browserEvalString(ctx context.Context, tab *browserTab, expression string) string {
	value, _, err := a.browserEvalValue(ctx, tab, expression)
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

// browserEvalValue runs Runtime.evaluate with returnByValue and splits the
// outcome into a plain Go value or an exception message.
func (a *Activities) browserEvalValue(ctx context.Context, tab *browserTab, expression string) (any, string, error) {
	result, err := a.browserEngine().call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}, tab.sessionID)
	if err != nil {
		return nil, "", err
	}
	var evaluated struct {
		Result struct {
			Type           string          `json:"type"`
			Value          json.RawMessage `json:"value"`
			Unserializable string          `json:"unserializableValue"`
			Description    string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
				Value       string `json:"value"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &evaluated); err != nil {
		return nil, "", fmt.Errorf("decode evaluate reply: %w", err)
	}
	if evaluated.ExceptionDetails != nil {
		detail := evaluated.ExceptionDetails.Text
		if evaluated.ExceptionDetails.Exception != nil {
			if evaluated.ExceptionDetails.Exception.Description != "" {
				detail = evaluated.ExceptionDetails.Exception.Description
			} else if evaluated.ExceptionDetails.Exception.Value != "" {
				detail = evaluated.ExceptionDetails.Exception.Value
			}
		}
		return nil, detail, nil
	}
	switch evaluated.Result.Type {
	case "string", "boolean", "number", "object":
		if len(evaluated.Result.Value) > 0 {
			var value any
			if json.Unmarshal(evaluated.Result.Value, &value) == nil {
				return value, "", nil
			}
		}
		if evaluated.Result.Unserializable != "" {
			return evaluated.Result.Unserializable, "", nil
		}
		if evaluated.Result.Description != "" {
			return evaluated.Result.Description, "", nil
		}
		return nil, "", nil
	default: // undefined, function, symbol
		return nil, "", nil
	}
}

func (a *Activities) browserClick(ctx context.Context, request ToolRequest) (ToolResult, error) {
	selector, err := browserArgument(request, "selector")
	if err != nil {
		return ToolResult{}, err
	}
	tab, err := a.browserTabFor(ctx, request.RunID, "about:blank")
	if err != nil {
		return ToolResult{}, err
	}
	rectJSON := a.browserEvalString(ctx, tab, fmt.Sprintf(
		`(() => { const el = document.querySelector(%q); if (!el) return null; el.scrollIntoView({block: "center"}); const r = el.getBoundingClientRect(); return JSON.stringify({x: r.x + r.width / 2, y: r.y + r.height / 2, w: r.width, h: r.height}); })()`, selector))
	var rect struct {
		X, Y, W, H float64
	}
	if json.Unmarshal([]byte(rectJSON), &rect) != nil || rect.W <= 0 || rect.H <= 0 {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("element %q not found or not visible", selector), "ElementNotFound", nil)
	}
	press := map[string]any{"type": "mousePressed", "x": rect.X, "y": rect.Y, "button": "left", "clickCount": 1}
	release := map[string]any{"type": "mouseReleased", "x": rect.X, "y": rect.Y, "button": "left", "clickCount": 1}
	if _, err := a.browserEngine().call(ctx, "Input.dispatchMouseEvent", press, tab.sessionID); err != nil {
		return ToolResult{}, fmt.Errorf("click press: %w", err)
	}
	if _, err := a.browserEngine().call(ctx, "Input.dispatchMouseEvent", release, tab.sessionID); err != nil {
		return ToolResult{}, fmt.Errorf("click release: %w", err)
	}
	// Report where we landed: browserWaitReady polls until the load settles
	// (a click may trigger navigation or only inline handlers).
	title, href := a.browserWaitReady(ctx, tab)
	return ToolResult{Content: fmt.Sprintf("clicked %s; page: %s (%s)", selector, href, title)}, nil
}

func (a *Activities) browserFill(ctx context.Context, request ToolRequest) (ToolResult, error) {
	selector, err := browserArgument(request, "selector")
	if err != nil {
		return ToolResult{}, err
	}
	text, _ := request.Arguments["text"].(string) // empty text clears the field
	tab, err := a.browserTabFor(ctx, request.RunID, "about:blank")
	if err != nil {
		return ToolResult{}, err
	}
	focused := a.browserEvalString(ctx, tab, fmt.Sprintf(
		`(() => { const el = document.querySelector(%q); if (!el) return "missing"; el.scrollIntoView({block: "center"}); el.focus(); if ("value" in el) { el.value = ""; } else { document.execCommand("selectAll", false, null); } return "ok"; })()`, selector))
	if focused != "ok" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("element %q not found", selector), "ElementNotFound", nil)
	}
	if _, err := a.browserEngine().call(ctx, "Input.insertText", map[string]any{"text": text}, tab.sessionID); err != nil {
		return ToolResult{}, fmt.Errorf("insert text: %w", err)
	}
	return ToolResult{Content: fmt.Sprintf("filled %s with %d characters", selector, len(text))}, nil
}

func (a *Activities) browserEvaluate(ctx context.Context, request ToolRequest) (ToolResult, error) {
	expression, err := browserArgument(request, "expression")
	if err != nil {
		return ToolResult{}, err
	}
	tab, err := a.browserTabFor(ctx, request.RunID, "about:blank")
	if err != nil {
		return ToolResult{}, err
	}
	value, exception, err := a.browserEvalValue(ctx, tab, expression)
	if err != nil {
		return ToolResult{}, fmt.Errorf("evaluate: %w", err)
	}
	if exception != "" {
		return ToolResult{Content: "exception: " + truncateForModel(exception, browserEvalCap)}, nil
	}
	encoded, _ := json.Marshal(value)
	return ToolResult{Content: truncateForModel(string(encoded), browserEvalCap)}, nil
}

func (a *Activities) browserContent(ctx context.Context, request ToolRequest) (ToolResult, error) {
	tab, err := a.browserTabFor(ctx, request.RunID, "about:blank")
	if err != nil {
		return ToolResult{}, err
	}
	html := a.browserEvalString(ctx, tab, "document.documentElement.outerHTML")
	if strings.TrimSpace(html) == "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("no rendered content — open a page first", "NoContent", nil)
	}
	pageURL := currentTabURL(ctx, a, tab)
	title, markdown, extractErr := extractHTML([]byte(html), pageURL)
	if extractErr != nil {
		return ToolResult{Content: truncateForModel(html, browserEvalCap)}, nil
	}
	return ToolResult{Content: fmt.Sprintf("title: %s\nurl: %s\n\n%s", title, pageURL, truncateForModel(string(markdown), browserEvalCap))}, nil
}

// currentTabURL reports the tab's location for content headers and link
// rewriting; empty when evaluation fails (about:blank never got opened).
func currentTabURL(ctx context.Context, a *Activities, tab *browserTab) string {
	return a.browserEvalString(ctx, tab, "location.href")
}

func (a *Activities) browserScreenshot(ctx context.Context, request ToolRequest) (ToolResult, error) {
	tab, err := a.browserTabFor(ctx, request.RunID, "about:blank")
	if err != nil {
		return ToolResult{}, err
	}
	result, err := a.browserEngine().call(ctx, "Page.captureScreenshot", map[string]any{"format": "png"}, tab.sessionID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("screenshot: %w", err)
	}
	var captured struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &captured); err != nil || captured.Data == "" {
		return ToolResult{}, fmt.Errorf("screenshot: unexpected reply")
	}
	data, err := base64.StdEncoding.DecodeString(captured.Data)
	if err != nil {
		return ToolResult{}, fmt.Errorf("screenshot: decode: %w", err)
	}
	block, meta, err := prepareImageBlock(data)
	if err != nil {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("screenshot: "+err.Error(), "UnsupportedContent", nil)
	}
	return ToolResult{Content: meta.summary(), Images: []llm.ImageBlock{block}}, nil
}

func (a *Activities) browserClose(ctx context.Context, request ToolRequest) (ToolResult, error) {
	a.browserTabsMu.Lock()
	tab, ok := a.browserTabs[request.RunID]
	if ok {
		delete(a.browserTabs, request.RunID)
	}
	a.browserTabsMu.Unlock()
	if !ok {
		return ToolResult{Content: "no open tab for this run"}, nil
	}
	if _, err := a.browserEngine().call(ctx, "Target.closeTarget", map[string]any{"targetId": tab.targetID}, ""); err != nil {
		return ToolResult{}, fmt.Errorf("close tab: %w", err)
	}
	return ToolResult{Content: "tab closed"}, nil
}

// truncateForModel bounds a tool payload with an explicit marker.
func truncateForModel(text string, cap int) string {
	if len(text) <= cap {
		return text
	}
	return text[:cap] + fmt.Sprintf("\n\n[truncated at %d of %d bytes]", cap, len(text))
}
