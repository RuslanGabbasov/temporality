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
	"os"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

const embeddingBackfillInterval = 5 * time.Minute

func startEmbeddingBackfillLoop(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL string, modelClient func(ctx context.Context) (*llm.Client, error)) {
	go func() {
		// First pass shortly after boot so a freshly configured model fills
		// the backlog without waiting a full interval.
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		runEmbeddingBackfill(ctx, log, activities, observationURL, modelClient)
		ticker := time.NewTicker(embeddingBackfillInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runEmbeddingBackfill(ctx, log, activities, observationURL, modelClient)
			}
		}
	}()
}

func runEmbeddingBackfill(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL string, modelClient func(ctx context.Context) (*llm.Client, error)) {
	// Token read per pass: a boot-order race (journal down at kernel start)
	// self-heals here instead of requiring a kernel restart.
	journalToken := os.Getenv("TEMPORALITY_API_TOKEN")
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
	vectors, err := activities.EmbedTexts(ctx, "", agent.EmbedPassage, texts)
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
	// Wave C5 (docs/supervisor-model.md §3): a fresh vector landing close to
	// an existing one predicts a knowledge conflict — flag it before the
	// contradiction ever reaches a run.
	predictKnowledgeConflicts(ctx, log, activities, observationURL, journalToken, model, items, modelClient)
}

// conflictScoreBand brackets "close but not the same": below it the items are
// unrelated, at or above it they are near-duplicates the dedup already
// handles.
const (
	conflictScoreMin = 0.85
	conflictScoreMax = 0.97
)

// conflictMeta is the journal-side knowledge identity the conflict verdict
// needs: which project the item lives in and its current proposition.
type conflictMeta struct {
	Project     string
	Proposition string
}

// fetchKnowledgeMeta builds knowledge_id → (project, proposition) from the
// journal's knowledge events. Events arrive oldest-first; a corrected item's
// latest correction carries its current wording, so later events win.
func fetchKnowledgeMeta(ctx context.Context, observationURL, journalToken string) map[string]conflictMeta {
	meta := map[string]conflictMeta{}
	for _, eventType := range []string{"knowledge.proposed", "knowledge.corrected"} {
		events, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, eventType, 500)
		if err != nil {
			continue
		}
		for _, ev := range events {
			id, _ := ev.Data["knowledge_id"].(string)
			prop, _ := ev.Data["proposition"].(string)
			if id == "" || prop == "" {
				continue
			}
			// Later events overwrite: the newest formulation is the one to show.
			meta[id] = conflictMeta{Project: ev.Project, Proposition: prop}
		}
	}
	return meta
}

// predictKnowledgeConflicts flags pairs of close-but-not-equal knowledge.
// Cosine closeness only NOMINATES a candidate — unrelated statements land in
// the score band all the time — so with a judge available each candidate is
// verified first: only a judged conflict reaches the queue. No judge
// (unconfigured or erroring) fails open: the deterministic behavior as before.
func predictKnowledgeConflicts(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL, journalToken, model string, items map[string][]float32, modelClient func(ctx context.Context) (*llm.Client, error)) {
	metaByKnowledge := fetchKnowledgeMeta(ctx, observationURL, journalToken)
	var client *llm.Client
	if modelClient != nil {
		client, _ = modelClient(ctx) // nil on error — the gate fails open
	}
	checked := 0
	for id := range items {
		if checked >= 4 {
			return
		}
		checked++
		neighbors, err := fetchKnowledgeNeighbors(ctx, observationURL, journalToken, model, id, conflictScoreMin)
		if err != nil {
			log.Warn("conflict prediction: neighbors", "knowledge", id, "error", err.Error())
			continue
		}
		for _, neighbor := range neighbors {
			if neighbor.Score < conflictScoreMin || neighbor.Score > conflictScoreMax {
				continue
			}
			newMeta := metaByKnowledge[id]
			oldMeta := metaByKnowledge[neighbor.ID]
			if client != nil {
				verdict, vErr := agent.ConflictPredictionCase(ctx, client, newMeta.Proposition, oldMeta.Proposition)
				if vErr != nil {
					log.Warn("conflict prediction: judge", "pair", id+"~"+neighbor.ID, "error", vErr.Error())
				} else if !verdict.Conflict {
					log.Info("conflict prediction: judge rejected a look-alike pair", "pair", id+"~"+neighbor.ID, "explanation", verdict.Explanation)
					break // this item's best neighbor is not a conflict — next item
				}
			}
			subject := id + "~" + neighbor.ID
			summary := fmt.Sprintf("«%s» (новое) против «%s» (существующее) — косинус %.2f: формулировки близки, смысл может конфликтовать.",
				newMeta.Proposition, oldMeta.Proposition, neighbor.Score)
			project := oldMeta.Project
			if project == "" {
				project = newMeta.Project
			}
			if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseKnowledgeConflict, subject, project, summary, "Сопоставьте формулировки: схлопнуть, инвалидировать устаревшее или оставить оба в разных scope.", map[string]any{
				"knowledge_id": id, "neighbor_id": neighbor.ID, "score": neighbor.Score, "model": model,
				"new_proposition": newMeta.Proposition, "old_proposition": oldMeta.Proposition,
				"old_project": oldMeta.Project, "new_project": newMeta.Project,
			}); err != nil {
				log.Warn("conflict prediction: publish", "error", err.Error())
			}
			break // one conflict verdict per fresh item per pass
		}
	}
}

type knowledgeNeighbor struct {
	ID    string  `json:"id"`
	Score float32 `json:"score"`
}

// fetchKnowledgeNeighbors asks the journal for the nearest stored vectors.
func fetchKnowledgeNeighbors(ctx context.Context, observationURL, journalToken, model, id string, minScore float32) ([]knowledgeNeighbor, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(observationURL, "/")+"/v1/observations/knowledge/embeddings/neighbors?model="+url.QueryEscape(model)+"&id="+url.QueryEscape(id)+"&min_score="+fmt.Sprint(minScore), nil)
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
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("neighbors %s", journalErrorSnippet(response.StatusCode, string(body)))
	}
	var page struct {
		Neighbors []knowledgeNeighbor `json:"neighbors"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	return page.Neighbors, nil
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
