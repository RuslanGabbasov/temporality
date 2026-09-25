package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const okResponse = `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

func TestNewDerivesProviderHost(t *testing.T) {
	client := New(Config{BaseURL: "https://api.example.com/v1", Model: "m"})
	if got := client.Provider(); got != "api.example.com" {
		t.Fatalf("provider host = %q, want api.example.com", got)
	}
}

func TestNewKeepsProviderEmptyForBadURL(t *testing.T) {
	client := New(Config{BaseURL: "://not-a-url", Model: "m"})
	if got := client.Provider(); got != "" {
		t.Fatalf("provider host = %q, want empty", got)
	}
}

func TestTruncatedDetection(t *testing.T) {
	if !(Completion{Finish: "length"}).Truncated() {
		t.Error("finish_reason=length must report truncated")
	}
	if (Completion{Finish: "stop"}).Truncated() {
		t.Error("finish_reason=stop must not report truncated")
	}
}

func TestTransientClassification(t *testing.T) {
	if !transient(&transportError{err: errors.New("connection reset by peer")}) {
		t.Error("transport errors must be transient")
	}
	if transient(&statusError{code: http.StatusBadRequest, body: "bad request"}) {
		t.Error("400 must not be transient")
	}
	if transient(&statusError{code: http.StatusUnauthorized, body: "bad key"}) {
		t.Error("401 must not be transient")
	}
	if !transient(&statusError{code: http.StatusTooManyRequests, body: "slow down"}) {
		t.Error("429 must be transient")
	}
	if !transient(&statusError{code: http.StatusBadGateway, body: "upstream boom"}) {
		t.Error("5xx must be transient")
	}
	if transient(errors.New("decode response: unexpected EOF")) {
		t.Error("decoding errors must not be transient")
	}
}

func TestCompleteToolChoiceOnlyWithTools(t *testing.T) {
	var lastRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		lastRequest = body
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})

	completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Complete without tools: %v", err)
	}
	if _, present := lastRequest["tool_choice"]; present {
		t.Errorf("tool_choice sent on a tools-less call: %v", lastRequest["tool_choice"])
	}

	tools := []ToolDef{{Name: "echo", Description: "echo", Parameters: map[string]any{"type": "object"}}}
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, tools); err != nil {
		t.Fatalf("Complete with tools: %v", err)
	}
	if got := lastRequest["tool_choice"]; got != "auto" {
		t.Errorf("tool_choice with tools = %v, want auto", got)
	}

	// Observability fields must be populated with hashes, never payloads.
	if completion.Provider != server.Listener.Addr().String() {
		t.Errorf("provider = %q, want %q", completion.Provider, server.Listener.Addr().String())
	}
	if len(completion.RequestRef) != len("sha256:")+64 || completion.RequestRef[:7] != "sha256:" {
		t.Errorf("request_ref = %q, want sha256:<64 hex>", completion.RequestRef)
	}
	if len(completion.ResponseRef) != len("sha256:")+64 || completion.ResponseRef[:7] != "sha256:" {
		t.Errorf("response_ref = %q, want sha256:<64 hex>", completion.ResponseRef)
	}
	if completion.RequestRef == completion.ResponseRef {
		t.Error("request and response payloads hashed to the same ref")
	}
	if completion.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", completion.Attempts)
	}
	if completion.Usage.TotalTokens != 5 || completion.Usage.PromptTokens != 3 || completion.Usage.CompletionTokens != 2 {
		t.Errorf("usage = %+v, want 3/2/5", completion.Usage)
	}
}

func TestCompleteRetriesTransientStatus(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"message":"upstream overloaded"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if calls != 2 {
		t.Errorf("provider calls = %d, want 2", calls)
	}
	if completion.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", completion.Attempts)
	}
	if completion.LatencyMs <= 0 {
		t.Errorf("latency_ms = %d, want > 0 (covers the retry backoff)", completion.LatencyMs)
	}
}

func TestCompleteFailsFastOnAuthError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected auth error")
	}
	if calls != 1 {
		t.Errorf("provider calls = %d, want 1 (no retries on 401)", calls)
	}
}
