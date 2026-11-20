package llm

// The usage hook is the observability seam the supervisor spend dashboard
// reads: background judge/supervisor passes call the model outside any run,
// so their tokens would otherwise be invisible — no workspace_run row, no
// model.completed event. SetUsageHook + observeUsage journal them instead.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUsageHookObservesSuccessfulCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL + "/v1", Model: "judge-model", Timeout: 5 * time.Second})
	var got []UsageEvent
	client.SetUsageHook(func(_ context.Context, event UsageEvent) {
		got = append(got, event)
	})

	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(got))
	}
	event := got[0]
	if event.Model != "judge-model" {
		t.Fatalf("model = %q", event.Model)
	}
	if event.Usage.PromptTokens != 3 || event.Usage.CompletionTokens != 2 {
		t.Fatalf("usage = %+v", event.Usage)
	}
}

// A failed call spent nothing worth accounting: the hook must stay silent.
// 401 is deliberate — non-transient statuses return on the first attempt
// (transient 5xx retries until the context dies, which would just park the
// test in the backoff select).
func TestUsageHookSkipsFailedCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL + "/v1", Model: "judge-model", Timeout: 5 * time.Second})
	fired := 0
	client.SetUsageHook(func(_ context.Context, _ UsageEvent) { fired++ })

	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("Complete must fail against a 401 upstream")
	}
	if fired != 0 {
		t.Fatalf("hook fired %d times on failures, want 0", fired)
	}
}

// A client without a hook behaves exactly as before (nil deref guard).
func TestUsageHookNilIsSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL + "/v1", Model: "m", Timeout: 5 * time.Second})
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}
