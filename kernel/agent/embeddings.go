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

// embedderState guards the lazy one-time model load. A failure is retried on
// the next EmbedTexts call — a transient Hub outage must not brick the
// feature for the process lifetime.
type embedderState struct {
	mu       sync.Mutex
	embedder *rembed.Embedder
	ready    chan struct{} // closed on load success OR failure
	err      error
}

var (
	embedMu    sync.Mutex
	embedState embedderState
)

// getEmbedder returns the loaded embedder, waiting for an in-flight load.
// Kicks off the load when nobody has attempted it yet; a failed attempt is
// retried by the caller re-invoking after embedWaitTimeout.
func (a *Activities) getEmbedder(ctx context.Context) (*rembed.Embedder, error) {
	embedMu.Lock()
	if embedState.ready == nil {
		// Nobody attempted yet: kick off the preload in the background and
		// wait for it below, bounded by the caller's deadline.
		a.startEmbeddingPreloadLocked()
	}
	ready := embedState.ready
	embedMu.Unlock()

	select {
	case <-ready:
		embedMu.Lock()
		defer embedMu.Unlock()
		if embedState.err != nil {
			// Allow a retry on the next call.
			embedMu.Lock()
			embedState.ready = nil
			embedMu.Unlock()
			return nil, embedState.err
		}
		return embedState.embedder, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(embedWaitTimeout):
		return nil, fmt.Errorf("embedding model is still loading — retry shortly")
	}
}

// startEmbeddingPreloadLocked begins the model load; embedMu must be held.
func (a *Activities) startEmbeddingPreloadLocked() {
	embedState.ready = make(chan struct{})
	model := embeddingModelID()
	workers := embedWorkers()
	cacheDir := embedCacheDir()
	_ = os.Setenv("REMBED_CACHE", cacheDir)
	go func() {
		opts := []rembed.Option{rembed.WithInt8()}
		if workers > 0 {
			opts = append(opts, rembed.WithWorkers(workers))
		}
		emb, err := rembed.Load(model, opts...)
		embedMu.Lock()
		defer embedMu.Unlock()
		embedState.err = err
		if err != nil {
			// Retryable: the next getEmbedder call re-attempts the load.
			embedState.ready = nil
			return
		}
		embedState.embedder = emb
		close(embedState.ready)
	}()
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

// EmbeddingsStatus reports the current semantic-layer state.
func (a *Activities) EmbeddingsStatus() EmbeddingsStatus {
	embedMu.Lock()
	defer embedMu.Unlock()
	status := EmbeddingsStatus{Model: embeddingModelID()}
	if embedState.ready != nil {
		select {
		case <-embedState.ready:
			status.Ready = embedState.err == nil
			if embedState.err != nil {
				status.Error = embedState.err.Error()
			}
		default:
			status.Error = "model is loading"
		}
	} else {
		status.Error = "load not attempted"
	}
	return status
}

// StartEmbeddingPreload kicks off the model load in the background at kernel
// boot so the first hint slot does not pay the Hub download.
func (a *Activities) StartEmbeddingPreload() {
	embedMu.Lock()
	defer embedMu.Unlock()
	if embedState.ready != nil {
		return
	}
	a.startEmbeddingPreloadLocked()
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
	if a.APIToken != "" {
		request.Header.Set("Authorization", "Bearer "+a.APIToken)
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
