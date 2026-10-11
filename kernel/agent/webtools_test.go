package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// webCacheReset clears the global fetch cache so tests are order-independent.
func webCacheReset(t *testing.T) {
	t.Helper()
	webCacheMu.Lock()
	webCache = map[string]webCacheEntry{}
	webCacheMu.Unlock()
}

func articleHTML(title string, paragraphs int) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><head><title>" + title + "</title></head><body>")
	b.WriteString("<article><h1>" + title + "</h1>")
	for i := 0; i < paragraphs; i++ {
		b.WriteString(fmt.Sprintf("<p>Paragraph %d: the agent loop persists observations to the journal outbox and expects an event id before continuing the turn.</p>", i))
	}
	b.WriteString("</article></body></html>")
	return b.String()
}

func TestWebFetchNetworkDisabled(t *testing.T) {
	webCacheReset(t)
	a := &Activities{}
	_, err := a.handleWebFetch(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": "https://example.com"},
	})
	kind, _ := nonRetryableType(t, err)
	if kind != "NetworkDisabled" {
		t.Fatalf("expected NetworkDisabled, got %q (%v)", kind, err)
	}
}

func TestWebFetchBlockedPrivate(t *testing.T) {
	webCacheReset(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be reached when guard is active")
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true}
	_, err := a.handleWebFetch(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": server.URL + "/page"},
	})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked-target error, got %v", err)
	}
}

func TestWebFetchHTMLArticle(t *testing.T) {
	webCacheReset(t)
	const title = "Agent Loops in Temporality"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, articleHTML(title, 20))
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	result, err := a.handleWebFetch(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": server.URL + "/docs/loops"},
	})
	if err != nil {
		t.Fatalf("handleWebFetch: %v", err)
	}
	if !strings.Contains(result.Content, "content-type: text/html") {
		t.Errorf("result missing content-type header line:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "title: "+title) {
		t.Errorf("result missing title %q:\n%s", title, result.Content)
	}
	if !strings.Contains(result.Content, "Paragraph 0:") {
		t.Errorf("result missing article body:\n%s", result.Content)
	}
}

func TestWebFetchRawJSON(t *testing.T) {
	webCacheReset(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"items":[1,2,3]}`)
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	result, err := a.handleWebFetch(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": server.URL + "/api/status"},
	})
	if err != nil {
		t.Fatalf("handleWebFetch: %v", err)
	}
	if !strings.Contains(result.Content, `"ok": true`) && !strings.Contains(result.Content, `"ok":true`) {
		t.Errorf("raw JSON body not passed through:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "title:") {
		t.Errorf("raw JSON must not carry a title line:\n%s", result.Content)
	}
}

func TestWebFetchTruncationBudget(t *testing.T) {
	webCacheReset(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("x", 4096))
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	result, err := a.handleWebFetch(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": server.URL + "/big.txt", "max_bytes": float64(200)},
	})
	if err != nil {
		t.Fatalf("handleWebFetch: %v", err)
	}
	if !strings.Contains(result.Content, "[truncated at 200 of 4096 bytes]") {
		t.Errorf("expected truncation note:\n%s", result.Content)
	}
}

func TestWebFetchTooLargeDownload(t *testing.T) {
	webCacheReset(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		// webFetchDownloadCap + 1 bytes of garbage.
		chunk := strings.Repeat("y", 64<<10)
		for i := 0; i <= webFetchDownloadCap/(64<<10); i++ {
			fmt.Fprint(w, chunk)
		}
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	_, err := a.handleWebFetch(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": server.URL + "/huge.bin"},
	})
	kind, _ := nonRetryableType(t, err)
	if kind != "TooLarge" {
		t.Fatalf("expected TooLarge, got %q (%v)", kind, err)
	}
}

func TestWebFetchBinaryRejected(t *testing.T) {
	webCacheReset(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		fmt.Fprint(w, "%PDF-1.7 fake")
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	_, err := a.handleWebFetch(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": server.URL + "/file.pdf"},
	})
	kind, _ := nonRetryableType(t, err)
	if kind != "UnsupportedContent" {
		t.Fatalf("expected UnsupportedContent, got %q (%v)", kind, err)
	}
}

func TestWebFetchCachesRepeatFetches(t *testing.T) {
	webCacheReset(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "stable body")
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	request := ToolRequest{Arguments: map[string]any{"url": server.URL + "/cached"}}
	for i := 0; i < 2; i++ {
		result, err := a.handleWebFetch(context.Background(), request)
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if !strings.Contains(result.Content, "stable body") {
			t.Fatalf("call %d: unexpected body:\n%s", i+1, result.Content)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected 1 origin hit, got %d", got)
	}
}

func TestWebFetchFollowsRedirects(t *testing.T) {
	webCacheReset(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/new", http.StatusFound)
		case "/new":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "moved content")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	result, err := a.handleWebFetch(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": server.URL + "/old"},
	})
	if err != nil {
		t.Fatalf("handleWebFetch: %v", err)
	}
	if !strings.Contains(result.Content, "url: "+server.URL+"/new") {
		t.Errorf("final URL must reflect redirect target:\n%s", result.Content)
	}
}

func TestWebSearchNetworkDisabled(t *testing.T) {
	a := &Activities{SearXNGURL: "http://searxng:8080"}
	_, err := a.handleWebSearch(context.Background(), ToolRequest{
		Arguments: map[string]any{"query": "temporal workflow"},
	})
	kind, _ := nonRetryableType(t, err)
	if kind != "NetworkDisabled" {
		t.Fatalf("expected NetworkDisabled, got %q (%v)", kind, err)
	}
}

func TestWebSearchNotConfigured(t *testing.T) {
	a := &Activities{NetworkAccess: true}
	_, err := a.handleWebSearch(context.Background(), ToolRequest{
		Arguments: map[string]any{"query": "temporal workflow"},
	})
	kind, _ := nonRetryableType(t, err)
	if kind != "SearchNotConfigured" {
		t.Fatalf("expected SearchNotConfigured, got %q (%v)", kind, err)
	}
}

// The SearXNG endpoint is operator configuration and frequently lives on an
// internal address (docker network) — the fetch SSRF guard must not apply.
func TestWebSearchReachesInternalSearXNG(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("expected format=json, got %q", r.URL.Query().Get("format"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"query": "temporal workflow",
			"answers": ["Temporal is a durable execution engine"],
			"infoboxes": [{"content": "Durable execution framework"}],
			"results": [
				{"title": "Temporal docs", "url": "https://docs.temporal.io", "content": "Develop durable workflows", "score": 1.0},
				{"title": "Temporal GitHub", "url": "https://github.com/temporalio/temporal", "content": "Source code", "score": 0.9},
				{"title": "Blog post", "url": "https://example.com/post", "content": "Deep dive", "score": 0.5}
			]
		}`)
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true, SearXNGURL: server.URL}
	result, err := a.handleWebSearch(context.Background(), ToolRequest{
		Arguments: map[string]any{"query": "temporal workflow", "max_results": float64(2)},
	})
	if err != nil {
		t.Fatalf("handleWebSearch: %v", err)
	}
	for _, expected := range []string{
		"query: temporal workflow",
		"answer: Temporal is a durable execution engine",
		"infobox: Durable execution framework",
		"results (2 of 3):",
		"Temporal docs",
		"https://docs.temporal.io",
		"Temporal GitHub",
		"Blog post", // listed beyond the cap must be cut
	} {
		if strings.Contains(result.Content, expected) && expected == "Blog post" {
			t.Errorf("result beyond max_results must be omitted:\n%s", result.Content)
		}
		if expected != "Blog post" && !strings.Contains(result.Content, expected) {
			t.Errorf("result missing %q:\n%s", expected, result.Content)
		}
	}
}

func TestWebSearchNoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"query": "zzz nothing", "results": []}`)
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true, SearXNGURL: server.URL}
	result, err := a.handleWebSearch(context.Background(), ToolRequest{
		Arguments: map[string]any{"query": "zzz nothing"},
	})
	if err != nil {
		t.Fatalf("handleWebSearch: %v", err)
	}
	if !strings.Contains(result.Content, "no results") {
		t.Errorf("expected no-results hint:\n%s", result.Content)
	}
}
