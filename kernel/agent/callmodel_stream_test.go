package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// sseChatServer fakes a provider that streams one answer and then closes.
func sseChatServer(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if !payload.Stream {
			t.Errorf("expected a streaming request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range chunks {
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": chunk}}}})
			fmt.Fprintf(w, "data: %s\n\n", body)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

func TestCallModelStreamsTokensToBus(t *testing.T) {
	server := sseChatServer(t, []string{"Hello", " streamed", " answer"})
	defer server.Close()
	activities := &Activities{
		Model:  llm.New(llm.Config{BaseURL: server.URL, Model: "test", APIKey: "k", Timeout: 10 * time.Second}),
		Tokens: NewTokenBus(),
	}

	completion, err := activities.CallModel(context.Background(), ModelRequest{
		RunID: "run-stream", Turn: 3,
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CallModel: %v", err)
	}
	if completion.Content != "Hello streamed answer" {
		t.Fatalf("content = %q", completion.Content)
	}
	_, snapshot, cancel := activities.Tokens.Subscribe("run-stream")
	defer cancel()
	if snapshot.Turn != 3 || snapshot.Text != "Hello streamed answer" {
		t.Fatalf("snapshot = %+v, want turn 3 with full streamed text", snapshot)
	}
}

// capturedOutbox records enqueued events so tests can assert what the
// activity journaled straight through the outbox (model.attempt markers).
type capturedOutbox struct {
	events []observation.Event
}

func (c *capturedOutbox) Enqueue(_ context.Context, event observation.Event) error {
	c.events = append(c.events, event)
	return nil
}

// A nudge cancels the model call mid-stream: the guard must return fast
// without the blocking salvage (one HTTP request total) and without a
// stream_fallback marker — the workflow already journals model.nudged for the
// retry, a marker here would render as a bogus duplicate "retry" in the trace.
func TestCallModelCancelledContextSkipsMarkerAndSalvage(t *testing.T) {
	var requests int32
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"par\"}}]}\n\n")
		flusher.Flush()
		<-released // hang the stream like a stuck provider
	}))
	defer server.Close()
	defer close(released)
	activities := &Activities{
		Model:  llm.New(llm.Config{BaseURL: server.URL, Model: "test", APIKey: "k", Timeout: 10 * time.Second, StreamIdleTimeout: time.Minute}),
		Tokens: NewTokenBus(),
		Events: &capturedOutbox{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-cancelled context: the nudge fired before the request
	_, err := activities.CallModel(ctx, ModelRequest{
		RunID: "run-nudge", Turn: 2, Project: "p",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected the cancelled call to return an error")
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0 — the request context is dead before Do", got)
	}
	if outbox, ok := activities.Events.(*capturedOutbox); ok && len(outbox.events) != 0 {
		t.Fatalf("enqueued events = %d, want 0 (no marker for an intentional cancel)", len(outbox.events))
	}
}

// A provider that rejects streaming must not fail the call: CallModel falls
// back to the blocking non-streaming request.
func TestCallModelFallsBackWhenStreamingFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Stream {
			http.Error(w, "streaming unsupported", http.StatusBadRequest)
			return
		}
		body, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "blocking answer"}, "finish_reason": "stop"}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	activities := &Activities{
		Model:  llm.New(llm.Config{BaseURL: server.URL, Model: "test", APIKey: "k", Timeout: 10 * time.Second}),
		Tokens: NewTokenBus(),
	}

	completion, err := activities.CallModel(context.Background(), ModelRequest{
		RunID: "run-fallback", Turn: 1,
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CallModel: %v", err)
	}
	if !strings.Contains(completion.Content, "blocking answer") {
		t.Fatalf("content = %q", completion.Content)
	}
	if !completion.StreamFailed {
		t.Fatal("salvaged completion must be marked StreamFailed")
	}
	if completion.StreamError == "" {
		t.Fatal("salvaged completion must carry the stream error for model.completed")
	}
}
