package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
)

// Semantic knowledge layer (docs/supervisor-model.md §6): the kernel — which
// owns the provider keys — computes knowledge embeddings with the supervisor
// embeddings model and stores them here; hint matching consumes them as an
// extra recall tier. The journal itself never calls a model.

const (
	maxEmbeddingDim   = 4096
	maxEmbeddingBatch = 512
)

type embeddingsStore interface {
	UpsertKnowledgeEmbeddings(ctx context.Context, model string, items map[string][]float32) error
	ListKnowledgeEmbeddingGaps(ctx context.Context, model string, limit int) ([]observation.KnowledgeEmbeddingGap, error)
}

// putKnowledgeEmbeddings stores a batch: {model, items: {knowledge_id: [..]}}.
// Vectors for one model must share a dimension; batches are capped.
func (s *Server) putKnowledgeEmbeddings(w http.ResponseWriter, r *http.Request) {
	if !s.gate.Allow(w, r, controlplane.RoleWriter) {
		return
	}
	var input struct {
		Model string               `json:"model"`
		Items map[string][]float32 `json:"items"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		writeError(w, 422, errors.New("model is required"))
		return
	}
	if len(input.Items) == 0 {
		writeError(w, 422, errors.New("items is required"))
		return
	}
	if len(input.Items) > maxEmbeddingBatch {
		writeError(w, 422, fmt.Errorf("items must contain at most %d vectors", maxEmbeddingBatch))
		return
	}
	dim := 0
	for id, vector := range input.Items {
		if len(vector) == 0 || len(vector) > maxEmbeddingDim {
			writeError(w, 422, fmt.Errorf("vector %q has invalid dimension %d", id, len(vector)))
			return
		}
		if dim == 0 {
			dim = len(vector)
		} else if len(vector) != dim {
			writeError(w, 422, errors.New("all vectors in a batch must share one dimension"))
			return
		}
	}
	store, ok := s.store.(embeddingsStore)
	if !ok {
		writeError(w, 501, errors.New("this journal store does not support knowledge embeddings"))
		return
	}
	if err := store.UpsertKnowledgeEmbeddings(r.Context(), model, input.Items); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"stored": len(input.Items), "model": model, "dim": dim})
}

// getKnowledgeEmbeddingGaps lists knowledge still missing a vector for the
// model: id + proposition text, so the kernel can embed it directly.
func (s *Server) getKnowledgeEmbeddingGaps(w http.ResponseWriter, r *http.Request) {
	if !s.gate.Allow(w, r, controlplane.RoleWriter) {
		return
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		writeError(w, 422, errors.New("model is required"))
		return
	}
	limit := 256
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &limit); err != nil || limit < 1 || limit > 2048 {
			writeError(w, 400, errors.New("limit must be between 1 and 2048"))
			return
		}
	}
	store, ok := s.store.(embeddingsStore)
	if !ok {
		writeError(w, 501, errors.New("this journal store does not support knowledge embeddings"))
		return
	}
	gaps, err := store.ListKnowledgeEmbeddingGaps(r.Context(), model, limit)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if gaps == nil {
		gaps = []observation.KnowledgeEmbeddingGap{}
	}
	writeJSON(w, 200, map[string]any{"gaps": gaps, "model": model})
}

var _ = observation.KnowledgeEmbeddingGap{}
