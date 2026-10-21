package agent

// Unit tests for the embedding model load lifecycle state machine
// (docs/fixes/embeddings-load-lifecycle, R5): the loader seam
// (loadEmbedder) is substituted with a fake, so every path — success,
// failure, auto-retry, waiter wakeup — runs without the Hub.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rostamlabs/rembed"
)

// resetEmbedderState zeroes the package-level load state before a test and
// after it (stopping a pending retry timer), so tests never inherit each
// other's attempts, embedders or schedules.
func resetEmbedderState(t *testing.T) {
	t.Helper()
	reset := func() {
		embedMu.Lock()
		if embedState.retryTimer != nil {
			embedState.retryTimer.Stop()
		}
		embedState = embedderState{}
		embedMu.Unlock()
	}
	reset()
	orig := loadEmbedder
	t.Cleanup(func() {
		reset()
		loadEmbedder = orig
	})
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, deadline time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestEmbeddingLoadSuccess(t *testing.T) {
	resetEmbedderState(t)
	t.Setenv("KERNEL_EMBEDDINGS_MODEL", "test/model")
	fake := &rembed.Embedder{}
	var calls int
	loadEmbedder = func(ref string, opts ...rembed.Option) (*rembed.Embedder, error) {
		calls++
		if ref != "test/model" {
			t.Errorf("loader got model %q, want %q", ref, "test/model")
		}
		return fake, nil
	}

	a := &Activities{}
	emb, err := a.getEmbedder(context.Background())
	if err != nil {
		t.Fatalf("getEmbedder: %v", err)
	}
	if emb != fake {
		t.Fatalf("getEmbedder returned %p, want the loaded embedder %p", emb, fake)
	}
	status := a.EmbeddingsStatus()
	if !status.Ready {
		t.Errorf("status.Ready = false after a successful load")
	}
	if status.Error != "" {
		t.Errorf("status.Error = %q, want empty on success", status.Error)
	}
	if status.Model != "test/model" {
		t.Errorf("status.Model = %q, want %q", status.Model, "test/model")
	}
	if calls != 1 {
		t.Errorf("loader called %d times, want exactly 1", calls)
	}
}

// TestEmbeddingLoadFailureStatusAndImmediateError covers D1/D2: a failed
// attempt surfaces its real error in the status (never masked as
// "load not attempted"), waiters get it immediately — not after
// embedWaitTimeout — and a follow-up call retries right away.
func TestEmbeddingLoadFailureStatusAndImmediateError(t *testing.T) {
	resetEmbedderState(t)
	t.Setenv("KERNEL_EMBEDDINGS_MODEL", "test/model")
	loadErr := errors.New("hub unreachable: 401 unauthorized")
	var mu sync.Mutex
	var calls int
	loadEmbedder = func(string, ...rembed.Option) (*rembed.Embedder, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil, loadErr
	}
	// Park the auto-retry far out so this test observes a single settled
	// failure without a background second attempt racing the assertions.
	embedMu.Lock()
	embedRetryBase, embedRetryMax = time.Hour, time.Hour
	embedMu.Unlock()
	defer func() {
		embedMu.Lock()
		embedRetryBase, embedRetryMax = 30*time.Second, 5*time.Minute
		embedMu.Unlock()
	}()

	a := &Activities{}
	a.StartEmbeddingPreload()

	// The waiter must receive the real error promptly, not hang on the
	// closed-never channel until embedWaitTimeout (D1).
	start := time.Now()
	_, err := a.getEmbedder(context.Background())
	elapsed := time.Since(start)
	if err == nil || !errors.Is(err, loadErr) {
		t.Fatalf("getEmbedder err = %v, want the real load error", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("waiter blocked %v after the load failed — must return immediately", elapsed)
	}

	status := a.EmbeddingsStatus()
	if status.Ready {
		t.Errorf("status.Ready = true after a failed load")
	}
	if status.Error == "load not attempted" {
		t.Fatalf("failed attempt masked as %q (D2)", status.Error)
	}
	if !strings.Contains(status.Error, "401 unauthorized") {
		t.Errorf("status.Error = %q, want it to carry the real load error", status.Error)
	}

	// An external call after a settled failure retries immediately (R3):
	// no waiting out a backoff, and the error surfaces again at once.
	start = time.Now()
	_, err = a.getEmbedder(context.Background())
	elapsed = time.Since(start)
	if err == nil || !errors.Is(err, loadErr) {
		t.Fatalf("retry getEmbedder err = %v, want the real load error", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("retry blocked %v — a caller-forced retry must not wait out the backoff", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Errorf("loader called %d times, want the follow-up call to re-attempt the load", calls)
	}
}

// TestEmbeddingWaiterGetsFailureImmediately is the D1 regression test in its
// sharpest form: a caller parked mid-load must be released the moment the
// attempt fails, with the real cause.
func TestEmbeddingWaiterGetsFailureImmediately(t *testing.T) {
	resetEmbedderState(t)
	t.Setenv("KERNEL_EMBEDDINGS_MODEL", "test/model")
	loadErr := errors.New("model shard corrupt")
	release := make(chan struct{})
	loading := make(chan struct{})
	var once sync.Once
	loadEmbedder = func(string, ...rembed.Option) (*rembed.Embedder, error) {
		once.Do(func() { close(loading) })
		<-release
		return nil, loadErr
	}
	embedMu.Lock()
	embedRetryBase, embedRetryMax = time.Hour, time.Hour
	embedMu.Unlock()
	defer func() {
		embedMu.Lock()
		embedRetryBase, embedRetryMax = 30*time.Second, 5*time.Minute
		embedMu.Unlock()
	}()

	a := &Activities{}
	a.StartEmbeddingPreload()
	<-loading // the attempt is in flight

	type result struct {
		err     error
		elapsed time.Duration
	}
	results := make(chan result, 1)
	go func() {
		start := time.Now()
		_, err := a.getEmbedder(context.Background())
		results <- result{err, time.Since(start)}
	}()
	time.Sleep(100 * time.Millisecond) // let the waiter park on the attempt channel
	close(release)                     // the attempt now fails

	select {
	case r := <-results:
		if r.err == nil || !errors.Is(r.err, loadErr) {
			t.Fatalf("waiter err = %v, want the real load error", r.err)
		}
		if r.elapsed > 2*time.Second {
			t.Fatalf("waiter released after %v — must be immediate, not the %v timeout", r.elapsed, embedWaitTimeout)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter still blocked well past the failed attempt — ready channel not closed on failure (D1)")
	}
}

// TestEmbeddingAutoRetryWithoutCallers covers D4/R3: after a failure the
// load re-attempts on its own with the backoff, no EmbedTexts/Start call
// involved, and a later success clears the failure.
func TestEmbeddingAutoRetryWithoutCallers(t *testing.T) {
	resetEmbedderState(t)
	t.Setenv("KERNEL_EMBEDDINGS_MODEL", "test/model")
	embedMu.Lock()
	embedRetryBase, embedRetryMax = 30*time.Millisecond, 120*time.Millisecond
	embedMu.Unlock()
	defer func() {
		embedMu.Lock()
		embedRetryBase, embedRetryMax = 30*time.Second, 5*time.Minute
		embedMu.Unlock()
	}()
	fake := &rembed.Embedder{}
	var mu sync.Mutex
	var calls int
	loadEmbedder = func(string, ...rembed.Option) (*rembed.Embedder, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return nil, errors.New("transient hub outage")
		}
		return fake, nil
	}

	a := &Activities{}
	a.StartEmbeddingPreload() // the only external call — boot preload
	waitFor(t, "the automatic retry to load the model", 5*time.Second, func() bool {
		return a.EmbeddingsStatus().Ready
	})
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("loader called %d times, want 1 failure + 1 automatic retry", calls)
	}
	if got := a.EmbeddingsStatus(); got.Error != "" {
		t.Errorf("status.Error = %q after recovery, want empty", got.Error)
	}
}

// TestGetEmbedderNoDeadlockOnFailure is the D3 regression test: the error
// branch used to take embedMu a second time while already holding it. The
// call must return the load error, promptly.
func TestGetEmbedderNoDeadlockOnFailure(t *testing.T) {
	resetEmbedderState(t)
	t.Setenv("KERNEL_EMBEDDINGS_MODEL", "test/model")
	loadErr := errors.New("dns failure for huggingface.co")
	loadEmbedder = func(string, ...rembed.Option) (*rembed.Embedder, error) {
		return nil, loadErr
	}
	embedMu.Lock()
	embedRetryBase, embedRetryMax = time.Hour, time.Hour
	embedMu.Unlock()
	defer func() {
		embedMu.Lock()
		embedRetryBase, embedRetryMax = 30*time.Second, 5*time.Minute
		embedMu.Unlock()
	}()

	a := &Activities{}
	done := make(chan error, 1)
	go func() {
		// First call fails; the follow-up enters the error branch that used
		// to deadlock on the nested embedMu.Lock.
		if _, err := a.getEmbedder(context.Background()); err == nil {
			done <- errors.New("first getEmbedder unexpectedly succeeded")
			return
		}
		_, err := a.getEmbedder(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, loadErr) {
			t.Fatalf("getEmbedder after failure err = %v, want the real load error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("getEmbedder deadlocked in the failure branch (D3)")
	}
}

// TestEmbeddingSingleLoadInFlight: however many callers arrive mid-load,
// exactly one loader invocation is in flight (R2).
func TestEmbeddingSingleLoadInFlight(t *testing.T) {
	resetEmbedderState(t)
	t.Setenv("KERNEL_EMBEDDINGS_MODEL", "test/model")
	release := make(chan struct{})
	var mu sync.Mutex
	var active, peak int
	loadEmbedder = func(string, ...rembed.Option) (*rembed.Embedder, error) {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		return &rembed.Embedder{}, nil
	}

	a := &Activities{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = a.getEmbedder(context.Background())
		}()
	}
	waitFor(t, "the load to start", time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return active == 1
	})
	close(release)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if peak != 1 {
		t.Errorf("peak concurrent loads = %d, want exactly 1", peak)
	}
}

// TestEmbedRetryBackoffGrowth pins the bounded exponential schedule (R3).
func TestEmbedRetryBackoffGrowth(t *testing.T) {
	embedMu.Lock()
	embedRetryBase, embedRetryMax = 30*time.Second, 5*time.Minute
	embedMu.Unlock()
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{1, 30 * time.Second},
		{2, 60 * time.Second},
		{3, 120 * time.Second},
		{4, 240 * time.Second},
		{5, 300 * time.Second}, // capped at 5 minutes
		{9, 300 * time.Second},
	}
	for _, c := range cases {
		if got := embedRetryBackoff(c.failures); got != c.want {
			t.Errorf("embedRetryBackoff(%d) = %v, want %v", c.failures, got, c.want)
		}
	}
}

// TestEmbeddingsStatusJSONContract pins the operator-facing JSON keys (R6).
func TestEmbeddingsStatusJSONContract(t *testing.T) {
	raw, err := json.Marshal(EmbeddingsStatus{Model: "m", Ready: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"model", "ready"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("JSON contract lost key %q", key)
		}
	}
	if _, ok := keys["error"]; ok {
		t.Errorf("empty error must stay omitted")
	}
	raw, err = json.Marshal(EmbeddingsStatus{Model: "m", Error: "boom"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if keys["error"] != "boom" {
		t.Errorf("JSON contract lost key \"error\"")
	}
}
