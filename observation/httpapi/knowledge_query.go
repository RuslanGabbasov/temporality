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
	Terms []string
	State string
	Scope string
	// Topic/Entity/Origin/Kind are the clickable facet dimensions of the
	// research surface (docs/human-knowledge-explorer.md §6): exact match on
	// the projected attribute. Single value per dimension, like state/scope.
	Topic  string
	Entity string
	// Area is the sticky map filter: the name matches topic OR entity (the
	// area map merges both dimensions by name, so a map click cannot know
	// which one it was). Empty = off.
	Area string
	// Origin filters by the birth project of the item (Knowledge.Project) —
	// named "origin" because the "project" parameter already scopes
	// authorization and visibility.
	Origin string
	Kind   string
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
		State:  strings.TrimSpace(values.Get("state")),
		Scope:  strings.TrimSpace(values.Get("scope")),
		Topic:  strings.TrimSpace(values.Get("topic")),
		Entity: strings.TrimSpace(values.Get("entity")),
		Area:   strings.TrimSpace(values.Get("area")),
		Origin: strings.TrimSpace(values.Get("origin")),
		Kind:   strings.TrimSpace(values.Get("kind")),
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
	if query.Kind != "" && !knowledgeKindFilters[query.Kind] {
		return query, errors.New("kind must be claim or observation")
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

// knowledgeKindFilters are the authored kinds of knowledge items
// (knowledge.go: model claims vs kernel heuristic observations); items
// recorded before kinds existed project as claims.
var knowledgeKindFilters = map[string]bool{"claim": true, "observation": true}

// knowledgeKindGroup normalizes the item kind for filtering: the projection
// already defaults empty kinds to "claim".
func knowledgeKindGroup(item observation.Knowledge) string {
	if item.Kind == "" {
		return "claim"
	}
	return item.Kind
}

// knowledgeValueMatches is the exact (case-insensitive) match facet values
// rely on — facet chips send back the exact string they counted.
func knowledgeValueMatches(values []string, wanted string) bool {
	if wanted == "" {
		return true
	}
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

// knowledgeMatches reports whether an item passes the query's scope, state,
// text and facet-value filters. Empty filter dimensions accept everything, so
// it doubles as the facet building block.
func knowledgeMatches(item observation.Knowledge, query knowledgeListQuery) bool {
	if query.Scope != "" && knowledgeScopeGroup(item) != query.Scope {
		return false
	}
	if query.State != "" && !knowledgeStateMatches(item, query.State) {
		return false
	}
	if query.Topic != "" && !knowledgeValueMatches(item.Topics, query.Topic) {
		return false
	}
	if query.Entity != "" && !knowledgeValueMatches(item.Entities, query.Entity) {
		return false
	}
	if query.Area != "" && !knowledgeValueMatches(item.Topics, query.Area) && !knowledgeValueMatches(item.Entities, query.Area) {
		return false
	}
	if query.Area != "" && !knowledgeValueMatches(item.Topics, query.Area) && !knowledgeValueMatches(item.Entities, query.Area) {
		return false
	}
	if query.Origin != "" && !strings.EqualFold(item.Project, query.Origin) {
		return false
	}
	if query.Kind != "" && knowledgeKindGroup(item) != query.Kind {
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
	// Topics/Entities/Projects/Kinds extend the facet dimensions for the
	// research surface (docs/human-knowledge-explorer.md §6). Open-value
	// dimensions are capped to the most frequent values.
	Topics   map[string]int `json:"topics,omitempty"`
	Entities map[string]int `json:"entities,omitempty"`
	Projects map[string]int `json:"projects,omitempty"`
	Kinds    map[string]int `json:"kinds,omitempty"`
}

// knowledgeFacetValueCap bounds open-value facet dimensions: the explorer
// needs the popular values to navigate, not the full long tail.
const knowledgeFacetValueCap = 40

// knowledgeFacetsFor counts every facet dimension over the filtered set.
// Each dimension is computed without its own filter (standard faceting), so
// the chip counts always show what switching that chip would reveal; the
// other dimensions (text search, the remaining filters, bitemporal cutoffs,
// visibility) still apply.
func knowledgeFacetsFor(knowledge []observation.Knowledge, query knowledgeListQuery) knowledgeFacets {
	facets := knowledgeFacets{
		States: map[string]int{
			"proposed": 0, "confirmed": 0, "challenged": 0,
			"corrected": 0, "superseded": 0, "invalidated": 0, "alive": 0, "at_risk": 0,
		},
		Scopes: map[string]int{"project": 0, "org_unit": 0, "organization": 0},
		Kinds:  map[string]int{"claim": 0, "observation": 0},
	}
	// Dimension queries: the facet's own filter is cleared, every other
	// dimension still applies.
	withoutState := query
	withoutState.State = ""
	for _, item := range knowledge {
		if !knowledgeMatches(item, withoutState) {
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
	withoutScope := query
	withoutScope.Scope = ""
	for _, item := range knowledge {
		if !knowledgeMatches(item, withoutScope) {
			continue
		}
		facets.Scopes[knowledgeScopeGroup(item)]++
	}
	withoutKind := query
	withoutKind.Kind = ""
	for _, item := range knowledge {
		if !knowledgeMatches(item, withoutKind) {
			continue
		}
		facets.Kinds[knowledgeKindGroup(item)]++
	}
	// Open-value dimensions (topics/entities/projects) clear their own filter
	// and land in temporary counters capped afterwards.
	topics := map[string]int{}
	withoutTopic := query
	withoutTopic.Topic = ""
	for _, item := range knowledge {
		if !knowledgeMatches(item, withoutTopic) {
			continue
		}
		for _, topic := range item.Topics {
			topics[strings.ToLower(strings.TrimSpace(topic))]++
		}
	}
	facets.Topics = capKnowledgeFacetValues(topics)
	entities := map[string]int{}
	withoutEntity := query
	withoutEntity.Entity = ""
	for _, item := range knowledge {
		if !knowledgeMatches(item, withoutEntity) {
			continue
		}
		for _, entity := range item.Entities {
			entities[strings.ToLower(strings.TrimSpace(entity))]++
		}
	}
	facets.Entities = capKnowledgeFacetValues(entities)
	projects := map[string]int{}
	withoutOrigin := query
	withoutOrigin.Origin = ""
	for _, item := range knowledge {
		if !knowledgeMatches(item, withoutOrigin) {
			continue
		}
		if item.Project != "" {
			projects[item.Project]++
		}
	}
	facets.Projects = capKnowledgeFacetValues(projects)
	return facets
}

// capKnowledgeFacetValues keeps the most frequent values (ties broken
// alphabetically) so open dimensions stay bounded and deterministic.
func capKnowledgeFacetValues(values map[string]int) map[string]int {
	if len(values) <= knowledgeFacetValueCap {
		return values
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if values[keys[i]] != values[keys[j]] {
			return values[keys[i]] > values[keys[j]]
		}
		return keys[i] < keys[j]
	})
	capped := make(map[string]int, knowledgeFacetValueCap)
	for _, key := range keys[:knowledgeFacetValueCap] {
		capped[key] = values[key]
	}
	return capped
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

// filterKnowledgeList applies the query's state/scope/text/facet filters.
func filterKnowledgeList(knowledge []observation.Knowledge, query knowledgeListQuery) []observation.Knowledge {
	if query.State == "" && query.Scope == "" && len(query.Terms) == 0 && query.Topic == "" && query.Entity == "" && query.Area == "" && query.Origin == "" && query.Kind == "" {
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
	// Non-nil even when nothing matches: a nil slice marshals to JSON null,
	// and browser clients that iterate the field (`for (const item of knowledge)`)
	// crash on it — an empty historical slice must arrive as [].
	page := []observation.Knowledge{}
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
