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

// A turn that goes straight for a tool call streams no text at all — only
// tool-call argument fragments. Those must reach the bus too, or the UI sits
// on "waiting for the first token" for the whole call while tokens flow.
func TestCallModelStreamsToolCallDeltasToBus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{
			`{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"run_command","arguments":""}}]}`,
			`{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]}`,
			`{"tool_calls":[{"index":0,"function":{"arguments":"[\"go\",\"test\"]}"}}]}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s}]}\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n\n")
		flusher.Flush()
	}))
	defer server.Close()
	activities := &Activities{
		Model:  llm.New(llm.Config{BaseURL: server.URL, Model: "test", APIKey: "k", Timeout: 10 * time.Second}),
		Tokens: NewTokenBus(),
	}

	_, err := activities.CallModel(context.Background(), ModelRequest{
		RunID: "run-tool-stream", Turn: 7,
		Messages: []llm.Message{{Role: "user", Content: "run tests"}},
	})
	if err != nil {
		t.Fatalf("CallModel: %v", err)
	}
	_, snapshot, cancel := activities.Tokens.Subscribe("run-tool-stream")
	defer cancel()
	if snapshot.Turn != 7 || snapshot.Tool != "run_command" {
		t.Fatalf("snapshot = %+v, want turn 7 tool run_command", snapshot)
	}
	if snapshot.Args != `{"command":["go","test"]}` {
		t.Fatalf("snapshot args = %q, want accumulated tool arguments", snapshot.Args)
	}
	if snapshot.Text != "" {
		t.Fatalf("snapshot text = %q, want no text on a tool-only turn", snapshot.Text)
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

// A provider that accepts the request and then goes silent is queueing the
// request server-side: the blocking salvage would join the same queue and add
// minutes of silence. CallModel must fail the attempt instead (one HTTP
// request total) and journal a stream_timeout marker — the activity retry
// opens a fresh connection, which is the automatic version of a manual nudge.
func TestCallModelStreamTimeoutSkipsBlockingSalvage(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		// Empty-choice pings keep data lines flowing — the request looks alive
		// to the idle watchdog, only the first-token budget can catch it.
		for {
			if _, err := fmt.Fprint(w, "data: {\"choices\":[]}\n\n"); err != nil {
				return // the watchdog cancelled the stream — let server.Close() finish
			}
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer server.Close()
	activities := &Activities{
		Model: llm.New(llm.Config{
			BaseURL: server.URL, Model: "test", APIKey: "k", Timeout: 10 * time.Second,
			StreamIdleTimeout:       time.Minute,     // pings reset it — isolate the first-token budget
			StreamNoProgressTimeout: 5 * time.Minute, // would fire far later
			StreamFirstTokenTimeout: 150 * time.Millisecond,
		}),
		Tokens:   NewTokenBus(),
		Events:   &capturedOutbox{},
		SourceID: "kernel", // markers validate source.id — NewActivities always sets one
	}

	_, err := activities.CallModel(context.Background(), ModelRequest{
		RunID: "run-queued", Turn: 2, Project: "p",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected the watchdog timeout to fail the attempt")
	}
	if !llm.StreamTimedOut(err) {
		t.Fatalf("error should classify as a stream timeout, got: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("HTTP requests = %d, want 1 — no blocking salvage after a watchdog timeout", got)
	}
	outbox, ok := activities.Events.(*capturedOutbox)
	if !ok || len(outbox.events) != 1 {
		t.Fatalf("enqueued events = %d, want exactly the stream_timeout marker", len(outbox.events))
	}
	if outbox.events[0].Type != "model.attempt" || outbox.events[0].Data["reason"] != "stream_timeout" {
		t.Fatalf("marker = %v, want model.attempt/stream_timeout", outbox.events[0])
	}
	if outbox.events[0].Data["error"] == nil || !strings.Contains(fmt.Sprint(outbox.events[0].Data["error"]), "first token timeout") {
		t.Fatalf("marker error should name the fired budget, got: %v", outbox.events[0].Data["error"])
	}
}
