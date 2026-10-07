package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
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
}
