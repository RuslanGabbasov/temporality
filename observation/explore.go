package observation

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Knowledge exploration engine (docs/agent-knowledge-exploration.md §4): six
// read-only operations over the visible knowledge base. The engine is pure —
// visibility, bitemporal slices and authorization are resolved by the caller
// (the journal HTTP API) before items get here, so no result can leak beyond
// the caller's scope. It never mutates knowledge and never persists research
// relations: candidates exist only inside a computed result (§8.1).

// ExploreAlgorithmVersion identifies the deterministic ranking scheme so
// results can be compared and replayed across releases.
const ExploreAlgorithmVersion = "lexical-idf.v1"

// Explore operations.
const (
	ExploreOrient     = "orient"
	ExploreSearch     = "search"
	ExploreExpand     = "expand"
	ExploreTrace      = "trace"
	ExploreCompare    = "compare"
	ExploreSynthesize = "synthesize"
)

// ExploreLimits caps per docs §7.2 (budget and infinite-walk protection).
// Defaults are deliberately small: exploration is iterative, and the first
// answer should orient, not dump.
const (
	ExploreDefaultItems = 20
	ExploreMaxItems     = 100
	ExploreDefaultDepth = 1
	ExploreMaxDepth     = 3
	ExploreMaxTargets   = 16
	ExploreMaxRelations = 8
	// exploreOrientDefaultAreas keeps the orientation map compact (§4.1: "a
	// compact map of the area, not the full list of statements").
	exploreOrientDefaultAreas = 12
)

// ExploreRelationTypes mirrors the knowledge.linked vocabulary (event.go).
var ExploreRelationTypes = []string{"supports", "contradicts", "derived_from", "depends_on", "supersedes", "related_to"}

// ExploreQuery is one research operation request.
type ExploreQuery struct {
	Operation string
	// Question is the natural-language question or task description; it drives
	// orient, search and synthesize.
	Question string
	// Targets are knowledge ids for expand/trace/compare/synthesize.
	Targets []string
	// Relations restricts expand to specific relationship types; empty means all.
	Relations []string
	MaxDepth  int
	MaxItems  int
	// IncludeCandidates lets expand and compare additionally report
	// heuristically discovered relations (shared topics), clearly marked as
	// candidates rather than explicit facts (§8).
	IncludeCandidates bool
}

// ExploreKnowledgeCard is a compact, transport-safe view of one knowledge item
// with the exploration-specific explanations attached (matched_by, score,
// depth, via, basis).
type ExploreKnowledgeCard struct {
	KnowledgeID string   `json:"knowledge_id"`
	Proposition string   `json:"proposition"`
	State       string   `json:"state"`
	Kind        string   `json:"kind,omitempty"`
	Topics      []string `json:"topics,omitempty"`
	Entities    []string `json:"entities,omitempty"`
	Project     string   `json:"project,omitempty"`
	ScopeKind   string   `json:"scope_kind,omitempty"`
	ScopeID     string   `json:"scope_id,omitempty"`
	// MatchedBy explains relevance: term:<token>, entity:<name>, topic:<name>.
	MatchedBy []string `json:"matched_by,omitempty"`
	Score     float64  `json:"score,omitempty"`
	// Depth/Via/Basis carry the expand walk provenance: how many hops from the
	// targets, over which relation, and whether the link is explicit or a
	// candidate.
	Depth       int    `json:"depth,omitempty"`
	Via         string `json:"via,omitempty"`
	Basis       string `json:"basis,omitempty"`
	AtRisk      bool   `json:"at_risk,omitempty"`
	Replacement string `json:"replacement_id,omitempty"`
	Evidence    int    `json:"evidence,omitempty"`
}

// ExploreArea is one region of the knowledge map returned by orient.
type ExploreArea struct {
	Name           string                 `json:"name"`
	Kind           string                 `json:"kind"` // topic|entity
	KnowledgeCount int                    `json:"knowledge_count"`
	States         map[string]int         `json:"states"`
	Score          float64                `json:"score,omitempty"`
	MatchedBy      []string               `json:"matched_by,omitempty"`
	Representative []ExploreKnowledgeCard `json:"representative,omitempty"`
	// Contradictions counts items in the area touched by an explicit
	// contradicts relation or living in a challenged state.
	Contradictions int `json:"contradictions,omitempty"`
}

// ExploreRelation is one edge reported by expand (or one conflict ground by
// compare). Explicit edges carry the linking event id.
type ExploreRelation struct {
	FromID  string `json:"from_id"`
	ToID    string `json:"to_id"`
	Type    string `json:"type"`
	Basis   string `json:"basis"` // explicit|candidate
	EventID string `json:"event_id,omitempty"`
	Depth   int    `json:"depth,omitempty"`
	// Note is a human-readable ground for candidate edges ("shared topic: x").
	Note string `json:"note,omitempty"`
}

// ExploreHistoryStep is one reconstructed history step of a knowledge item.
type ExploreHistoryStep struct {
	EventID string    `json:"event_id"`
	Type    string    `json:"type"`
	State   string    `json:"state,omitempty"`
	Rule    string    `json:"rule,omitempty"`
	At      time.Time `json:"at"`
	ActorID string    `json:"actor_id,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}

// ExploreHistory is the provenance dossier of one knowledge item: full
// history plus usage statistics, straight from the projection (§4.4).
type ExploreHistory struct {
	KnowledgeID string               `json:"knowledge_id"`
	Proposition string               `json:"proposition"`
	State       string               `json:"state"`
	CreatedAt   time.Time            `json:"created_at"`
	CreatedBy   string               `json:"created_by,omitempty"`
	Steps       []ExploreHistoryStep `json:"steps"`
	Usage       ExploreTraceUsage    `json:"usage"`
	Replacement string               `json:"replacement_id,omitempty"`
	AtRisk      bool                 `json:"at_risk,omitempty"`
	Evidence    []Evidence           `json:"evidence,omitempty"`
}

// ExploreTraceUsage summarizes recorded hint telemetry for the item.
type ExploreTraceUsage struct {
	HintOffers      int `json:"hint_offers"`
	HintUses        int `json:"hint_uses"`
	HintIgnores     int `json:"hint_ignores"`
	HelpfulOutcomes int `json:"helpful_outcomes"`
	HarmfulOutcomes int `json:"harmful_outcomes"`
	ReuseCount      int `json:"reuse_count"`
}

// ExploreConflict reports two (or more) knowledge items that cannot all be
// applied as-is: an explicit contradiction, a doubt state, or a replacement.
type ExploreConflict struct {
	KnowledgeIDs []string `json:"knowledge_ids"`
	// Basis: explicit:contradicts | candidate:same-topic-different-state |
	// resolved:replacement.
	Basis string `json:"basis"`
	// Detail explains why the items are treated as conflicting.
	Detail string `json:"detail"`
	// ContextDifferences lists material scope/time/context splits that may
	// make the statements non-contradictory in practice (§4.5).
	ContextDifferences []string `json:"context_differences,omitempty"`
	Resolved           bool     `json:"resolved"`
	Resolution         string   `json:"resolution,omitempty"`
}

// ExploreSynthesis is the compact research dossier (§4.6).
type ExploreSynthesis struct {
	// Established: confirmed statements with their evidence grounds.
	Established []ExploreKnowledgeCard `json:"established,omitempty"`
	// Disputed: challenged statements, unconfirmed proposals and items in
	// explicit contradictions.
	Disputed []ExploreKnowledgeCard `json:"disputed,omitempty"`
	// Retired: corrected/superseded/invalidated statements — negative
	// experience that must not surface as live rules but often carries the
	// most useful lesson (§4.6 "Временной контекст").
	Retired []ExploreKnowledgeCard `json:"retired,omitempty"`
	// Conflicts observed within the working set.
	Conflicts []ExploreConflict `json:"conflicts,omitempty"`
	// Gaps and NextActions are derived heuristics, not facts; the struct marks
	// them as interpretation so a reader cannot mistake them for content
	// sourced from the knowledge base (§4.6 "не превращать предположения в
	// факты").
	Gaps        []string `json:"gaps,omitempty"`
	NextActions []string `json:"next_actions,omitempty"`
	// References lists every knowledge id the dossier is grounded in.
	References []string `json:"references,omitempty"`
	// Interpretation flags sections computed by heuristics (gaps,
	// next_actions) as opposed to grounded sections above.
	Interpretation bool `json:"interpretation"`
}

// ExploreLimitsReport echoes the caps actually applied and whether the result
// was cut by them. Truncation is never evidence of completeness (§6.4).
type ExploreLimitsReport struct {
	MaxItems      int  `json:"max_items"`
	MaxDepth      int  `json:"max_depth"`
	ItemsReturned int  `json:"items_returned"`
	Truncated     bool `json:"truncated"`
}

// ExploreResult is the unified machine-readable envelope (§6.2). Sections that
// do not apply to the requested operation stay empty.
type ExploreResult struct {
	Operation        string                 `json:"operation"`
	AlgorithmVersion string                 `json:"algorithm_version"`
	Areas            []ExploreArea          `json:"areas,omitempty"`
	Results          []ExploreKnowledgeCard `json:"results,omitempty"`
	Relations        []ExploreRelation      `json:"relations,omitempty"`
	Traces           []ExploreHistory       `json:"traces,omitempty"`
	Conflicts        []ExploreConflict      `json:"conflicts,omitempty"`
	Synthesis        *ExploreSynthesis      `json:"synthesis,omitempty"`
	Gaps             []string               `json:"gaps,omitempty"`
	NextActions      []string               `json:"next_actions,omitempty"`
	Limits           ExploreLimitsReport    `json:"limits"`
	Completeness     string                 `json:"completeness"` // complete|truncated|partial
	// Notes carry non-fatal limitations: unknown targets, ignored relation
	// filters, empty bases (§6.4 — distinguish "no results" from "no data").
	Notes []string `json:"notes,omitempty"`
}

// ExploreKnowledge runs one exploration operation over the visible knowledge
// base. It returns a result even when nothing matches: absence of findings is
// reported, never treated as an error.
func ExploreKnowledge(knowledge []Knowledge, query ExploreQuery) ExploreResult {
	result := ExploreResult{Operation: query.Operation, AlgorithmVersion: ExploreAlgorithmVersion, Completeness: "complete"}
	if result.Operation == "" {
		result.Operation = ExploreSearch
	}
	result.Limits.MaxItems = normalizeExploreItems(query.MaxItems)
	// The orientation map defaults to a tighter cap than item listings: it is
	// a map to navigate from, not a result set (§4.1).
	if query.Operation == ExploreOrient && query.MaxItems <= 0 {
		result.Limits.MaxItems = exploreOrientDefaultAreas
	}
	result.Limits.MaxDepth = normalizeExploreDepth(query.MaxDepth)
	byID := make(map[string]Knowledge, len(knowledge))
	for _, item := range knowledge {
		byID[item.ID] = item
	}
	// Feature index and document frequencies power every lexical operation;
	// they are computed once and reused across orient/search/synthesize.
	tokensByID := make(map[string]map[string]bool, len(knowledge))
	documentFrequency := make(map[string]int)
	for _, item := range knowledge {
		tokens := knowledgeItemTokens(item)
		tokensByID[item.ID] = tokens
		for token := range tokens {
			documentFrequency[token]++
		}
	}
	idf := func(token string) float64 {
		// Smoothed inverse document frequency: rare tokens rank higher, tokens
		// present everywhere (and therefore unselective) sink toward zero.
		return math.Log(1 + float64(len(knowledge))/float64(1+documentFrequency[token]))
	}
	engine := exploreEngine{knowledge: knowledge, byID: byID, tokensByID: tokensByID, idf: idf}
	switch result.Operation {
	case ExploreOrient:
		engine.orient(&result, query)
	case ExploreSearch:
		engine.search(&result, query)
	case ExploreExpand:
		engine.expand(&result, query)
	case ExploreTrace:
		engine.trace(&result, query)
	case ExploreCompare:
		engine.compare(&result, query)
	case ExploreSynthesize:
		engine.synthesize(&result, query)
	default:
		result.Completeness = "partial"
		result.Notes = append(result.Notes, fmt.Sprintf("unknown operation %q; expected one of orient, search, expand, trace, compare, synthesize", query.Operation))
	}
	return result
}

type exploreEngine struct {
	knowledge  []Knowledge
	byID       map[string]Knowledge
	tokensByID map[string]map[string]bool
	idf        func(token string) float64
}

// knowledgeItemTokens builds the searchable feature set of one item:
// proposition, topics, entities and the id itself (ids are valid search
// handles too).
func knowledgeItemTokens(item Knowledge) map[string]bool {
	tokens := knowledgeFeatureTokens(item.Proposition)
	for _, topic := range item.Topics {
		for token := range knowledgeFeatureTokens(topic) {
			tokens[token] = true
		}
	}
	for _, entity := range item.Entities {
		for token := range knowledgeFeatureTokens(entity) {
			tokens[token] = true
		}
	}
	for token := range knowledgeFeatureTokens(item.ID) {
		tokens[token] = true
	}
	return tokens
}

func normalizeExploreItems(value int) int {
	if value <= 0 {
		return ExploreDefaultItems
	}
	if value > ExploreMaxItems {
		return ExploreMaxItems
	}
	return value
}

func normalizeExploreDepth(value int) int {
	if value <= 0 {
		return ExploreDefaultDepth
	}
	if value > ExploreMaxDepth {
		return ExploreMaxDepth
	}
	return value
}

// visibleTargets resolves query targets against the visible base; unknown ids
// become notes instead of errors so one stale handle cannot abort research.
func (e exploreEngine) visibleTargets(result *ExploreResult, targets []string) []Knowledge {
	var found []Knowledge
	seen := make(map[string]bool)
	for _, id := range targets {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if item, ok := e.byID[id]; ok {
			found = append(found, item)
		} else {
			result.Notes = append(result.Notes, fmt.Sprintf("target %q is not visible in this project", id))
		}
	}
	return found
}

// matchItem scores one item against the query tokens: weighted idf over
// matched terms with entity/topic matches boosted and explained.
func (e exploreEngine) matchItem(item Knowledge, queryTokens map[string]bool) ([]string, float64) {
	if len(queryTokens) == 0 {
		return nil, 0
	}
	tokens := e.tokensByID[item.ID]
	entityTokens := make(map[string]bool)
	for _, entity := range item.Entities {
		for token := range knowledgeFeatureTokens(entity) {
			entityTokens[token] = true
		}
	}
	topicTokens := make(map[string]bool)
	for _, topic := range item.Topics {
		for token := range knowledgeFeatureTokens(topic) {
			topicTokens[token] = true
		}
	}
	var matched []string
	score := 0.0
	for token := range queryTokens {
		weight := 0.0
		switch {
		case entityTokens[token]:
			weight, matched = 3*e.idf(token), append(matched, "entity:"+token)
		case topicTokens[token]:
			weight, matched = 2*e.idf(token), append(matched, "topic:"+token)
		case tokens[token]:
			weight, matched = e.idf(token), append(matched, "term:"+token)
		}
		score += weight
	}
	sort.Strings(matched)
	return matched, score
}

// searchItems ranks the whole base against a question; strong matches need
// either multiple term hits or an entity/topic hit, mirroring the priming
// matcher's noise guard without sharing its implementation.
func (e exploreEngine) searchItems(question string) ([]ExploreKnowledgeCard, bool) {
	queryTokens := knowledgeFeatureTokens(question)
	if len(queryTokens) == 0 {
		return nil, false
	}
	var cards []ExploreKnowledgeCard
	for _, item := range e.knowledge {
		matched, score := e.matchItem(item, queryTokens)
		if len(matched) == 0 {
			continue
		}
		hasAnchor := false
		for _, entry := range matched {
			if strings.HasPrefix(entry, "entity:") || strings.HasPrefix(entry, "topic:") {
				hasAnchor = true
				break
			}
		}
		if !hasAnchor && len(matched) == 1 && len(queryTokens) >= 4 {
			continue
		}
		cards = append(cards, exploreCard(item, matched, score))
	}
	sortExploreCards(cards)
	return cards, true
}

func exploreCard(item Knowledge, matched []string, score float64) ExploreKnowledgeCard {
	return ExploreKnowledgeCard{
		KnowledgeID: item.ID, Proposition: item.Proposition, State: item.State, Kind: item.Kind,
		Topics: boundedStrings(item.Topics, 8), Entities: boundedStrings(item.Entities, 8),
		Project: item.Project, ScopeKind: item.ScopeKind, ScopeID: item.ScopeID,
		MatchedBy: boundedStrings(matched, 8), Score: math.Round(score*1000) / 1000,
		AtRisk: item.AtRisk, Replacement: item.Replacement, Evidence: len(item.Evidence),
	}
}

func sortExploreCards(cards []ExploreKnowledgeCard) {
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].Score != cards[j].Score {
			return cards[i].Score > cards[j].Score
		}
		return cards[i].KnowledgeID < cards[j].KnowledgeID
	})
}

// orient builds the compact topic/entity map of the visible base, ranked by
// match with the question (§4.1). Areas are navigation views computed for this
// call; they never persist as an ontology.
func (e exploreEngine) orient(result *ExploreResult, query ExploreQuery) {
	limit := result.Limits.MaxItems
	type areaAggregate struct {
		name    string
		kind    string
		items   []Knowledge
		itemIDs map[string]bool
		tokens  map[string]bool
	}
	// One name = one area regardless of whether the value was tagged as a
	// topic or an entity: two same-named tiles read as duplicates to the user
	// (the kind stays for the fallback label, topics win). An item is counted
	// once per area even when both its topic and entity share the name.
	areas := make(map[string]*areaAggregate)
	addArea := func(name, kind string, item Knowledge) {
		key := name
		area, ok := areas[key]
		if !ok {
			area = &areaAggregate{name: name, kind: kind, tokens: make(map[string]bool), itemIDs: make(map[string]bool)}
			areas[key] = area
		} else if area.kind != kind && kind == "topic" {
			area.kind = "topic"
		}
		if area.itemIDs[item.ID] {
			return
		}
		area.itemIDs[item.ID] = true
		area.items = append(area.items, item)
		for token := range knowledgeFeatureTokens(name) {
			area.tokens[token] = true
		}
	}
	for _, item := range e.knowledge {
		for _, topic := range item.Topics {
			addArea(strings.ToLower(strings.TrimSpace(topic)), "topic", item)
		}
		for _, entity := range item.Entities {
			addArea(strings.ToLower(strings.TrimSpace(entity)), "entity", item)
		}
	}
	queryTokens := knowledgeFeatureTokens(query.Question)
	var scoredAreas []ExploreArea
	for _, area := range areas {
		if len(area.items) == 0 {
			continue
		}
		card := ExploreArea{Name: area.name, Kind: area.kind, KnowledgeCount: len(area.items), States: make(map[string]int)}
		var matched []string
		if len(queryTokens) > 0 {
			for token := range queryTokens {
				if area.tokens[token] {
					card.Score += e.idf(token)
					matched = append(matched, "term:"+token)
				}
			}
			sort.Strings(matched)
		}
		card.Score = math.Round(card.Score*1000) / 1000
		card.MatchedBy = boundedStrings(matched, 8)
		for _, item := range area.items {
			card.States[item.State]++
			if item.State == "challenged" || itemAtRiskContradicted(item) {
				card.Contradictions++
			}
		}
		scoredAreas = append(scoredAreas, card)
	}
	// Rank: question match first; without a question (or on ties) size and
	// then the alphabetical name decide — fully deterministic.
	sort.Slice(scoredAreas, func(i, j int) bool {
		if scoredAreas[i].Score != scoredAreas[j].Score {
			return scoredAreas[i].Score > scoredAreas[j].Score
		}
		if scoredAreas[i].KnowledgeCount != scoredAreas[j].KnowledgeCount {
			return scoredAreas[i].KnowledgeCount > scoredAreas[j].KnowledgeCount
		}
		if scoredAreas[i].Kind != scoredAreas[j].Kind {
			return scoredAreas[i].Kind < scoredAreas[j].Kind
		}
		return scoredAreas[i].Name < scoredAreas[j].Name
	})
	if len(scoredAreas) > limit {
		scoredAreas = scoredAreas[:limit]
		result.Limits.Truncated = true
		result.Completeness = "truncated"
	}
	// Representative items: the three best question matches inside each area
	// (or the freshest when there is no question).
	for i := range scoredAreas {
		areaKey := scoredAreas[i].Name
		area := areas[areaKey]
		var cards []ExploreKnowledgeCard
		for _, item := range area.items {
			matched, score := e.matchItem(item, queryTokens)
			cards = append(cards, exploreCard(item, matched, score))
		}
		sortExploreCards(cards)
		if len(cards) > 3 {
			cards = cards[:3]
		}
		if len(cards) == 0 {
			cards = nil
		}
		scoredAreas[i].Representative = cards
	}
	if scoredAreas == nil {
		scoredAreas = []ExploreArea{}
	}
	result.Areas = scoredAreas
	if len(result.Areas) == 0 {
		result.Gaps = append(result.Gaps, "no topics or entities are recorded in the visible knowledge base yet")
	}
	result.NextActions = append(result.NextActions, "search: pick the most relevant area name as the question keyword")
	result.Limits.ItemsReturned = len(result.Areas)
}

func itemAtRiskContradicted(item Knowledge) bool {
	for _, relation := range item.Relationships {
		if relation.Type == "contradicts" {
			return true
		}
	}
	return false
}

// search finds knowledge by approximate meaning through deterministic
// lexical matching with idf weighting (§4.2). All lifecycle states are
// returned and labelled — relevance and veracity stay separate axes.
func (e exploreEngine) search(result *ExploreResult, query ExploreQuery) {
	cards, hasQuery := e.searchItems(query.Question)
	if !hasQuery {
		result.Completeness = "partial"
		result.Notes = append(result.Notes, "question is empty or carries no indexable tokens")
		return
	}
	if len(cards) > result.Limits.MaxItems {
		cards = cards[:result.Limits.MaxItems]
		result.Results = cards
		result.Limits.Truncated = true
		result.Completeness = "truncated"
		result.Limits.ItemsReturned = len(cards)
		result.NextActions = append(result.NextActions, "more matches exist beyond max_items: narrow the question or raise max_items")
		return
	}
	if len(cards) == 0 {
		result.Gaps = append(result.Gaps, "no knowledge matches the question in the visible base")
		result.NextActions = append(result.NextActions, "orient: map the area first — the question wording may differ from recorded propositions")
		return
	}
	result.Results = cards
	result.Limits.ItemsReturned = len(cards)
	var retired int
	for _, card := range cards {
		if card.State == "corrected" || card.State == "superseded" || card.State == "invalidated" {
			retired++
		}
	}
	result.NextActions = append(result.NextActions, fmt.Sprintf("expand: walk relations from the top match (e.g. %s) to see support and contradictions", cards[0].KnowledgeID))
	if retired > 0 {
		result.Notes = append(result.Notes, fmt.Sprintf("%d of the matches are retired statements; treat them as history, not live rules", retired))
	}
}

// expand walks the relation neighborhood of the targets with an explicit
// depth cap and a visited set (§4.3): outgoing relationships, incoming links,
// replacement chains. Candidate relations (shared topics) are reported only on
// request and always marked.
func (e exploreEngine) expand(result *ExploreResult, query ExploreQuery) {
	targets := e.visibleTargets(result, query.Targets)
	if len(targets) == 0 {
		result.Completeness = "partial"
		return
	}
	relationsFilter := make(map[string]bool, len(query.Relations))
	for _, relation := range query.Relations {
		relation = strings.TrimSpace(relation)
		if relation == "" {
			continue
		}
		if !containsString(ExploreRelationTypes, relation) {
			result.Notes = append(result.Notes, fmt.Sprintf("relation type %q is not part of the vocabulary; ignored", relation))
			continue
		}
		relationsFilter[relation] = true
	}
	allowed := func(relationType string) bool {
		return len(relationsFilter) == 0 || relationsFilter[relationType]
	}
	var cards []ExploreKnowledgeCard
	relations := make(map[string]ExploreRelation)
	visited := make(map[string]bool)
	type frontierEntry struct {
		id    string
		depth int
	}
	frontier := make([]frontierEntry, 0, len(targets))
	for _, item := range targets {
		visited[item.ID] = true
		frontier = append(frontier, frontierEntry{id: item.ID, depth: 0})
		cards = append(cards, exploreCard(item, nil, 0))
	}
	recordRelation := func(fromID, toID, relationType, eventID string, depth int, basis, note string) {
		key := fromID + "\x00" + toID + "\x00" + relationType + "\x00" + basis
		if existing, ok := relations[key]; ok && existing.Depth <= depth {
			return
		}
		relations[key] = ExploreRelation{FromID: fromID, ToID: toID, Type: relationType, Basis: basis, EventID: eventID, Depth: depth, Note: note}
	}
	// The walk budget bounds both result lists by construction: once max_items
	// cards are collected no new nodes enter the frontier (§7.2 — bounded
	// walk), and the truncation flag reports that the graph was cut.
	budgetReached := func() bool {
		if len(cards) >= result.Limits.MaxItems {
			result.Limits.Truncated = true
			result.Completeness = "truncated"
			return true
		}
		return false
	}
	// BFS with depth guard; the visited set doubles as cycle protection.
	for len(frontier) > 0 {
		current := frontier[0]
		frontier = frontier[1:]
		if current.depth >= result.Limits.MaxDepth {
			continue
		}
		item, ok := e.byID[current.id]
		if !ok {
			continue
		}
		nextDepth := current.depth + 1
		enqueue := func(targetID, relationType, eventID, basis, note string) {
			neighbor, exists := e.byID[targetID]
			if !exists {
				// The caller filtered visibility; a dangling reference must be
				// reported, not silently dropped (§6.4).
				if basis == "explicit" {
					result.Notes = append(result.Notes, fmt.Sprintf("knowledge %s references %s %q outside the visible base", current.id, relationType, targetID))
				}
				return
			}
			recordRelation(current.id, targetID, relationType, eventID, nextDepth, basis, note)
			if !visited[targetID] {
				if budgetReached() {
					return
				}
				visited[targetID] = true
				card := exploreCard(neighbor, nil, 0)
				card.Depth = nextDepth
				card.Via = relationType
				card.Basis = basis
				cards = append(cards, card)
				frontier = append(frontier, frontierEntry{id: targetID, depth: nextDepth})
			}
		}
		for _, relation := range item.Relationships {
			if !allowed(relation.Type) {
				continue
			}
			enqueue(relation.TargetID, relation.Type, relation.EventID, "explicit", "")
		}
		if item.Replacement != "" && allowed("supersedes") {
			enqueue(item.Replacement, "supersedes", "", "explicit", "replacement_id")
		}
		// Incoming links: who depends on / supports / contradicts this item.
		for _, other := range e.knowledge {
			for _, relation := range other.Relationships {
				if relation.TargetID != item.ID || !allowed(relation.Type) {
					continue
				}
				recordRelation(other.ID, item.ID, relation.Type, relation.EventID, nextDepth, "explicit", "incoming")
				if visited[other.ID] {
					continue
				}
				if budgetReached() {
					continue
				}
				card := exploreCard(other, nil, 0)
				card.Depth = nextDepth
				card.Via = relation.Type
				card.Basis = "explicit"
				cards = append(cards, card)
				visited[other.ID] = true
				frontier = append(frontier, frontierEntry{id: other.ID, depth: nextDepth})
			}
		}
	}
	if query.IncludeCandidates {
		// Candidate edges: shared topics with the start set, marked as
		// heuristic and never persisted (§8.1).
		targetTopics := make(map[string][]string)
		for _, item := range targets {
			for _, topic := range item.Topics {
				normalized := strings.ToLower(strings.TrimSpace(topic))
				if normalized != "" {
					targetTopics[normalized] = append(targetTopics[normalized], item.ID)
				}
			}
		}
		for _, other := range e.knowledge {
			if visited[other.ID] {
				continue
			}
			for _, topic := range other.Topics {
				normalized := strings.ToLower(strings.TrimSpace(topic))
				owners, shared := targetTopics[normalized]
				if !shared {
					continue
				}
				// A pair already confirmed (an explicit edge would duplicate)
				// or reviewed-and-dismissed never re-enters the candidate pool:
				// curator verdicts must be sticky, or every explore recomputes
				// the very suggestion the judge rejected an hour ago. Either
				// endpoint's verdict suppresses the pair.
				confirmed := false
				for _, owner := range owners {
					ownerItem, ok := e.byID[owner]
					if !ok {
						continue
					}
					if pairHasRelation(ownerItem, other.ID) || pairDismissed(ownerItem, other.ID) || pairHasRelation(other, owner) || pairDismissed(other, owner) {
						confirmed = true
						break
					}
				}
				if confirmed {
					continue
				}
				if budgetReached() {
					break
				}
				card := exploreCard(other, nil, 0)
				card.Depth = 1
				card.Via = "related_to"
				card.Basis = "candidate"
				cards = append(cards, card)
				visited[other.ID] = true
				recordRelation(owners[0], other.ID, "related_to", "", 1, "candidate", "shared topic: "+normalized)
				break
			}
		}
	}
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].Depth != cards[j].Depth {
			return cards[i].Depth < cards[j].Depth
		}
		return cards[i].KnowledgeID < cards[j].KnowledgeID
	})
	if len(cards) > result.Limits.MaxItems {
		cards = cards[:result.Limits.MaxItems]
		result.Limits.Truncated = true
		result.Completeness = "truncated"
	}
	relationList := make([]ExploreRelation, 0, len(relations))
	for _, relation := range relations {
		relationList = append(relationList, relation)
	}
	sort.Slice(relationList, func(i, j int) bool {
		if relationList[i].Depth != relationList[j].Depth {
			return relationList[i].Depth < relationList[j].Depth
		}
		if relationList[i].FromID != relationList[j].FromID {
			return relationList[i].FromID < relationList[j].FromID
		}
		return relationList[i].ToID < relationList[j].ToID
	})
	result.Results = cards
	if relationList == nil {
		relationList = []ExploreRelation{}
	}
	result.Relations = relationList
	result.Limits.ItemsReturned = len(cards)
	if len(cards) <= len(targets) {
		result.Gaps = append(result.Gaps, "targets have no explicit relations in the visible base")
		result.NextActions = append(result.NextActions, "compare or search around the target topics: relations may simply not be recorded yet")
	}
}

// trace reconstructs the history of each target from the recorded transition
// log — never from the current state alone (§4.4).
func (e exploreEngine) trace(result *ExploreResult, query ExploreQuery) {
	targets := e.visibleTargets(result, query.Targets)
	if len(targets) == 0 {
		result.Completeness = "partial"
		return
	}
	for _, item := range targets {
		trace := ExploreHistory{
			KnowledgeID: item.ID, Proposition: item.Proposition, State: item.State,
			CreatedAt: item.CreatedAt, CreatedBy: item.CreatedBy.ID,
			Usage: ExploreTraceUsage{
				HintOffers: item.HintOffers, HintUses: item.HintUses, HintIgnores: item.HintIgnores,
				HelpfulOutcomes: item.HelpfulOutcomes, HarmfulOutcomes: item.HarmfulOutcomes, ReuseCount: item.ReuseCount,
			},
			Replacement: item.Replacement, AtRisk: item.AtRisk, Evidence: boundedEvidence(item.Evidence, 8),
		}
		for _, transition := range item.History {
			trace.Steps = append(trace.Steps, ExploreHistoryStep{
				EventID: transition.EventID, Type: transition.Type, State: transition.State, Rule: transition.Rule,
				At: transition.At, ActorID: transition.Actor.ID, Reason: transition.Reason,
			})
		}
		if len(trace.Steps) > result.Limits.MaxItems {
			trace.Steps = trace.Steps[:result.Limits.MaxItems]
			result.Limits.Truncated = true
			result.Completeness = "truncated"
			result.Notes = append(result.Notes, fmt.Sprintf("history of %s cut at max_items; older steps omitted", item.ID))
		}
		result.Traces = append(result.Traces, trace)
	}
	result.Limits.ItemsReturned = len(result.Traces)
	if len(result.Traces) > 0 {
		result.NextActions = append(result.NextActions, "expand: follow replacement_id or contradicts links found in the history")
	}
}

// compare surfaces contradictions among the targets (or the whole visible base
// when no targets are given), separating explicit contradictions from
// same-topic-different-state candidates and replacement-resolved pairs
// (§4.5). It never decides truth: newer ≠ true, and context splits are
// reported instead of assumed away.
func (e exploreEngine) compare(result *ExploreResult, query ExploreQuery) {
	targets := e.visibleTargets(result, query.Targets)
	conflicts := e.collectConflicts(targets)
	if len(conflicts) > result.Limits.MaxItems {
		conflicts = conflicts[:result.Limits.MaxItems]
		result.Limits.Truncated = true
		result.Completeness = "truncated"
	}
	if conflicts == nil {
		conflicts = []ExploreConflict{}
	}
	result.Conflicts = conflicts
	result.Limits.ItemsReturned = len(conflicts)
	if len(conflicts) == 0 {
		result.Gaps = append(result.Gaps, "no explicit contradictions found in the compared set")
		result.Notes = append(result.Notes, "absence of recorded contradictions is not proof of consistency: most pairs simply have no linking event")
		result.NextActions = append(result.NextActions, "compare with explicit targets when you suspect two statements disagree")
		return
	}
	result.NextActions = append(result.NextActions, "trace: check the provenance of each side before deciding which applies")
}

// collectConflicts enumerates conflicts: every explicit contradicts relation
// within the working set, plus candidate pairs sharing a topic with diverging
// lifecycle states, plus replacement resolutions. Enumeration is bounded by
// grouping items per topic and sorting deterministically.
func (e exploreEngine) collectConflicts(targets []Knowledge) []ExploreConflict {
	var conflicts []ExploreConflict
	seen := make(map[string]bool)
	workingSet := make(map[string]bool, len(targets))
	for _, item := range targets {
		workingSet[item.ID] = true
	}
	inSet := func(id string) bool {
		if len(workingSet) == 0 {
			return true
		}
		return workingSet[id]
	}
	addConflict := func(conflict ExploreConflict) {
		key := strings.Join(conflict.KnowledgeIDs, "\x00") + "\x00" + conflict.Basis
		if seen[key] {
			return
		}
		seen[key] = true
		conflicts = append(conflicts, conflict)
	}
	// Explicit contradictions: direction-insensitive, both orders collapse.
	reportedExplicit := make(map[string]bool)
	for _, item := range e.knowledge {
		if !inSet(item.ID) {
			continue
		}
		for _, relation := range item.Relationships {
			if relation.Type != "contradicts" {
				continue
			}
			pair := item.ID + "\x00" + relation.TargetID
			reverse := relation.TargetID + "\x00" + item.ID
			if reportedExplicit[pair] || reportedExplicit[reverse] {
				continue
			}
			reportedExplicit[pair], reportedExplicit[reverse] = true, true
			other, known := e.byID[relation.TargetID]
			if !inSet(relation.TargetID) || !known {
				continue
			}
			conflict := ExploreConflict{
				KnowledgeIDs: sortedPair(item.ID, relation.TargetID),
				Basis:        "explicit:contradicts",
				Detail:       fmt.Sprintf("knowledge.linked event %s records an explicit contradiction", relation.EventID),
			}
			conflict.ContextDifferences = contextDifferences(item, other)
			addConflict(conflict)
		}
		// Replacement-resolved pairs are reported as resolved conflicts: the
		// tension is real, but the system already carries the successor.
		if item.Replacement != "" {
			if replacement, known := e.byID[item.Replacement]; known && inSet(item.Replacement) {
				conflict := ExploreConflict{
					KnowledgeIDs: sortedPair(item.ID, item.Replacement),
					Basis:        "resolved:replacement",
					Detail:       fmt.Sprintf("%s is retired and points to %s as its replacement", item.ID, item.Replacement),
					Resolved:     true,
					Resolution:   fmt.Sprintf("apply %s; %s remains as history", item.Replacement, item.ID),
				}
				conflict.ContextDifferences = contextDifferences(item, replacement)
				addConflict(conflict)
			}
		}
	}

	// Candidate conflicts: same topic, diverging states (one side doubted or
	// retired while the other lives). Heuristic by design and labelled as
	// such; bounded to adjacent state-divergent pairs per topic.
	byTopic := make(map[string][]Knowledge)
	for _, item := range e.knowledge {
		for _, topic := range item.Topics {
			normalized := strings.ToLower(strings.TrimSpace(topic))
			if normalized == "" {
				continue
			}
			byTopic[normalized] = append(byTopic[normalized], item)
		}
	}
	for _, topic := range sortedKeys(byTopic) {
		items := byTopic[topic]
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		for i := range items {
			for j := i + 1; j < len(items); j++ {
				left, right := items[i], items[j]
				if !inSet(left.ID) || !inSet(right.ID) {
					continue
				}
				// Already-resolved pairs (an explicit contradict edge, or a
				// curator verdict) are not heuristic candidates anymore.
				if pairHasRelation(left, right.ID) || pairDismissed(left, right.ID) {
					continue
				}
				if stateWeight(left.State) == stateWeight(right.State) {
					continue
				}
				if left.Proposition == right.Proposition {
					continue
				}
				// A shared topic word alone is not a contradiction signal:
				// generic topics ("tooling", "testing") collect unrelated
				// statements from every run. Require real text overlap — two
				// meaningful tokens beyond the topic word itself — before the
				// pair is worth a human's queue slot.
				if contentTokenOverlap(left.Proposition, right.Proposition) < 2 {
					continue
				}
				conflict := ExploreConflict{
					KnowledgeIDs:       sortedPair(left.ID, right.ID),
					Basis:              "candidate:same-topic-different-state",
					Detail:             fmt.Sprintf("items share topic %q but carry states %s and %s; this is a heuristic candidate, not a recorded contradiction", topic, left.State, right.State),
					ContextDifferences: contextDifferences(left, right),
				}
				addConflict(conflict)
				break
			}
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Basis != conflicts[j].Basis {
			return conflicts[i].Basis < conflicts[j].Basis
		}
		return strings.Join(conflicts[i].KnowledgeIDs, "\x00") < strings.Join(conflicts[j].KnowledgeIDs, "\x00")
	})
	return conflicts
}

// contentTokenOverlap counts the meaningful tokens two statements share
// (stopwords and the topic word itself do not count): the same-topic heuristic
// needs real text overlap, or generic topics ("tooling", "testing") would
// pair unrelated statements from every run into the contradictions queue.
func contentTokenOverlap(left, right string) int {
	l := contentTokens(left)
	r := contentTokens(right)
	n := 0
	for token := range l {
		if r[token] {
			n++
		}
	}
	return n
}

// stateWeight maps lifecycle states onto a doubt scale: live proposals and
// confirmations weigh 0, doubt 1, retirement 2. Equal weights mean no
// candidate conflict.
func stateWeight(state string) int {
	switch state {
	case "proposed", "confirmed":
		return 0
	case "challenged":
		return 1
	default: // corrected, superseded, invalidated
		return 2
	}
}

func sortedPair(a, b string) []string {
	if a > b {
		a, b = b, a
	}
	return []string{a, b}
}

// contextDifferences lists the material applicability splits between two
// items — exactly the reasons they may both be true in their own niches
// (§4.5: never auto-decide that newer wins).
func contextDifferences(a, b Knowledge) []string {
	var differences []string
	if a.Project != b.Project {
		differences = append(differences, fmt.Sprintf("projects differ: %q vs %q", a.Project, b.Project))
	}
	if a.ScopeKind != b.ScopeKind || a.ScopeID != b.ScopeID {
		differences = append(differences, fmt.Sprintf("scopes differ: %s/%s vs %s/%s", a.ScopeKind, a.ScopeID, b.ScopeKind, b.ScopeID))
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		differences = append(differences, fmt.Sprintf("created %s vs %s", a.CreatedAt.Format(time.RFC3339), b.CreatedAt.Format(time.RFC3339)))
	}
	return differences
}

func sortedKeys[V any](mapping map[string]V) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// synthesize assembles the research dossier over the working set: explicit
// targets when given, otherwise the question's best matches (§4.6).
func (e exploreEngine) synthesize(result *ExploreResult, query ExploreQuery) {
	workingSet := e.visibleTargets(result, query.Targets)
	if len(workingSet) == 0 && strings.TrimSpace(query.Question) != "" {
		cards, _ := e.searchItems(query.Question)
		if len(cards) > result.Limits.MaxItems {
			cards = cards[:result.Limits.MaxItems]
			result.Limits.Truncated = true
			result.Completeness = "truncated"
		}
		for _, card := range cards {
			if item, ok := e.byID[card.KnowledgeID]; ok {
				workingSet = append(workingSet, item)
			}
		}
	}
	if len(workingSet) == 0 {
		result.Completeness = "partial"
		result.Gaps = append(result.Gaps, "nothing to synthesize: no targets and no question matches in the visible base")
		return
	}
	synthesis := ExploreSynthesis{Interpretation: true}
	ids := make(map[string]bool, len(workingSet))
	for _, item := range workingSet {
		ids[item.ID] = true
		card := exploreCard(item, nil, 0)
		switch item.State {
		case "confirmed":
			synthesis.Established = append(synthesis.Established, card)
		case "challenged", "proposed":
			synthesis.Disputed = append(synthesis.Disputed, card)
		default:
			synthesis.Retired = append(synthesis.Retired, card)
		}
		synthesis.References = append(synthesis.References, item.ID)
	}
	sort.Slice(synthesis.Established, func(i, j int) bool {
		return synthesis.Established[i].KnowledgeID < synthesis.Established[j].KnowledgeID
	})
	sort.Slice(synthesis.Disputed, func(i, j int) bool { return synthesis.Disputed[i].KnowledgeID < synthesis.Disputed[j].KnowledgeID })
	sort.Slice(synthesis.Retired, func(i, j int) bool { return synthesis.Retired[i].KnowledgeID < synthesis.Retired[j].KnowledgeID })
	// Conflicts inside the working set keep the dossier honest about tension.
	conflicts := e.collectConflicts(workingSet)
	if len(conflicts) > 8 {
		conflicts = conflicts[:8]
	}
	synthesis.Conflicts = conflicts
	// Gaps: question coverage and evidence holes, computed heuristically and
	// labelled as interpretation by the struct flag.
	if tokens := knowledgeFeatureTokens(query.Question); len(tokens) > 0 {
		unmatched := 0
		for token := range tokens {
			found := false
			for _, item := range workingSet {
				if e.tokensByID[item.ID][token] {
					found = true
					break
				}
			}
			if !found {
				unmatched++
			}
		}
		if unmatched > 0 {
			synthesis.Gaps = append(synthesis.Gaps, fmt.Sprintf("%d of %d question tokens have no match in the working set", unmatched, len(tokens)))
		}
	}
	for _, item := range workingSet {
		if len(item.Evidence) == 0 && (item.State == "confirmed" || item.State == "proposed") {
			synthesis.Gaps = append(synthesis.Gaps, fmt.Sprintf("%s carries no recorded evidence", item.ID))
		}
	}
	if len(synthesis.Disputed) > 0 {
		synthesis.NextActions = append(synthesis.NextActions, "compare: resolve the disputed statements before relying on them")
	}
	for _, item := range workingSet {
		if len(item.Relationships) > 0 {
			synthesis.NextActions = append(synthesis.NextActions, fmt.Sprintf("expand %s: relations may change the picture", item.ID))
			break
		}
	}
	if len(synthesis.Retired) > 0 {
		synthesis.NextActions = append(synthesis.NextActions, "trace a retired item to learn why the earlier approach was abandoned")
	}
	synthesis.References = sortedUnique(synthesis.References)
	result.Synthesis = &synthesis
	result.Limits.ItemsReturned = len(workingSet)
}

func sortedUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
