package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// streamServer fakes a provider that streams one content chunk every
// chunkInterval, then closes with [DONE]. If stallAfter > 0 it stops sending
// anything after that many chunks (connection stays open).
func streamServer(t *testing.T, chunks int, chunkInterval, stallFor time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < chunks; i++ {
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": fmt.Sprintf("c%d ", i)}}}})
			fmt.Fprintf(w, "data: %s\n\n", body)
			flusher.Flush()
			if stallFor > 0 {
				time.Sleep(stallFor)
			} else {
				time.Sleep(chunkInterval)
			}
		}
		if stallFor > 0 {
			// Stall: keep the connection open well past the idle timeout
			// without sending anything.
			time.Sleep(2 * time.Second)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// A slow but steady stream must survive its own total duration exceeding the
// blocking client timeout: the budget for streams is silence between chunks,
// not total time. This is the regression test for long generations cut off
// mid-flight by http.Client.Timeout.
func TestStreamCompleteSurvivesSlowStreamBeyondClientTimeout(t *testing.T) {
	server := streamServer(t, 8, 70*time.Millisecond, 0)
	defer server.Close()
	client := New(Config{
		BaseURL: server.URL, Model: "test", APIKey: "k",
		Timeout:           250 * time.Millisecond, // total cap a steady stream must outlive
		StreamIdleTimeout: 400 * time.Millisecond,
	})

	started := time.Now()
	completion, err := client.StreamComplete(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}
	if !strings.Contains(completion.Content, "c7") {
		t.Fatalf("content = %q, want the last chunk c7", completion.Content)
	}
	if elapsed := time.Since(started); elapsed < 500*time.Millisecond {
		t.Fatalf("elapsed = %v, want a stream longer than the 250ms client timeout", elapsed)
	}
}

// A stalled stream (no chunks at all) must be canceled after the idle
// timeout instead of hanging until some outer deadline.
func TestStreamCompleteCancelsStalledStreamAfterIdleTimeout(t *testing.T) {
	server := streamServer(t, 1, 0, 600*time.Millisecond)
	defer server.Close()
	client := New(Config{
		BaseURL: server.URL, Model: "test", APIKey: "k",
		Timeout:           10 * time.Second,
		StreamIdleTimeout: 150 * time.Millisecond,
	})

	started := time.Now()
	_, err := client.StreamComplete(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("StreamComplete succeeded, want an idle-timeout error")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("elapsed = %v, want cancellation shortly after the 150ms idle timeout", elapsed)
	}
}

// The stream shape (deltas + first-token latency) is the operator's
// stream-health signal: a buffered gateway shows one late delta where a real
// stream shows many early ones — recorded on every streaming completion.
func TestStreamCompleteRecordsStreamShape(t *testing.T) {
	server := streamServer(t, 4, 40*time.Millisecond, 0)
	defer server.Close()
	client := New(Config{
		BaseURL: server.URL, Model: "test", APIKey: "k",
		StreamIdleTimeout: 2 * time.Second,
	})

	completion, err := client.StreamComplete(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}
	if completion.Deltas != 4 {
		t.Fatalf("deltas = %d, want 4", completion.Deltas)
	}
	if completion.FirstTokenMs < 0 {
		t.Fatalf("first_token_ms = %d", completion.FirstTokenMs)
	}
}

// A provider that streams reasoning and then closes the connection mid-
// generation — no answer text, no tool calls, no finish reason, no [DONE] —
// must surface as a transport failure, not as a successful empty answer:
// CallModel would otherwise complete the run with an empty response.
func TestStreamCompleteReasoningOnlyTruncationIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": fmt.Sprintf("thinking %d ", i)}}}})
			fmt.Fprintf(w, "data: %s\n\n", body)
			flusher.Flush()
		}
		// Connection drops mid-generation: no content, no finish_reason,
		// no [DONE] — the provider just goes away.
	}))
	defer server.Close()
	client := New(Config{
		BaseURL: server.URL, Model: "test", APIKey: "k",
		StreamIdleTimeout: 2 * time.Second,
	})

	completion, err := client.StreamComplete(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatalf("a reasoning-only truncated stream must fail, got completion %+v", completion)
	}
	if !transient(err) {
		t.Fatalf("the truncation must be transient (retryable), got: %v", err)
	}
}

// The same truncation is survivable when the consumer stopped the stream on
// purpose: it owns the content it asked to stop at.
func TestStreamCompleteConsumerStopKeepsPartialContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "partial answer"}}}})
		fmt.Fprintf(w, "data: %s\n\n", body)
		flusher.Flush()
		time.Sleep(2 * time.Second) // no finish reason: the consumer stopped first
	}))
	defer server.Close()
	client := New(Config{
		BaseURL: server.URL, Model: "test", APIKey: "k",
		StreamIdleTimeout: 2 * time.Second,
	})

	completion, err := client.StreamComplete(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil, func(delta StreamDelta) bool {
			return true // stop after the first chunk: the consumer's choice
		})
	if err != nil {
		t.Fatalf("a consumer-stopped stream must keep its partial content, got: %v", err)
	}
	if completion.Content != "partial answer" {
		t.Fatalf("content = %q", completion.Content)
	}
}

func TestNewDefaultsStreamIdleTimeout(t *testing.T) {
	client := New(Config{BaseURL: "http://localhost:1", Model: "m"})
	if client.cfg.StreamIdleTimeout != 90*time.Second {
		t.Fatalf("StreamIdleTimeout = %v, want the 90s default", client.cfg.StreamIdleTimeout)
	}
}

// Tool calls accumulate into a map keyed by delta index; the workflow
// executes them in completion order, so index 1 arriving before index 0
// must still come out as [0, 1].
func TestStreamCompleteToolCallsSortedByIndex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call-b\",\"function\":{\"name\":\"second\",\"arguments\":\"{}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-a\",\"function\":{\"name\":\"first\",\"arguments\":\"{\\\"x\\\":1}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	completion, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}
	if len(completion.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(completion.ToolCalls))
	}
	if completion.ToolCalls[0].ID != "call-a" || completion.ToolCalls[0].Name != "first" {
		t.Fatalf("first tool call = %s/%s, want call-a/first (index 0)", completion.ToolCalls[0].ID, completion.ToolCalls[0].Name)
	}
	if completion.ToolCalls[1].ID != "call-b" || completion.ToolCalls[1].Name != "second" {
		t.Fatalf("second tool call = %s/%s, want call-b/second (index 1)", completion.ToolCalls[1].ID, completion.ToolCalls[1].Name)
	}
	if completion.ToolCalls[0].Args["x"].(float64) != 1 {
		t.Fatalf("index-0 args = %v", completion.ToolCalls[0].Args)
	}
}

// A single SSE data line larger than the old 64 KiB scanner cap must not
// kill the stream: bufio.Scanner used to fail with "token too long"
// mid-generation, and the caller replayed the whole turn. 128 KiB now parses
// as one ordinary delta.
func TestStreamCompleteHandlesLargeSSELine(t *testing.T) {
	big := strings.Repeat("x", 128*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": big}, "finish_reason": "stop"}}})
		fmt.Fprintf(w, "data: %s\n\n", body)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	completion, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}
	if len(completion.Content) != 128*1024 {
		t.Fatalf("content length = %d, want %d", len(completion.Content), 128*1024)
	}
}

// The streaming path never retries a status error, but the parsed
// Retry-After must still ride the error for messages and observability.
func TestStreamComplete429CarriesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	_, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	var statusErr *statusError
	if !errors.As(err, &statusErr) || statusErr.code != http.StatusTooManyRequests {
		t.Fatalf("want a 429 status error, got: %v", err)
	}
	if statusErr.retryAfter != 5*time.Second {
		t.Fatalf("retry_after = %s, want 5s", statusErr.retryAfter)
	}
	if !strings.Contains(statusErr.Error(), "retry-after 5s") {
		t.Fatalf("error message should surface the hint: %v", statusErr)
	}
}

// The streaming request carries all four fields the 400-degradation ladder
// can strip — reasoning (config), prompt_cache_key (option), the parallel
// toggle (tools) and stream_options (StreamComplete adds it on every non-
// Anthropic host). StreamComplete itself performs a single attempt — the
// ladder lives in Complete only — so this pins the precondition: all four
// ride the wire together when asked.
func TestStreamCompleteCarriesAllStrippableFields(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second, Reasoning: "high"})
	tools := []ToolDef{{Name: "echo", Description: "echo", Parameters: map[string]any{"type": "object"}}}
	_, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, tools, nil, WithPromptCacheKey("cache-1"))
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}
	for _, field := range []string{"reasoning", "prompt_cache_key", "parallel_tool_calls", "stream_options"} {
		if _, present := body[field]; !present {
			t.Fatalf("%s missing from the streaming request, want all four strippable fields present", field)
		}
	}
}

func TestConfigFromEnvStreamIdleTimeout(t *testing.T) {
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", "http://localhost:1")
	t.Setenv("TEMPORALITY_MODEL_ID", "m")
	t.Setenv("TEMPORALITY_MODEL_STREAM_IDLE_TIMEOUT", "45s")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.StreamIdleTimeout != 45*time.Second {
		t.Fatalf("StreamIdleTimeout = %v, want 45s", cfg.StreamIdleTimeout)
	}
	t.Setenv("TEMPORALITY_MODEL_STREAM_IDLE_TIMEOUT", "not-a-duration")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("ConfigFromEnv accepted a malformed idle timeout")
	}
}
