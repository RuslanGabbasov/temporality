package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// Lifecycle states of the knowledge projection plus the two pseudo-states the
// listing exposes: "alive" (usable now, i.e. not retired) and "at_risk"
// (flagged by dependency risk regardless of lifecycle state).
var knowledgeStateFilters = map[string]bool{
	"proposed": true, "confirmed": true, "challenged": true,
	"corrected": true, "superseded": true, "invalidated": true,
	"alive": true, "at_risk": true,
}

// Coarse scope groups of the listing; an empty ScopeKind is the project birth
// scope (docs/knowledge-evolution.md §5).
var knowledgeScopeFilters = map[string]bool{"project": true, "org_unit": true, "organization": true}

// knowledgeListQuery carries the post-projection filters of the knowledge
// listing. State, scope and text are computed by the projection, so they are
// applied after it, never in SQL.
type knowledgeListQuery struct {
	// Terms holds the q parameter split into lowercase words; every word must
	// match somewhere, so multi-word queries degrade gracefully instead of
	// becoming exact-substring searches.
	Terms  []string
	State  string
	Scope  string
	Limit  int // 0 = unlimited (full projection, legacy callers)
	Cursor knowledgePageCursor
}

// knowledgePageCursor anchors pagination in the deterministic listing order
// (updated_at DESC, id ASC) — the projection has no monotonically increasing
// row key, so the (UpdatedAt, ID) pair of the last served item is the cursor.
type knowledgePageCursor struct {
	UpdatedAt time.Time `json:"u"`
	ID        string    `json:"i"`
}

func parseKnowledgeListQuery(values url.Values) (knowledgeListQuery, error) {
	query := knowledgeListQuery{
		State: strings.TrimSpace(values.Get("state")),
		Scope: strings.TrimSpace(values.Get("scope")),
	}
	if raw := strings.TrimSpace(values.Get("q")); raw != "" {
		for _, word := range strings.Fields(strings.ToLower(raw)) {
			query.Terms = append(query.Terms, word)
		}
	}
	if query.State != "" && !knowledgeStateFilters[query.State] {
		return query, errors.New("state must be a lifecycle state or alive/at_risk")
	}
	if query.Scope != "" && !knowledgeScopeFilters[query.Scope] {
		return query, errors.New("scope must be project, org_unit, or organization")
	}
	if value := values.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 500 {
			return query, errors.New("limit must be between 1 and 500")
		}
		query.Limit = limit
	}
	cursor, err := decodeKnowledgeCursor(values.Get("cursor"))
	if err != nil {
		return query, errors.New("cursor is malformed")
	}
	query.Cursor = cursor
	return query, nil
}

func decodeKnowledgeCursor(raw string) (knowledgePageCursor, error) {
	if raw == "" {
		return knowledgePageCursor{}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return knowledgePageCursor{}, err
	}
	var cursor knowledgePageCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return knowledgePageCursor{}, err
	}
	if cursor.ID == "" || cursor.UpdatedAt.IsZero() {
		return knowledgePageCursor{}, errors.New("cursor is missing fields")
	}
	return cursor, nil
}

func encodeKnowledgeCursor(item observation.Knowledge) string {
	payload, err := json.Marshal(knowledgePageCursor{UpdatedAt: item.UpdatedAt, ID: item.ID})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

// knowledgeScopeGroup normalizes the effective scope to a filter group.
func knowledgeScopeGroup(item observation.Knowledge) string {
	if item.ScopeKind == "" {
		return "project"
	}
	return item.ScopeKind
}

func knowledgeStateMatches(item observation.Knowledge, state string) bool {
	switch state {
	case "alive":
		return item.State != "invalidated" && item.State != "superseded" && item.State != "corrected"
	case "at_risk":
		return item.AtRisk
	default:
		return item.State == state
	}
}

// knowledgeSearchFields collects the lowercase textual fields a search word
// can hit: proposition, id, topics and entities.
func knowledgeSearchFields(item observation.Knowledge) []string {
	fields := make([]string, 0, 2+len(item.Topics)+len(item.Entities))
	fields = append(fields, strings.ToLower(item.Proposition), strings.ToLower(item.ID))
	for _, topic := range item.Topics {
		fields = append(fields, strings.ToLower(topic))
	}
	for _, entity := range item.Entities {
		fields = append(fields, strings.ToLower(entity))
	}
	return fields
}

func knowledgeTextMatches(fields, terms []string) bool {
	for _, term := range terms {
		matched := false
		for _, field := range fields {
			if strings.Contains(field, term) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// knowledgeMatches reports whether an item passes the query's scope, state
// and text filters. Empty filter dimensions accept everything, so it doubles
// as the facet building block.
func knowledgeMatches(item observation.Knowledge, query knowledgeListQuery) bool {
	if query.Scope != "" && knowledgeScopeGroup(item) != query.Scope {
		return false
	}
	if query.State != "" && !knowledgeStateMatches(item, query.State) {
		return false
	}
	if len(query.Terms) > 0 && !knowledgeTextMatches(knowledgeSearchFields(item), query.Terms) {
		return false
	}
	return true
}

type knowledgeFacets struct {
	States map[string]int `json:"states"`
	Scopes map[string]int `json:"scopes"`
}

// knowledgeFacetsFor counts states and scopes over the filtered set. Each
// dimension is computed without its own filter (standard faceting), so the
// chip counts always show what switching that chip would reveal; the other
// dimensions (text search, bitemporal cutoffs, visibility) still apply.
func knowledgeFacetsFor(knowledge []observation.Knowledge, query knowledgeListQuery) knowledgeFacets {
	facets := knowledgeFacets{
		States: map[string]int{
			"proposed": 0, "confirmed": 0, "challenged": 0,
			"corrected": 0, "superseded": 0, "invalidated": 0, "alive": 0, "at_risk": 0,
		},
		Scopes: map[string]int{"project": 0, "org_unit": 0, "organization": 0},
	}
	for _, item := range knowledge {
		if !knowledgeMatches(item, knowledgeListQuery{Terms: query.Terms, Scope: query.Scope}) {
			continue
		}
		facets.States[item.State]++
		if knowledgeStateMatches(item, "alive") {
			facets.States["alive"]++
		}
		if knowledgeStateMatches(item, "at_risk") {
			facets.States["at_risk"]++
		}
	}
	for _, item := range knowledge {
		if !knowledgeMatches(item, knowledgeListQuery{Terms: query.Terms, State: query.State}) {
			continue
		}
		facets.Scopes[knowledgeScopeGroup(item)]++
	}
	return facets
}

// sortKnowledgeList pins the deterministic listing order: freshest update
// first, ties broken by ID, so equal scores never shuffle between requests
// (docs/plan-priming-relevance.md §7).
func sortKnowledgeList(knowledge []observation.Knowledge) {
	sort.Slice(knowledge, func(i, j int) bool {
		if !knowledge[i].UpdatedAt.Equal(knowledge[j].UpdatedAt) {
			return knowledge[i].UpdatedAt.After(knowledge[j].UpdatedAt)
		}
		return knowledge[i].ID < knowledge[j].ID
	})
}

// knowledgeAfterCursor reports whether an item follows the cursor in the
// deterministic listing order (updated_at DESC, id ASC): strictly older
// updates first, then the ID tie-break ascending. An empty cursor accepts
// everything.
func knowledgeAfterCursor(item observation.Knowledge, cursor knowledgePageCursor) bool {
	if cursor.ID == "" {
		return true
	}
	if !item.UpdatedAt.Equal(cursor.UpdatedAt) {
		return item.UpdatedAt.Before(cursor.UpdatedAt)
	}
	return item.ID > cursor.ID
}

// filterKnowledgeList applies the query's state/scope/text filters.
func filterKnowledgeList(knowledge []observation.Knowledge, query knowledgeListQuery) []observation.Knowledge {
	if query.State == "" && query.Scope == "" && len(query.Terms) == 0 {
		return knowledge
	}
	filtered := make([]observation.Knowledge, 0, len(knowledge))
	for _, item := range knowledge {
		if knowledgeMatches(item, query) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// paginateKnowledgeList slices the sorted list after the cursor. The next
// cursor is the last included item; with limit == 0 it returns everything
// (legacy full-projection callers).
func paginateKnowledgeList(knowledge []observation.Knowledge, cursor knowledgePageCursor, limit int) ([]observation.Knowledge, string) {
	var page []observation.Knowledge
	for _, item := range knowledge {
		if !knowledgeAfterCursor(item, cursor) {
			continue
		}
		if limit > 0 && len(page) == limit {
			return page, encodeKnowledgeCursor(page[len(page)-1])
		}
		page = append(page, item)
	}
	return page, ""
}
