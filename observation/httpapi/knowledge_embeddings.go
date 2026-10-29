package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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
	// Batch ceiling: maxEmbeddingBatch vectors × maxEmbeddingDim floats — a
	// JSON float costs ~20 bytes, so 64 MiB covers the worst legal batch with
	// headroom; anything bigger is a client bug the truncation would only
	// mask as a decode error.
	if err := decodeJSONLimit(r, &input, 64<<20); err != nil {
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

// mapStore is the capability set behind the semantic map endpoint: vectors
// plus the per-project knowledge projection to join them with. A nil/empty
// knowledgeIDs filter lists every stored vector for the model.
type mapStore interface {
	ListKnowledgeEmbeddings(ctx context.Context, model string, knowledgeIDs []string) (map[string][]float32, error)
}

// knowledgeProjectLister enumerates the projects that carry knowledge events;
// implemented by the postgres store, absent in memory fakes.
type knowledgeProjectLister interface {
	ListKnowledgeProjects(ctx context.Context) ([]string, error)
}

// getKnowledgeEmbeddingMap serves the semantic map (docs/human-knowledge-
// explorer.md): every embedded knowledge item visible to the caller — vector
// plus the meta the map renders (proposition, state, topics, scope, project).
// Vectors ride as plain float arrays; the client projects them to 2D. Bound:
// newest 20000 items — the client renders the top levels server-independently;
func (s *Server) getKnowledgeEmbeddingMap(w http.ResponseWriter, r *http.Request) {
	if !s.gate.Allow(w, r, controlplane.RoleReader) {
		return
	}
	model := strings.TrimSpace(r.URL.Query().Get("model")) // empty = any stored model
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	// limit bounds the response for slow links; the client renders the map
	// per zoom tier and does not need every dot at once. offset pages through
	// the same stable order (freshness first), so the map can stream in
	// batches instead of waiting for one heavy payload.
	limit := 2000
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 100 && parsed <= 20000 {
			limit = parsed
		}
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	projects := []string{}
	if project != "" {
		if _, allowed := s.gate.AllowQuery(w, r, controlplane.RoleReader, project); !allowed {
			return
		}
		projects = []string{project}
	} else {
		principal, allowed := s.gate.AllowQuery(w, r, controlplane.RoleReader, "")
		if !allowed {
			return
		}
		if visible, all := principal.AllowedProjects(); !all {
			projects = visible
		}
	}
	store, ok := s.store.(mapStore)
	if !ok {
		writeError(w, 501, errors.New("this journal store does not support knowledge embeddings"))
		return
	}
	type mapItem struct {
		ID          string    `json:"id"`
		Project     string    `json:"project"`
		Proposition string    `json:"proposition"`
		State       string    `json:"state"`
		Kind        string    `json:"kind,omitempty"`
		Topics      []string  `json:"topics,omitempty"`
		ScopeKind   string    `json:"scope_kind,omitempty"`
		ScopeID     string    `json:"scope_id,omitempty"`
		UpdatedAt   string    `json:"updated_at,omitempty"`
		Vector      []float32 `json:"vector"`
	}
	// Collect the projections first, then load only the vectors they name:
	// the id filter keeps the store from reading every project's vectors for a
	// single-project map, and in the global view the union is exactly the set
	// the map can render.
	type pendingItem struct {
		item    observation.Knowledge
		project string
	}
	pending := []pendingItem{}
	seen := map[string]bool{}
	collect := func(project string) error {
		knowledge, _, err := s.cachedKnowledge(r.Context(), project, nil, nil)
		if err != nil {
			return err
		}
		for _, item := range knowledge {
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			pending = append(pending, pendingItem{item: item, project: project})
		}
		return nil
	}
	// One broken project's projection must not zero the whole map: collect
	// failures degrade to a per-project error list in the response — the UI
	// renders the healthy projects and names the broken ones.
	projectErrors := []string{}
	collectMany := func(projects []string) {
		for _, project := range projects {
			if err := collect(project); err != nil {
				projectErrors = append(projectErrors, project+": "+err.Error())
			}
		}
	}
	if len(projects) > 0 {
		collectMany(projects)
	} else {
		// Global view: enumerate the projects that carry knowledge events
		// (few in an installation; each projection is cached after the first
		// read), then collect their embedded items.
		lister, ok := s.store.(knowledgeProjectLister)
		if !ok {
			writeError(w, 501, errors.New("the store cannot enumerate knowledge projects; pass ?project="))
			return
		}
		all, err := lister.ListKnowledgeProjects(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		collectMany(all)
	}
	vectors := map[string][]float32{}
	if len(pending) > 0 {
		ids := make([]string, 0, len(pending))
		for _, candidate := range pending {
			ids = append(ids, candidate.item.ID)
		}
		loaded, err := store.ListKnowledgeEmbeddings(r.Context(), model, ids)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		vectors = loaded
	}
	items := []mapItem{}
	for _, candidate := range pending {
		vector, ok := vectors[candidate.item.ID]
		if !ok {
			continue
		}
		item := candidate.item
		items = append(items, mapItem{
			ID: item.ID, Project: candidate.project, Proposition: item.Proposition,
			State: item.State, Kind: item.Kind, Topics: item.Topics,
			ScopeKind: item.ScopeKind, ScopeID: item.ScopeID,
			UpdatedAt: item.UpdatedAt.Format(time.RFC3339), Vector: vector,
		})
	}
	// Newest first — the map streams in batches, so the freshest knowledge
	// paints first — with an id tiebreak: the offset paging must stay stable
	// across requests even when timestamps tie.
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		return items[i].ID < items[j].ID
	})
	total := len(items)
	// knowledgeTotal = knowledge items seen regardless of vectors: when it is
	// non-zero but total is zero, the semantic layer has produced no vectors
	// for this installation (embeddings model never loaded — Hugging Face
	// unreachable behind a corporate egress) and the UI can say so instead of
	// a bare «empty».
	knowledgeTotal := len(pending)
	if offset < len(items) {
		end := offset + limit
		if end > len(items) {
			end = len(items)
		}
		items = items[offset:end]
	} else {
		items = nil
	}
	writeJSON(w, 200, map[string]any{"items": items, "model": model, "total": total, "knowledge_total": knowledgeTotal, "project_errors": projectErrors})
}

// neighborStore is the vector-listing capability behind the neighbors lookup.
// A nil/empty knowledgeIDs filter lists every stored vector for the model —
// neighbors are intentionally cross-project (a conflict candidate may live in
// another project's knowledge).
type neighborStore interface {
	ListKnowledgeEmbeddings(ctx context.Context, model string, knowledgeIDs []string) (map[string][]float32, error)
}

// getKnowledgeEmbeddingNeighbors returns the nearest stored vectors for one
// knowledge item (wave C5 conflict prediction, docs/supervisor-model.md
// §3): id + cosine, nearest first, above a floor so quiet payloads stay
// small. Volumes are per-project bounded, so a linear scan in-process is
// fine; a real ANN index is an optimization for a much larger corpus.
func (s *Server) getKnowledgeEmbeddingNeighbors(w http.ResponseWriter, r *http.Request) {
	if !s.gate.Allow(w, r, controlplane.RoleReader) {
		return
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	// Empty model is legal for UI consumers (related-knowledge in the
	// dossier): the vectors are matched across whatever model is stored.
	if id == "" {
		writeError(w, 422, errors.New("id is required"))
		return
	}
	minScore := 0.35
	if raw := r.URL.Query().Get("min_score"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%f", &minScore); err != nil || minScore < 0 || minScore > 1 {
			writeError(w, 400, errors.New("min_score must be between 0 and 1"))
			return
		}
	}
	store, ok := s.store.(neighborStore)
	if !ok {
		writeError(w, 501, errors.New("this journal store does not support knowledge embeddings"))
		return
	}
	vectors, err := store.ListKnowledgeEmbeddings(r.Context(), model, nil)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	query, ok := vectors[id]
	if !ok {
		writeJSON(w, 200, map[string]any{"neighbors": []any{}, "model": model, "id": id})
		return
	}
	type neighbor struct {
		ID    string  `json:"id"`
		Score float32 `json:"score"`
	}
	neighbors := []neighbor{}
	for otherID, vector := range vectors {
		if otherID == id {
			continue
		}
		score, ok := observation.CosineSimilarity(query, vector)
		if !ok || float64(score) < minScore {
			continue
		}
		neighbors = append(neighbors, neighbor{ID: otherID, Score: score})
	}
	// Nearest first; cap at 10 so a dense cluster still answers cheaply.
	for i := 1; i < len(neighbors); i++ {
		for j := i; j > 0 && neighbors[j].Score > neighbors[j-1].Score; j-- {
			neighbors[j], neighbors[j-1] = neighbors[j-1], neighbors[j]
		}
	}
	if len(neighbors) > 10 {
		neighbors = neighbors[:10]
	}
	writeJSON(w, 200, map[string]any{"neighbors": neighbors, "model": model, "id": id})
}
