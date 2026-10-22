package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// A queued request that never produces its first token must die at the
// first-token deadline even while the gateway keeps data lines (empty pings)
// flowing — and earlier than the generic no-progress budget would fire.
func TestStreamFirstTokenTimeoutCancelsQueuedStream(t *testing.T) {
	server := sseHandler([]string{`data: {"choices":[]}`, `data: {"choices":[]}`, `data: {"choices":[]}`}, 40*time.Millisecond)
	defer server.Close()
	client := New(Config{
		BaseURL:                 server.URL,
		Model:                   "test",
		StreamIdleTimeout:       2 * time.Second, // pings reset it
		StreamNoProgressTimeout: 5 * time.Second, // would fire much later
		StreamFirstTokenTimeout: 300 * time.Millisecond,
	})

	started := time.Now()
	_, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected the first-token watchdog to cancel a queued stream")
	}
	if !strings.Contains(err.Error(), "first token timeout") {
		t.Fatalf("error should name the fired budget, got: %v", err)
	}
	if !StreamTimedOut(err) {
		t.Fatal("StreamTimedOut must classify watchdog errors")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("watchdog fired too late: %s", elapsed)
	}
}

// Once the first generated token arrived the first-token budget is spent:
// a stream that generated and then idles is bounded by the idle/no-progress
// watchdogs, not by the first-token one.
func TestStreamFirstTokenTimeoutSatisfiedByFirstChunk(t *testing.T) {
	server := sseFinishingServer([]string{contentChunk("hello")}, 150*time.Millisecond)
	defer server.Close()
	client := New(Config{
		BaseURL:                 server.URL,
		Model:                   "test",
		StreamIdleTimeout:       2 * time.Second,
		StreamNoProgressTimeout: 0,
		StreamFirstTokenTimeout: 250 * time.Millisecond, // the chunk lands well inside it
	})

	completion, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("a stream that produced its first token in budget must complete, got: %v", err)
	}
	if completion.Content != "hello" {
		t.Fatalf("content = %q", completion.Content)
	}
}

// The first-token budget scales with prompt size: a large uncached context
// legitimately spends minutes in prefill before any token, and the watchdog
// must not execute a lawful big-prompt request. The server stays silent for
// 600ms — past the 300ms base budget — so only the payload scaling can save
// the stream.
func TestStreamFirstTokenTimeoutScalesWithPromptSize(t *testing.T) {
	bigPrompt := strings.Repeat("a", 4_000_000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(600 * time.Millisecond) // silent prefill, no data lines
		fmt.Fprintf(w, "%s\n\n", contentChunk("late"))
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()
	client := New(Config{
		BaseURL:                 server.URL,
		Model:                   "test",
		StreamIdleTimeout:       2 * time.Second,
		StreamNoProgressTimeout: 0,
		StreamFirstTokenTimeout: 300 * time.Millisecond,
	})

	started := time.Now()
	completion, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: bigPrompt}}, nil, nil)
	if err != nil {
		t.Fatalf("big-prompt prefill must outlive the base first-token budget, got: %v", err)
	}
	if !strings.Contains(completion.Content, "late") {
		t.Fatalf("content = %q", completion.Content)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("elapsed = %s, test stream should finish quickly", elapsed)
	}
}

// Protocol failures are not watchdog timeouts: the blocking fallback may
// still help there, so the classifier must tell them apart.
func TestStreamTimedOutRejectsNonWatchdogErrors(t *testing.T) {
	if StreamTimedOut(context.Canceled) {
		t.Fatal("context cancellation is not a watchdog timeout")
	}
	if StreamTimedOut(&statusError{code: 500, body: "boom"}) {
		t.Fatal("a status error is not a watchdog timeout")
	}
	if StreamTimedOut(nil) {
		t.Fatal("nil is not a watchdog timeout")
	}
}

// A provider that accepts the connection and then sends nothing — not even
// response headers — parks Do() itself. The watchdog cancel must keep its
// identity through the Do error path: as a bare transport error it would
// misroute callers into the blocking retry ladder, which joins the same queue
// and hangs for minutes (the exact 5-10-minute first-token waits the traces
// show).
func TestStreamWatchdogCancelDuringDoKeepsWatchdogIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body first: only after the body hits EOF does the test
		// server arm its disconnect detection (background read), and without it
		// the handler would never learn that the client went away.
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done() // headers never sent: a pure server-side queue
	}))
	defer server.Close()
	client := New(Config{
		BaseURL:           server.URL,
		Model:             "test",
		StreamIdleTimeout: 250 * time.Millisecond,
		// Isolate the idle watchdog: the other budgets would fire later.
		StreamNoProgressTimeout: time.Minute,
		StreamFirstTokenTimeout: time.Minute,
	})

	started := time.Now()
	_, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected the idle watchdog to cancel a header-less queue")
	}
	if !StreamTimedOut(err) {
		t.Fatalf("a watchdog cancel during Do must stay a watchdog error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("error should name the fired budget, got: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("watchdog fired too late: %s", elapsed)
	}
}

func TestConfigFromEnvFirstTokenTimeout(t *testing.T) {
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", "http://localhost:1")
	t.Setenv("TEMPORALITY_MODEL_ID", "m")
	t.Setenv("TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT", "90s")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.StreamFirstTokenTimeout != 90*time.Second {
		t.Fatalf("StreamFirstTokenTimeout = %v, want 90s", cfg.StreamFirstTokenTimeout)
	}
	t.Setenv("TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT", "nope")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("ConfigFromEnv accepted a malformed first-token timeout")
	}

	// Explicit env "0" disables the watchdog and must survive New's
	// zero-means-unset defaulting.
	t.Setenv("TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT", "0s")
	cfg, err = ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.StreamFirstTokenTimeout >= 0 {
		t.Fatalf("env 0s should disable, got %v", cfg.StreamFirstTokenTimeout)
	}
	client := New(cfg)
	if client.cfg.StreamFirstTokenTimeout >= 0 {
		t.Fatalf("New revived an explicitly disabled watchdog: %v", client.cfg.StreamFirstTokenTimeout)
	}
}

func TestNewDefaultsFirstTokenTimeout(t *testing.T) {
	client := New(Config{BaseURL: "http://localhost:1", Model: "m"})
	if client.cfg.StreamFirstTokenTimeout != 2*time.Minute {
		t.Fatalf("StreamFirstTokenTimeout = %v, want the 2m default", client.cfg.StreamFirstTokenTimeout)
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
