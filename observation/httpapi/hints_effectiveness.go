package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
)

// Hint tier effectiveness (docs/supervisor-model.md §6.6): the semantic tier
// is only worth its cost when its helpful rate stands up against the lexical
// tiers. This report joins hint.used (carries matched_by) with hint.outcome
// (carries the helpful|harmful verdict) per run+knowledge and aggregates the
// outcome rate per tier family. Deterministic, model-free, read-only; the
// window keeps the join bounded.

// tierOf collapses a matched_by value ("semantic:0.91", "entity:playwright",
// "term:x") into the comparable tier family.
func tierOf(matchedBy string) string {
	matchedBy = strings.TrimSpace(matchedBy)
	switch {
	case matchedBy == "":
		return "unknown"
	case strings.HasPrefix(matchedBy, "semantic"):
		return "semantic"
	case strings.HasPrefix(matchedBy, "entity"):
		return "entity"
	case strings.HasPrefix(matchedBy, "topic"):
		return "topic"
	case strings.HasPrefix(matchedBy, "term"):
		return "term"
	default:
		return "other"
	}
}

// TierOutcome is one tier row of the report.
type TierOutcome struct {
	Tier           string `json:"tier"`
	Helpful        int    `json:"helpful"`
	Harmful        int    `json:"harmful"`
	HelpfulRatePct int    `json:"helpful_rate_pct"`
}

// aggregateTierOutcomes joins outcomes with tiers by run+knowledge and folds
// the counts per tier family. Events may arrive in any order; the newest
// matched_by wins (the listing is newest-first, so first hit sticks).
func aggregateTierOutcomes(used, outcomes []observation.Event) []TierOutcome {
	tierByKey := map[string]string{}
	for _, event := range used {
		knowledgeID, _ := event.Data["knowledge_id"].(string)
		if knowledgeID == "" {
			continue
		}
		matchedBy, _ := event.Data["matched_by"].(string)
		key := event.Context.Run + "\x00" + knowledgeID
		if _, exists := tierByKey[key]; !exists {
			tierByKey[key] = tierOf(matchedBy)
		}
	}
	counts := map[string]*TierOutcome{}
	for _, event := range outcomes {
		knowledgeID, _ := event.Data["knowledge_id"].(string)
		outcome, _ := event.Data["outcome"].(string)
		if knowledgeID == "" || (outcome != "helpful" && outcome != "harmful") {
			continue
		}
		tier := "unknown"
		if t, ok := tierByKey[event.Context.Run+"\x00"+knowledgeID]; ok {
			tier = t
		}
		row := counts[tier]
		if row == nil {
			row = &TierOutcome{Tier: tier}
			counts[tier] = row
		}
		if outcome == "helpful" {
			row.Helpful++
		} else {
			row.Harmful++
		}
	}
	result := make([]TierOutcome, 0, len(counts))
	for _, row := range counts {
		if total := row.Helpful + row.Harmful; total > 0 {
			row.HelpfulRatePct = row.Helpful * 100 / total
		}
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Helpful+result[i].Harmful != result[j].Helpful+result[j].Harmful {
			return result[i].Helpful+result[i].Harmful > result[j].Helpful+result[j].Harmful
		}
		return result[i].Tier < result[j].Tier
	})
	return result
}

// getHintEffectiveness serves GET /v1/observations/hints/effectiveness.
// pageAll follows listing cursors up to maxPages (the store caps a page at
// 500) so a 30-day window is not silently truncated to its oldest page.
func (s *Server) pageAll(filter observation.Filter, maxPages int) ([]observation.Event, error) {
	var all []observation.Event
	cursor := ""
	for page := 0; page < maxPages; page++ {
		pageData, err := s.store.ListObservationPage(context.Background(), filter, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, pageData.Events...)
		if pageData.NextCursor == "" {
			break
		}
		cursor = pageData.NextCursor
	}
	return all, nil
}

// getHintEffectiveness serves GET /v1/observations/hints/effectiveness.
func (s *Server) getHintEffectiveness(w http.ResponseWriter, r *http.Request) {
	// Same query surface as the global events listing: the gate resolves the
	// principal and constrains the report to the visible projects.
	principal, allowed := s.gate.AllowQuery(w, r, controlplane.RoleReader, r.URL.Query().Get("project"))
	if !allowed {
		return
	}
	since := time.Now().Add(-30 * 24 * time.Hour)
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("since must be RFC3339"))
			return
		}
		since = parsed
	}
	limit := 500 // the store caps listing pages at 500 (NormalizeLimit)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	project := r.URL.Query().Get("project")
	base := observation.Filter{Since: &since, Limit: limit}
	if project != "" {
		base.Project = project
	} else if projects, all := principal.AllowedProjects(); !all {
		if len(projects) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"tiers": []TierOutcome{}, "since": since.UTC()})
			return
		}
		base.Projects = projects
	}
	usedFilter := base
	usedFilter.Type = "hint.used"
	usedEvents, err := s.pageAll(usedFilter, 5)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	outcomeFilter := base
	outcomeFilter.Type = "hint.outcome"
	outcomeEvents, err := s.pageAll(outcomeFilter, 5)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tiers := aggregateTierOutcomes(usedEvents, outcomeEvents)
	if tiers == nil {
		tiers = []TierOutcome{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tiers": tiers, "since": since.UTC()})
}
