package llm

import (
	"context"
	"encoding/json"
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
