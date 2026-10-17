package main

// Knowledge embedding backfill (docs/supervisor-model.md §6): a background
// loop that asks the journal for knowledge still missing a vector for the
// supervisor embeddings model, embeds the propositions (the kernel owns the
// provider keys) and publishes the batch. Best-effort on every tick; the
// journal's gaps query naturally re-reports anything that failed.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
)

const embeddingBackfillInterval = 5 * time.Minute

func startEmbeddingBackfillLoop(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL, journalToken string) {
	go func() {
		// First pass shortly after boot so a freshly configured model fills
		// the backlog without waiting a full interval.
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		runEmbeddingBackfill(ctx, log, activities, observationURL, journalToken)
		ticker := time.NewTicker(embeddingBackfillInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runEmbeddingBackfill(ctx, log, activities, observationURL, journalToken)
			}
		}
	}()
}

func runEmbeddingBackfill(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL, journalToken string) {
	model := activities.SupervisorEmbeddingModel(ctx)
	if model == "" {
		return // supervisor embeddings off — nothing to do
	}
	gaps, err := fetchEmbeddingGaps(ctx, observationURL, journalToken, model)
	if err != nil {
		log.Warn("embedding backfill: list gaps", "error", err.Error())
		return
	}
	if len(gaps) == 0 {
		return
	}
	ids := make([]string, 0, len(gaps))
	texts := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		ids = append(ids, gap.KnowledgeID)
		texts = append(texts, gap.Text)
	}
	vectors, err := activities.EmbedTexts(ctx, "", texts)
	if err != nil {
		log.Warn("embedding backfill: embed", "error", err.Error())
		return
	}
	items := make(map[string][]float32, len(ids))
	for i, vector := range vectors {
		if i < len(ids) && len(vector) > 0 {
			items[ids[i]] = vector
		}
	}
	if len(items) == 0 {
		return
	}
	if err := publishKnowledgeEmbeddings(ctx, observationURL, journalToken, model, items); err != nil {
		log.Warn("embedding backfill: publish", "error", err.Error())
		return
	}
	log.Info("knowledge embeddings backfilled", "count", len(items), "model", model)
}

func fetchEmbeddingGaps(ctx context.Context, observationURL, journalToken, model string) ([]observation.KnowledgeEmbeddingGap, error) {
	fetchURL := strings.TrimSuffix(observationURL, "/") + "/v1/observations/knowledge/embeddings/missing?model=" + url.QueryEscape(model) + "&limit=256"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	if journalToken != "" {
		request.Header.Set("Authorization", "Bearer "+journalToken)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("journal HTTP %d: %s", response.StatusCode, string(body))
	}
	var page struct {
		Gaps []struct {
			KnowledgeID string `json:"knowledge_id"`
			Text        string `json:"text"`
		} `json:"gaps"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	gaps := make([]observation.KnowledgeEmbeddingGap, 0, len(page.Gaps))
	for _, gap := range page.Gaps {
		gaps = append(gaps, observation.KnowledgeEmbeddingGap{KnowledgeID: gap.KnowledgeID, Text: gap.Text})
	}
	return gaps, nil
}

func publishKnowledgeEmbeddings(ctx context.Context, observationURL, journalToken, model string, items map[string][]float32) error {
	payload, err := json.Marshal(map[string]any{"model": model, "items": items})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut,
		strings.TrimSuffix(observationURL, "/")+"/v1/observations/knowledge/embeddings", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if journalToken != "" {
		request.Header.Set("Authorization", "Bearer "+journalToken)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode >= 300 {
		return fmt.Errorf("journal HTTP %d: %s", response.StatusCode, string(body))
	}
	return nil
}
