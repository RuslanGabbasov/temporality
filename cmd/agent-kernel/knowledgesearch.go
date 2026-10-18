package main

// Semantic knowledge search (docs/supervisor-model.md §6.6, «Семантический
// поиск в Знаниях»): the journal's hint matcher already fuses lexical and
// semantic tiers; this endpoint exposes that matching as a standalone search
// for the knowledge UI. Readers only: results are scoped to the caller's
// visible projects by the journal, exactly like the run-time hints.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
)

func RegisterKnowledgeSearchRoutes(mux *http.ServeMux, gate *controlplane.Gate, activities *agent.Activities) {
	mux.HandleFunc("GET /v1/workspace/knowledge/search", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		project := strings.TrimSpace(r.URL.Query().Get("project"))
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if project == "" || query == "" {
			writeError(w, 400, errors.New("project and q are required"))
			return
		}
		hints, err := activities.KnowledgeHints(r.Context(), agent.HintRequest{
			Project: project,
			RunID:   "knowledge-search",
			TaskID:  "knowledge-search",
			ActorID: requestUserID(r),
			Query:   query,
		})
		if err != nil {
			writeError(w, 502, err)
			return
		}
		results := make([]map[string]any, 0, len(hints))
		for _, hint := range hints {
			entry := map[string]any{
				"knowledge_id": hint.KnowledgeID,
				"proposition":  hint.Proposition,
				"matched_by":   hint.MatchedBy,
			}
			if hint.State != "" {
				entry["state"] = hint.State
			}
			results = append(results, entry)
		}
		writeJSON(w, 200, map[string]any{"results": results, "query": query})
	})
}
