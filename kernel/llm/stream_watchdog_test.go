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

// sseHandler streams scripted raw SSE lines with a delay between them, then
// keeps the connection open until the client goes away. Raw lines (not parsed
// chunks) let tests imitate gateway keepalives exactly.
func sseHandler(lines []string, every time.Duration) *httptest.Server {
	return sseServer(lines, every, false)
}

// sseFinishingServer streams the same lines but then sends [DONE] and closes:
// a stream that actually finishes.
func sseFinishingServer(lines []string, every time.Duration) *httptest.Server {
	return sseServer(lines, every, true)
}

func sseServer(lines []string, every time.Duration, done bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range lines {
			fmt.Fprintf(w, "%s\n\n", line)
			flusher.Flush()
			time.Sleep(every)
		}
		if done {
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
		// Hold the connection open: a gateway parking a queued request never
		// closes voluntarily. Returns when the client cancels the request.
		<-r.Context().Done()
	}))
}

func contentChunk(text string) string {
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
	return "data: " + string(body)
}

// Keepalive comments must not reset the idle watchdog: a gateway that parks a
// queued request behind ": ping" lines gets canceled at the idle deadline,
// not after the activity budget burns down.
func TestStreamIdleTimeoutIgnoresKeepaliveComments(t *testing.T) {
	server := sseHandler([]string{": ping", ": ping", ": ping"}, 50*time.Millisecond)
	defer server.Close()
	client := New(Config{
		BaseURL:                 server.URL,
		Model:                   "test",
		StreamIdleTimeout:       300 * time.Millisecond,
		StreamNoProgressTimeout: 0, // isolate the idle watchdog
	})

	started := time.Now()
	_, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected the watchdog to cancel a keepalive-only stream")
	}
	if !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("error should name the fired budget, got: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("watchdog fired too late: %s", elapsed)
	}
}

// Parseable data lines with no generated tokens reset the idle timer but not
// the no-progress one: empty-choice pings are connection liveness, not
// generation.
func TestStreamNoProgressTimeoutWithEmptyDataPings(t *testing.T) {
	server := sseHandler([]string{`data: {"choices":[]}`, `data: {"choices":[]}`, `data: {"choices":[]}`}, 50*time.Millisecond)
	defer server.Close()
	client := New(Config{
		BaseURL:                 server.URL,
		Model:                   "test",
		StreamIdleTimeout:       2 * time.Second, // generous: pings reset it
		StreamNoProgressTimeout: 400 * time.Millisecond,
	})

	started := time.Now()
	_, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected the no-progress watchdog to cancel a ping-only stream")
	}
	if !strings.Contains(err.Error(), "no generated tokens") {
		t.Fatalf("error should name the fired budget, got: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("watchdog fired too late: %s", elapsed)
	}
}

// A slow but steadily generating stream must survive both watchdogs: every
// content delta resets the idle and the no-progress timers.
func TestStreamSlowGenerationNotCanceled(t *testing.T) {
	chunks := []string{
		contentChunk("One "), contentChunk("two "), contentChunk("three "),
		contentChunk("four "), contentChunk("five "),
	}
	server := sseFinishingServer(chunks, 100*time.Millisecond)
	defer server.Close()
	client := New(Config{
		BaseURL:                 server.URL,
		Model:                   "test",
		StreamIdleTimeout:       250 * time.Millisecond,
		StreamNoProgressTimeout: 300 * time.Millisecond,
	})

	completion, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("slow stream should complete, got: %v", err)
	}
	if completion.Content != "One two three four five " {
		t.Fatalf("content = %q", completion.Content)
	}
	if completion.Transport != "stream" {
		t.Fatalf("transport = %q", completion.Transport)
	}
}

func TestConfigFromEnvNoProgressTimeout(t *testing.T) {
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", "http://localhost:1")
	t.Setenv("TEMPORALITY_MODEL_ID", "m")

	t.Setenv("TEMPORALITY_MODEL_STREAM_NO_PROGRESS_TIMEOUT", "")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StreamNoProgressTimeout != 4*time.Minute {
		t.Fatalf("default = %s, want 4m", cfg.StreamNoProgressTimeout)
	}

	t.Setenv("TEMPORALITY_MODEL_STREAM_NO_PROGRESS_TIMEOUT", "0s")
	cfg, err = ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StreamNoProgressTimeout != 0 {
		t.Fatalf("0s should disable, got %s", cfg.StreamNoProgressTimeout)
	}

	t.Setenv("TEMPORALITY_MODEL_STREAM_NO_PROGRESS_TIMEOUT", "nope")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid duration should fail")
	}
}
