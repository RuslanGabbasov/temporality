package agent

// Semantic knowledge layer (docs/supervisor-model.md §6): the embedding
// inference runs IN-PROCESS via the pure-Go rembed engine (no cgo, no ONNX
// Runtime, no separate service). The model — intfloat/multilingual-e5-small
// by default — is lazily loaded from the Hugging Face Hub into a persistent
// cache on kernel boot and served from RAM; vectors are published to the
// journal where hint matching consumes them as an extra recall tier.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rostamlabs/rembed"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
)

const (
	// EmbeddingModelDefault is the supervisor-pinned multilingual model:
	// 384 dims, 100+ languages, small enough for CPU-only inference.
	EmbeddingModelDefault = "intfloat/multilingual-e5-small"
	// embedCacheDirDefault keeps downloaded weights across restarts; the
	// kernel image provisions this path and compose persists it as a volume.
	embedCacheDirDefault = "/var/cache/rembed"
	// embedWorkersDefault caps inference CPU so encoding cannot starve the
	// Temporal worker goroutines sharing the process.
	embedWorkersDefault = 2
	// embedWaitTimeout bounds how long a hint-slot embed waits for the
	// background preload. Past it the call degrades to the lexical matcher —
	// the backfill loop retries publication later.
	embedWaitTimeout = 10 * time.Second
)

// EmbedKind selects the e5 input prefix. The e5 family was trained with
// "query: " on queries and "passage: " on documents; rembed embeds exactly
// the text it is given, so the kernel adds the prefixes itself — skipping
// them silently degrades retrieval quality (rembed README, e5 caveat). The
// prefixes apply when the model id names an e5 model.
type EmbedKind string

const (
	EmbedQuery   EmbedKind = "query"
	EmbedPassage EmbedKind = "passage"
)

// embedPrefix returns the e5 input prefix for the model and kind, "" when the
// model is not e5 (a non-e5 model must not get e5 prefixes).
func embedPrefix(model string, kind EmbedKind) string {
	if !strings.Contains(strings.ToLower(model), "e5") {
		return ""
	}
	switch kind {
	case EmbedQuery:
		return "query: "
	case EmbedPassage:
		return "passage: "
	}
	return ""
}

// embeddingModelID resolves the model id: env override first, the default
// multilingual model otherwise.
func embeddingModelID() string {
	if value := strings.TrimSpace(os.Getenv("KERNEL_EMBEDDINGS_MODEL")); value != "" {
		return value
	}
	return EmbeddingModelDefault
}

// embedCacheDir resolves where downloaded weights persist.
func embedCacheDir() string {
	if value := strings.TrimSpace(os.Getenv("REMBED_CACHE")); value != "" {
		return value
	}
	return embedCacheDirDefault
}

// embedWorkers caps inference CPU (env override, 0 = rembed default).
func embedWorkers() int {
	if value := strings.TrimSpace(os.Getenv("KERNEL_EMBEDDING_WORKERS")); value != "" {
		switch value {
		case "1":
			return 1
		case "2":
			return 2
		case "4":
			return 4
		case "8":
			return 8
		}
	}
	return embedWorkersDefault
}

// embedderState tracks the lazy model load as an explicit state machine
// (docs/fixes/embeddings-load-lifecycle): at most one attempt is in flight,
// every attempt — success or failure — settles its own channel so waiters
// learn the real outcome immediately, and a failed attempt schedules a
// bounded exponential auto-retry that needs no caller to fire.
type embedAttempt struct {
	ready    chan struct{} // closed when this attempt settles (success OR failure)
	embedder *rembed.Embedder
	err      error // valid once ready is closed; nil on success
}

type embedderState struct {
	embedder *rembed.Embedder // set once a load succeeds; terminal
	attempt  *embedAttempt    // current in-flight or last settled attempt
	loading  bool             // an attempt is in flight (guarded by embedMu)
	failures int              // consecutive failed attempts; drives the retry backoff
	// lastErr keeps the most recent settled failure across in-flight
	// retries so the status keeps reporting the real cause until a load
	// finally succeeds.
	lastErr error
	// retryTimer fires the automatic retry after a failure; nil when none is
	// scheduled. Stopped whenever a new attempt starts so a caller-forced
	// immediate retry supersedes the pending backoff wait.
	retryTimer *time.Timer
	// retrySeq invalidates stale retry timers: every new attempt or newly
	// scheduled retry bumps it, so a callback captured with an older seq is
	// a leftover from a superseded generation and must not fire a load.
	retrySeq uint64
}

// Auto-retry backoff after a failed load (R3): starts at embedRetryBase and
// doubles per consecutive failure up to embedRetryMax. Vars, not consts, so
// tests can shrink them.
var (
	embedRetryBase = 30 * time.Second
	embedRetryMax  = 5 * time.Minute
)

// embedRetryBackoff returns the delay before the next automatic retry given
// the number of consecutive failures (>= 1).
func embedRetryBackoff(failures int) time.Duration {
	delay := embedRetryBase
	for i := 1; i < failures; i++ {
		delay *= 2
		if delay >= embedRetryMax {
			return embedRetryMax
		}
	}
	if delay > embedRetryMax {
		return embedRetryMax
	}
	return delay
}

// loadEmbedder is the model loader seam: production wires rembed.Load, the
// unit tests substitute a fake to drive the state machine without the Hub.
var loadEmbedder = rembed.Load

var (
	embedMu    sync.Mutex
	embedState embedderState
)

// getEmbedder returns the loaded embedder, waiting for an in-flight load.
// Kicks off the load when none is in flight yet; a settled failure is
// retried immediately by the caller re-invoking — the external call skips
// the remaining backoff, while the automatic retry covers the no-caller case.
// A failed attempt surfaces its real error at once: the embedWaitTimeout
// text only ever applies to a load that is genuinely still in flight.
func (a *Activities) getEmbedder(ctx context.Context) (*rembed.Embedder, error) {
	embedMu.Lock()
	if emb := embedState.embedder; emb != nil {
		embedMu.Unlock()
		return emb, nil
	}
	if !embedState.loading {
		// Nobody is loading: start (or immediately re-attempt) the load.
		a.startEmbeddingAttemptLocked()
	}
	attempt := embedState.attempt
	embedMu.Unlock()

	select {
	case <-attempt.ready:
		if attempt.err != nil {
			return nil, attempt.err
		}
		return attempt.embedder, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(embedWaitTimeout):
		return nil, fmt.Errorf("embedding model is still loading — retry shortly")
	}
}

// startEmbeddingAttemptLocked begins one load attempt in the background;
// embedMu must be held and the caller must have verified that none is in
// flight. Any pending automatic retry is cancelled: this attempt supersedes
// it and will schedule the next one if it fails too.
func (a *Activities) startEmbeddingAttemptLocked() {
	if embedState.retryTimer != nil {
		embedState.retryTimer.Stop()
		embedState.retryTimer = nil
	}
	model := embeddingModelID()
	workers := embedWorkers()
	cacheDir := embedCacheDir()
	_ = os.Setenv("REMBED_CACHE", cacheDir)
	attempt := &embedAttempt{ready: make(chan struct{})}
	embedState.attempt = attempt
	embedState.loading = true
	embedState.retrySeq++
	seq := embedState.retrySeq
	go func() {
		started := time.Now()
		opts := []rembed.Option{rembed.WithInt8()}
		if workers > 0 {
			opts = append(opts, rembed.WithWorkers(workers))
		}
		emb, err := loadEmbedder(model, opts...)
		embedMu.Lock()
		defer embedMu.Unlock()
		embedState.loading = false
		attempt.embedder, attempt.err = emb, err
		if err != nil {
			// Retryable: the automatic retry below re-attempts the load
			// whether or not any caller ever asks again (D4).
			embedState.lastErr = err
			embedState.failures++
			delay := embedRetryBackoff(embedState.failures)
			embedState.retryTimer = time.AfterFunc(delay, func() { a.retryEmbeddingLoad(seq) })
			slog.Error("embedding model load failed",
				"model", model,
				"cache_dir", cacheDir,
				"error", err.Error(),
				"duration", time.Since(started).String(),
				"attempt", embedState.failures,
				"retry_in", delay.String())
		} else {
			embedState.embedder = emb
			embedState.failures = 0
			embedState.lastErr = nil
			slog.Info("embedding model loaded",
				"model", model,
				"cache_dir", cacheDir,
				"duration", time.Since(started).String())
		}
		// Settle the attempt on both paths so waiters get the real outcome
		// immediately instead of hanging until embedWaitTimeout (D1).
		close(attempt.ready)
	}()
}

// retryEmbeddingLoad is the automatic retry scheduled after a failed
// attempt. A callback carrying a stale generation seq is a leftover from a
// superseded schedule and does nothing; so does one that finds the model
// loaded or another attempt in flight — at most one load is ever in flight.
func (a *Activities) retryEmbeddingLoad(seq uint64) {
	embedMu.Lock()
	defer embedMu.Unlock()
	if seq != embedState.retrySeq {
		return
	}
	embedState.retryTimer = nil
	if embedState.embedder != nil || embedState.loading {
		return
	}
	a.startEmbeddingAttemptLocked()
}

// EmbeddingsStatus is the operator-facing health of the in-process semantic
// layer (docs/supervisor-model.md §6.5): when the model is not ready, hints
// silently degrade to the lexical tiers — this makes the degradation
// visible instead of discovered.
type EmbeddingsStatus struct {
	Model string `json:"model"`
	Ready bool   `json:"ready"`
	Error string `json:"error,omitempty"`
}

// EmbeddingsStatus reports the current semantic-layer state. A failed
// attempt surfaces its real error (never masked as "load not attempted"),
// so the readiness detail shows why the model is missing (D2).
func (a *Activities) EmbeddingsStatus() EmbeddingsStatus {
	embedMu.Lock()
	defer embedMu.Unlock()
	status := EmbeddingsStatus{Model: embeddingModelID()}
	switch {
	case embedState.embedder != nil:
		status.Ready = true
	case embedState.lastErr != nil:
		// A settled attempt failed and no load has succeeded since; report
		// the real cause (even while a retry is already in flight) instead
		// of masking the failure as "load not attempted" (D2).
		status.Error = embedState.lastErr.Error()
	case embedState.loading:
		status.Error = "model is loading"
	default:
		status.Error = "load not attempted"
	}
	return status
}

// StartEmbeddingPreload kicks off the model load in the background at kernel
// boot so the first hint slot does not pay the Hub download. Safe to call at
// any time: it is a no-op while the model is loading or already loaded, and
// after a failure it retries immediately, skipping the backoff.
func (a *Activities) StartEmbeddingPreload() {
	embedMu.Lock()
	defer embedMu.Unlock()
	if embedState.embedder != nil || embedState.loading {
		return
	}
	a.startEmbeddingAttemptLocked()
}

// EmbedTexts computes embeddings for the texts in-process. kind selects the
// e5 input prefix. On any failure it returns an error — callers degrade to
// the lexical matcher.
func (a *Activities) EmbedTexts(ctx context.Context, project string, kind EmbedKind, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	model := embeddingModelID()
	if prefix := embedPrefix(model, kind); prefix != "" {
		texts = append([]string(nil), texts...)
		for i := range texts {
			if !strings.HasPrefix(texts[i], prefix) {
				texts[i] = prefix + texts[i]
			}
		}
	}
	embedder, err := a.getEmbedder(ctx)
	if err != nil {
		return nil, err
	}
	vectors, err := embedder.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embedding inference: %w", err)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("embedding engine returned %d vectors for %d inputs", len(vectors), len(texts))
	}
	return vectors, nil
}

// SupervisorEmbeddingModel returns the configured embeddings model id — used
// by the backfill loop for the journal model stamp and gap queries.
func (a *Activities) SupervisorEmbeddingModel(ctx context.Context) string {
	return embeddingModelID()
}

// publishKnowledgeEmbeddings pushes computed vectors to the journal. The
// journal gates on writer; failures degrade to a log line — the backfill loop
// re-embeds the same ids on a later pass.
func (a *Activities) publishKnowledgeEmbeddings(ctx context.Context, model string, items map[string][]float32) error {
	payload, err := json.Marshal(map[string]any{"model": model, "items": items})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimSuffix(a.TemporalityURL, "/")+"/v1/observations/knowledge/embeddings", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if a.apiToken() != "" {
		request.Header.Set("Authorization", "Bearer "+a.apiToken())
	}
	response, err := a.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 200))
		return fmt.Errorf("journal embeddings HTTP %d: %s", response.StatusCode, string(body))
	}
	return nil
}

// embedLogger resolves the activity logger when inside an activity context;
// nil elsewhere (background loops pass their own logging).
func (a *Activities) embedLogger(ctx context.Context) log.Logger {
	if activity.IsActivity(ctx) {
		return activity.GetLogger(ctx)
	}
	return nil
}
