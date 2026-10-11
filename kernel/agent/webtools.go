package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	mdconv "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/go-shiori/go-readability"
	"go.temporal.io/sdk/temporal"

	"github.com/temporality-project/temporality/kernel/llm"
)

// Web research tools (docs/web-research.md phase 1): a readability-based page
// fetcher and a SearXNG-backed search. Both run in the kernel process (not the
// sandbox), are gated by the agent's network capability, land in the journal
// through the regular tool.* events, and are auto-approved like run_command —
// with network enabled the sandbox could curl the same endpoints anyway, and
// these tools are strictly safer (SSRF-guarded, size-capped, no exfil headers).

const (
	// webFetchDownloadCap bounds the raw response body before extraction.
	webFetchDownloadCap = 2 << 20 // 2 MiB
	// webFetchDefaultOutput is the default extracted-content budget.
	webFetchDefaultOutput = 48 << 10 // 48 KiB
	// webFetchMaxOutput is the largest budget an agent may request.
	webFetchMaxOutput = 128 << 10 // 128 KiB
	// webFetchTimeout bounds one fetch including redirects.
	webFetchTimeout = 30 * time.Second
	// webSearchTimeout bounds one SearXNG query.
	webSearchTimeout = 15 * time.Second
	// webCacheTTL keeps repeated fetches of the same URL within one kernel
	// process cheap; the cache is keyed by URL and evicts everything when full.
	webCacheTTL = 10 * time.Minute
	webCacheCap = 64
)

var webFetchTool = llm.ToolDef{
	Name:        "web_fetch",
	Description: "Fetch an http(s) URL and return the page as clean markdown (readability-extracted) or raw text for non-HTML content. Output is size-capped. Redirects are followed (max 5). Internal/private network targets are blocked. For binary downloads (archives, images, PDFs) use run_command with curl into /workspace instead.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":       map[string]any{"type": "string", "description": "Absolute http(s) URL to fetch"},
			"max_bytes": map[string]any{"type": "integer", "description": "Optional output budget in bytes (default 49152, max 131072)"},
		},
		"required": []string{"url"},
	},
}

var webSearchTool = llm.ToolDef{
	Name:        "web_search",
	Description: "Search the web through the instance's SearXNG meta-search engine and return ranked results (title, URL, snippet). Use it to discover pages, then web_fetch the promising ones. Queries work best in the language of the expected sources.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":       map[string]any{"type": "string", "description": "Search query"},
			"max_results": map[string]any{"type": "integer", "description": "Optional result cap (default 8, max 20)"},
		},
		"required": []string{"query"},
	},
}

// webCacheEntry stores the processed (extracted/capped) payload for a URL.
type webCacheEntry struct {
	at          time.Time
	contentType string
	finalURL    string
	title       string
	body        []byte
}

var (
	webCacheMu    sync.Mutex
	webCache      = map[string]webCacheEntry{}
	blockedNets   = mustCIDRs()
	webHTTPClient = &http.Client{}
)

func mustCIDRs() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "224.0.0.0/4",
		"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("webtools: bad blocked cidr " + cidr)
		}
		nets = append(nets, ipNet)
	}
	return nets
}

// webTransport returns an HTTP transport that refuses connections to private,
// loopback and link-local targets. The dialer resolves the host and dials the
// checked IP directly, so a DNS record flipped between check and connect
// (rebinding) cannot bypass the guard. Redirects go through the same dialer.
func webTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("web_fetch: bad address %q", addr)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("web_fetch: resolve %q: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("web_fetch: no addresses for %q", host)
			}
			for _, ip := range ips {
				if isBlockedWebIP(ip.IP) {
					return nil, fmt.Errorf("web_fetch: target %q resolves to a blocked (private/loopback) address", host)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		// Public web only: TLS verification stays strict.
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func isBlockedWebIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, blocked := range blockedNets {
		if blocked.Contains(ip) {
			return true
		}
	}
	return false
}

func (a *Activities) handleWebFetch(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if !a.NetworkAccess {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("network access is disabled for this agent", "NetworkDisabled", nil)
	}
	rawURL, _ := request.Arguments["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("url is required", "InvalidToolArguments", nil)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("url must be an absolute http(s) URL", "InvalidToolArguments", nil)
	}
	budget := webFetchDefaultOutput
	if value, ok := request.Arguments["max_bytes"].(float64); ok && value > 0 {
		budget = int(value)
		if budget > webFetchMaxOutput {
			budget = webFetchMaxOutput
		}
	}

	transport := webTransport()
	if a.WebFetchAllowPrivate {
		transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   webFetchTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("web_fetch: too many redirects")
			}
			return nil
		},
	}

	if entry, ok := webCacheLookup(rawURL); ok {
		return ToolResult{Content: formatWebResult(entry, budget)}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, webFetchTimeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidToolArguments", nil)
	}
	// A "compatible" UA: honest about the client while passing the UA filters
	// most documentation sites apply against library defaults.
	httpRequest.Header.Set("User-Agent", "Mozilla/5.0 (compatible; TemporalityAgent/1.0; +web_fetch)")
	httpRequest.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.5")
	httpRequest.Header.Set("Accept-Language", "en,ru;q=0.8")

	response, err := client.Do(httpRequest)
	if err != nil {
		return ToolResult{}, fmt.Errorf("web_fetch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("web_fetch: HTTP %d from %s", response.StatusCode, response.Request.URL.String()), "HTTPStatus", nil)
	}

	body := io.LimitReader(response.Body, webFetchDownloadCap+1)
	data, err := io.ReadAll(body)
	if err != nil {
		return ToolResult{}, fmt.Errorf("web_fetch: read body: %w", err)
	}
	if len(data) > webFetchDownloadCap {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("web_fetch: response exceeds %d bytes — download it with curl instead", webFetchDownloadCap), "TooLarge", nil)
	}

	contentType := response.Header.Get("Content-Type")
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	finalURL := rawURL
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}

	var entry webCacheEntry
	entry.at = time.Now()
	entry.finalURL = finalURL
	entry.contentType = mediaType
	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml" || mediaType == "":
		entry.title, entry.body, err = extractHTML(data, finalURL)
		if err != nil {
			return ToolResult{}, fmt.Errorf("web_fetch: extract: %w", err)
		}
	case mediaType == "application/json" || mediaType == "text/plain" || mediaType == "text/markdown" ||
		mediaType == "application/xml" || mediaType == "text/xml" || mediaType == "text/csv":
		entry.body = data
	default:
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("web_fetch: unsupported content type %q — binary content must be downloaded with curl into /workspace", mediaType), "UnsupportedContent", nil)
	}
	webCacheStore(rawURL, entry)
	return ToolResult{Content: formatWebResult(entry, budget)}, nil
}

// extractHTML converts a fetched HTML document into markdown: readability first
// (articles), falling back to whole-page conversion for non-article layouts
// (docs, landing pages). The last resort is readability's plain text.
func extractHTML(data []byte, pageURL string) (string, []byte, error) {
	parsed, err := url.Parse(pageURL)
	if err != nil {
		parsed = nil
	}
	article, readErr := readability.FromReader(bytes.NewReader(data), parsed)
	title := ""
	var markdown []byte
	if readErr == nil {
		title = article.Title
		if len(article.Content) >= 500 {
			if converted, err := mdconv.ConvertString(article.Content); err == nil && strings.TrimSpace(converted) != "" {
				markdown = []byte(converted)
			}
		}
	}
	if len(markdown) == 0 {
		if converted, err := mdconv.ConvertString(string(data)); err == nil && strings.TrimSpace(converted) != "" {
			markdown = []byte(converted)
		}
	}
	if len(markdown) == 0 && readErr == nil && article.TextContent != "" {
		markdown = []byte(article.TextContent)
	}
	if len(markdown) == 0 {
		return "", nil, fmt.Errorf("no extractable text content")
	}
	return title, markdown, nil
}

// formatWebResult renders a cache entry under the requested byte budget.
func formatWebResult(entry webCacheEntry, budget int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "url: %s\n", entry.finalURL)
	fmt.Fprintf(&b, "content-type: %s\n", entry.contentType)
	if entry.title != "" {
		fmt.Fprintf(&b, "title: %s\n", entry.title)
	}
	b.WriteString("\n")
	body := entry.body
	if len(body) > budget {
		b.Write(body[:budget])
		fmt.Fprintf(&b, "\n\n[truncated at %d of %d bytes]", budget, len(body))
	} else {
		b.Write(body)
	}
	return b.String()
}

func webCacheLookup(key string) (webCacheEntry, bool) {
	webCacheMu.Lock()
	defer webCacheMu.Unlock()
	entry, ok := webCache[key]
	if !ok || time.Since(entry.at) > webCacheTTL {
		return webCacheEntry{}, false
	}
	return entry, true
}

func webCacheStore(key string, entry webCacheEntry) {
	webCacheMu.Lock()
	defer webCacheMu.Unlock()
	if len(webCache) >= webCacheCap {
		webCache = map[string]webCacheEntry{}
	}
	webCache[key] = entry
}

func (a *Activities) handleWebSearch(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if !a.NetworkAccess {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("network access is disabled for this agent", "NetworkDisabled", nil)
	}
	if a.SearXNGURL == "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("web search is not configured (no SearXNG URL)", "SearchNotConfigured", nil)
	}
	query, _ := request.Arguments["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("query is required", "InvalidToolArguments", nil)
	}
	maxResults := 8
	if value, ok := request.Arguments["max_results"].(float64); ok && value > 0 {
		maxResults = int(value)
		if maxResults > 20 {
			maxResults = 20
		}
	}

	ctx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()
	searchURL := strings.TrimRight(a.SearXNGURL, "/") + "/search?format=json&q=" + url.QueryEscape(query)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return ToolResult{}, err
	}
	httpRequest.Header.Set("User-Agent", "TemporalityAgent/1.0")
	response, err := webSearchClient().Do(httpRequest)
	if err != nil {
		return ToolResult{}, fmt.Errorf("web_search: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("web_search: SearXNG returned HTTP %d", response.StatusCode), "SearchBackend", nil)
	}
	var payload struct {
		Query     string `json:"query"`
		Answers   []any  `json:"answers"`
		Infoboxes []struct {
			Content string `json:"content"`
		} `json:"infoboxes"`
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return ToolResult{}, fmt.Errorf("web_search: decode response: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "query: %s\n", payload.Query)
	if len(payload.Results) == 0 && len(payload.Answers) == 0 {
		b.WriteString("\nno results — try rephrasing the query or another language")
		return ToolResult{Content: b.String()}, nil
	}
	for _, answer := range payload.Answers {
		if text, ok := answer.(string); ok && strings.TrimSpace(text) != "" {
			b.WriteString("\nanswer: " + strings.TrimSpace(text) + "\n")
		}
	}
	for _, box := range payload.Infoboxes {
		if strings.TrimSpace(box.Content) != "" {
			b.WriteString("\ninfobox: " + strings.TrimSpace(box.Content) + "\n")
		}
	}
	shown := payload.Results
	if len(shown) > maxResults {
		shown = shown[:maxResults]
	}
	fmt.Fprintf(&b, "\nresults (%d of %d):\n", len(shown), len(payload.Results))
	for _, result := range shown {
		fmt.Fprintf(&b, "\n- %s\n  %s\n", result.Title, result.URL)
		if snippet := strings.TrimSpace(result.Content); snippet != "" {
			fmt.Fprintf(&b, "  %s\n", snippet)
		}
	}
	return ToolResult{Content: b.String()}, nil
}

// webSearchClient intentionally does NOT use webTransport: the SearXNG endpoint
// is operator configuration (often an internal docker-network address like
// http://searxng:8080), not an agent-supplied URL, so the SSRF guard must not
// apply to it. The guard protects web_fetch targets chosen by the model.
func webSearchClient() *http.Client {
	return &http.Client{Timeout: webSearchTimeout}
}
