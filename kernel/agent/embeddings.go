package agent

// Semantic knowledge layer client (docs/supervisor-model.md §6): the kernel
// owns the provider keys, so it computes knowledge embeddings with the
// supervisor embeddings model (OpenAI-compatible POST /v1/embeddings, model
// routed through the same provider registry as chat calls) and pushes them to
// the journal, where hint matching consumes them as an extra recall tier.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
)

const (
	// embedMaxBatch caps one /v1/embeddings call; larger sets are split.
	embedMaxBatch = 64
	// embedTimeout bounds one provider call; embeddings are small and fast,
	// but a wedged gateway must not hang the hints path.
	embedTimeout = 30 * time.Second
)

// EmbedKind selects the e5 input prefix. The e5 family was trained with
// "query: " on queries and "passage: " on documents; rembed embeds exactly
// the text it is given, so the kernel adds the prefixes itself — skipping
// them silently degrades retrieval quality (rembed README, e5 caveat).
type EmbedKind string

const (
	EmbedQuery   EmbedKind = "query"
	EmbedPassage EmbedKind = "passage"
)

func (k EmbedKind) prefix() string {
	switch k {
	case EmbedQuery:
		return "query: "
	case EmbedPassage:
		return "passage: "
	}
	return ""
}

// EmbedTexts computes embeddings for the texts with the LOCAL embeddings
// model (rembed serving multilingual-e5-small, always up with the stack). The
// endpoint is fixed by env (KERNEL_EMBEDDINGS_BASE_URL / _MODEL / _API_KEY) —
// embeddings are infrastructure, not a provider choice, so no keys or routing
// apply. kind selects the e5 input prefix ("query" for hint slots, "passage"
// for stored knowledge; empty for prefix-agnostic models).
func (a *Activities) EmbedTexts(ctx context.Context, project string, kind EmbedKind, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	config := embeddingsConfig()
	prefix := kind.prefix()
	if prefix != "" {
		texts = append([]string(nil), texts...)
		for i := range texts {
			if !strings.HasPrefix(texts[i], prefix) {
				texts[i] = prefix + texts[i]
			}
		}
	}
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += embedMaxBatch {
		end := start + embedMaxBatch
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := a.embedBatch(ctx, config, texts[start:end])
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, batch...)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("embeddings provider returned %d vectors for %d inputs", len(vectors), len(texts))
	}
	return vectors, nil
}

func (a *Activities) embedBatch(ctx context.Context, config embedConfig, texts []string) ([][]float32, error) {
	requestCtx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	payload, err := json.Marshal(map[string]any{"model": config.model, "input": texts})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimSuffix(config.baseURL, "/")+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if config.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+config.apiKey)
	}
	response, err := a.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings HTTP %d: %s", response.StatusCode, truncate(string(body), 200))
	}
	var reply struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, fmt.Errorf("decode embeddings reply: %w", err)
	}
	vectors := make([][]float32, 0, len(reply.Data))
	for _, item := range reply.Data {
		vectors = append(vectors, item.Embedding)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("embeddings provider returned %d vectors for %d inputs", len(vectors), len(texts))
	}
	return vectors, nil
}

// embedConfig is the resolved local embeddings endpoint.
type embedConfig struct {
	baseURL string
	model   string
	apiKey  string
}

// embeddingsConfig reads the local embeddings endpoint from env, with the
// compose defaults baked in (service `embeddings`, model `qwen3-embedding`).
func embeddingsConfig() embedConfig {
	return embedConfig{
		baseURL: envWithDefault("KERNEL_EMBEDDINGS_BASE_URL", "http://embeddings:11434/v1"),
		model:   envWithDefault("KERNEL_EMBEDDINGS_MODEL", "qwen3-embedding"),
		apiKey:  os.Getenv("KERNEL_EMBEDDINGS_API_KEY"),
	}
}

func envWithDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// publishKnowledgeEmbeddings pushes computed vectors to the journal. The
// journal gates on writer; failures degrade to a log line — the backfill loop
// will re-embed the same ids on a later pass.
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

// SupervisorEmbeddingModel returns the local embeddings model name — the
// same one EmbedTexts targets. Used by the backfill loop for the model stamp
// and by the journal gaps query.
func (a *Activities) SupervisorEmbeddingModel(ctx context.Context) string {
	return embeddingsConfig().model
}
