package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderConcurrencyLimitIsSharedByClients(t *testing.T) {
	var active, peak atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		defer active.Add(-1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	const calls = 6
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := New(Config{BaseURL: server.URL, Model: "m", ProviderID: "shared-provider", MaxConcurrentRequests: 2})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := client.Complete(ctx, []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
				t.Errorf("Complete: %v", err)
			}
		}()
	}

	deadline := time.After(2 * time.Second)
	for peak.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("limiter never admitted two requests; peak=%d", peak.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case <-time.After(50 * time.Millisecond):
		if got := peak.Load(); got > 2 {
			t.Fatalf("peak concurrency = %d, want at most 2", got)
		}
	case <-deadline:
		t.Fatal("timed out waiting for requests")
	}
	close(release)
	wg.Wait()
}
