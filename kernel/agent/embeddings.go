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
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
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

// EmbedTexts computes embeddings for the texts with the supervisor embeddings
// model, routed through the project's provider registry. Returns one vector
// per input, in order.
func (a *Activities) EmbedTexts(ctx context.Context, project string, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	model, _ := a.supervisorModelsFor(ctx)
	if model == "" {
		return nil, fmt.Errorf("embeddings model is not configured — pick one in Agents → Модель-супервайзер")
	}
	// Provider routing shares the chat path: resolve base URL + key for the
	// model (workspace registry first, env fallback).
	config, err := a.embeddingsConfig(ctx, project, model)
	if err != nil {
		return nil, err
	}
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += embedMaxBatch {
		end := start + embedMaxBatch
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := a.embedBatch(ctx, config, model, texts[start:end])
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

func (a *Activities) embedBatch(ctx context.Context, config embedConfig, model string, texts []string) ([][]float32, error) {
	requestCtx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	payload, err := json.Marshal(map[string]any{"model": model, "input": texts})
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

// embedConfig is the resolved endpoint for one embeddings model.
type embedConfig struct {
	baseURL string
	apiKey  string
}

// embeddingsConfig resolves base URL and key for the model: the workspace
// provider registry first (same routing as chat), env fallback otherwise.
func (a *Activities) embeddingsConfig(ctx context.Context, project, model string) (embedConfig, error) {
	if a.WorkspaceURL != "" {
		if config, ok, err := a.modelConfigFor(ctx, project, model); err != nil {
			return embedConfig{}, err
		} else if ok {
			return embedConfig{baseURL: config.BaseURL, apiKey: config.APIKey}, nil
		}
	}
	fallback, err := llm.OptionalConfigFromEnv()
	if err != nil {
		return embedConfig{}, err
	}
	return embedConfig{baseURL: fallback.BaseURL, apiKey: fallback.APIKey}, nil
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

// SupervisorEmbeddingModel returns the panel's embeddings model ("" = off).
// Exported for the kernel's background backfill loop, which runs outside the
// activities package.
func (a *Activities) SupervisorEmbeddingModel(ctx context.Context) string {
	_, embedding := a.supervisorModelsFor(ctx)
	return embedding
}
