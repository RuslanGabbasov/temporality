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
