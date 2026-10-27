package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestParallelToolCallsDisabledWhenToolsPresent(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		captured = body
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"content": "hi"},
			}},
		})
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := New(Config{BaseURL: server.URL, Model: "m", MaxOutputTokens: 64, Timeout: 5 * time.Second})
	_, err := client.Complete(ctx, []Message{{Role: "user", Content: "hi"}}, []ToolDef{{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	json.Unmarshal(captured, &decoded)
	if decoded["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls should be false when tools present, got %v", decoded["parallel_tool_calls"])
	}
}

func TestParallelToolCallsOmittedWhenNoTools(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		captured = body
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"content": "hi"},
			}},
		})
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := New(Config{BaseURL: server.URL, Model: "m", MaxOutputTokens: 64, Timeout: 5 * time.Second})
	_, err := client.Complete(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	json.Unmarshal(captured, &decoded)
	if _, present := decoded["parallel_tool_calls"]; present {
		t.Fatalf("parallel_tool_calls should be omitted without tools, got %v", decoded["parallel_tool_calls"])
	}
}

func TestMalformedArgumentsSalvage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"content": "done",
					"tool_calls": []any{
						map[string]any{
							"id": "c1", "type": "function",
							"function": map[string]any{"name": "broken", "arguments": "not json{{"},
						},
						map[string]any{
							"id": "c2", "type": "function",
							"function": map[string]any{"name": "good", "arguments": `{"x":1}`},
						},
					},
				},
			}},
		})
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := New(Config{BaseURL: server.URL, Model: "m", MaxOutputTokens: 64, Timeout: 5 * time.Second})
	result, err := client.Complete(ctx, []Message{{Role: "user", Content: "go"}}, []ToolDef{{Name: "t"}})
	if err != nil {
		t.Fatalf("malformed call must not fail the completion: %v", err)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected 2 salvaged calls, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].ArgsError == "" {
		t.Fatal("first call should have ArgsError set")
	}
	if result.ToolCalls[0].Name != "broken" {
		t.Fatalf("first call name = %q", result.ToolCalls[0].Name)
	}
	if result.ToolCalls[1].ArgsError != "" {
		t.Fatalf("second call should parse clean, got ArgsError=%q", result.ToolCalls[1].ArgsError)
	}
	if result.ToolCalls[1].Args["x"].(float64) != 1 {
		t.Fatalf("second call args = %v", result.ToolCalls[1].Args)
	}
}

// Complete from one shared client must be safe under concurrency: the retry
// backoff used to draw jitter from a per-client *rand.Rand, which raced when
// several goroutines retried at once. The server 503s everything in the
// first 100ms, so every goroutine eats one transient failure and retries —
// under -race this is exactly the interleaving that used to trip the
// detector; the retries themselves bound the run to the usual ~4s backoff.
func TestCompleteConcurrentTransientRetries(t *testing.T) {
	startedAt := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if time.Since(startedAt) < 100*time.Millisecond {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"overloaded"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	const goroutines = 8
	var wg sync.WaitGroup
	completions := make([]Completion, goroutines)
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			completions[i], errs[i] = client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: Complete: %v", i, errs[i])
		}
		if completions[i].Content != "ok" {
			t.Fatalf("goroutine %d: content = %q", i, completions[i].Content)
		}
	}
}
