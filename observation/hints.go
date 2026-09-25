package observation

import (
	"sort"
	"strings"
	"unicode"
)

type HintQuery struct {
	Text     string
	Entities []string
	Topics   []string
	Limit    int
}

type Hint struct {
	HintID       string                `json:"hint_id,omitempty"`
	OfferEventID string                `json:"offer_event_id,omitempty"`
	KnowledgeID  string                `json:"knowledge_id"`
	Proposition  string                `json:"proposition"`
	State        string                `json:"state"`
	MatchedBy    []string              `json:"matched_by"`
	Caution      string                `json:"caution,omitempty"`
	Evidence     []Evidence            `json:"evidence,omitempty"`
	History      []KnowledgeTransition `json:"history"`
}

type hintCandidate struct {
	hint      Hint
	matchTier int
	kindTier  int
	stateTier int
}

// FindHints uses explainable exact entity/topic and lexical matching. It does
// not estimate truth or confidence and returns lifecycle/provenance alongside
// each candidate so the caller can present memory separately from tool output.
func FindHints(knowledge []Knowledge, query HintQuery) []Hint {
	if query.Limit <= 0 || query.Limit > 20 {
		query.Limit = 8
	}
	queryEntities := normalizedSet(query.Entities)
	queryTopics := normalizedSet(query.Topics)
	queryTokens := contentTokens(query.Text)
	queryPhrase := strings.ToLower(strings.TrimSpace(query.Text))
	candidates := make([]hintCandidate, 0)
	direct := make(map[string]hintCandidate)
	itemsByID := make(map[string]Knowledge, len(knowledge))
	for _, item := range knowledge {
		itemsByID[item.ID] = item
		if item.State == "invalidated" || item.State == "superseded" || item.State == "corrected" {
			continue
		}
		matched, tier := matchKnowledge(item, queryEntities, queryTopics, queryTokens, queryPhrase)
		if len(matched) == 0 {
			continue
		}
		candidate, ok := makeHintCandidate(item, matched, tier)
		if !ok {
			continue
		}
		direct[item.ID] = candidate
		candidates = append(candidates, candidate)
	}
	// Add one-hop graph neighbors after direct matches. The relationship and
	// source node are returned as the reason, so this expansion is inspectable.
	relatedSeen := make(map[string]bool)
	for _, source := range knowledge {
		for _, relation := range source.Relationships {
			targetID := relation.TargetID
			if directSource, ok := direct[source.ID]; ok {
				if _, already := direct[targetID]; already || relatedSeen[targetID] {
					continue
				}
				if target, found := itemsByID[targetID]; found {
					candidate, eligible := makeHintCandidate(target, []string{"related:" + relation.Type, "via:" + source.ID}, directSource.matchTier+1)
					if eligible {
						candidates = append(candidates, candidate)
						relatedSeen[targetID] = true
					}
				}
			}
			if directTarget, ok := direct[targetID]; ok {
				if _, already := direct[source.ID]; already || relatedSeen[source.ID] {
					continue
				}
				candidate, eligible := makeHintCandidate(source, []string{"related:" + relation.Type, "via:" + targetID}, directTarget.matchTier+1)
				if eligible {
					candidates = append(candidates, candidate)
					relatedSeen[source.ID] = true
				}
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].matchTier != candidates[j].matchTier {
			return candidates[i].matchTier < candidates[j].matchTier
		}
		// Kind ranks below match strength: an entity/topic-matched observation
		// still outranks a term-matched claim, but observations must not crowd
		// claims out of the budget at equal match strength.
		if candidates[i].kindTier != candidates[j].kindTier {
			return candidates[i].kindTier < candidates[j].kindTier
		}
		if candidates[i].stateTier != candidates[j].stateTier {
			return candidates[i].stateTier < candidates[j].stateTier
		}
		return candidates[i].hint.KnowledgeID < candidates[j].hint.KnowledgeID
	})
	if len(candidates) > query.Limit {
		candidates = candidates[:query.Limit]
	}
	result := make([]Hint, len(candidates))
	for i := range candidates {
		result[i] = candidates[i].hint
	}
	return result
}

func makeHintCandidate(item Knowledge, matched []string, matchTier int) (hintCandidate, bool) {
	if item.State == "invalidated" || item.State == "superseded" || item.State == "corrected" {
		return hintCandidate{}, false
	}
	caution := ""
	stateTier := 0
	switch item.State {
	case "confirmed":
	case "proposed":
		stateTier = 1
		caution = "unconfirmed hypothesis"
	case "challenged":
		stateTier = 2
		caution = "challenged knowledge; inspect the history before relying on it"
	default:
		return hintCandidate{}, false
	}
	if item.AtRisk {
		if caution != "" {
			caution += "; "
		}
		caution += "depends on retired knowledge: " + strings.Join(item.RiskSources, ", ")
	}
	kindTier := 0
	if item.Kind == "observation" {
		// Observations are cheap kernel side-products: every successful
		// verification command records one. Claims are the model's authored
		// conclusions, so at equal match strength a claim outranks an
		// observation and observations fill the remaining budget.
		kindTier = 1
	}
	return hintCandidate{hint: Hint{
		KnowledgeID: item.ID, Proposition: item.Proposition, State: item.State,
		MatchedBy: boundedStrings(matched, 8), Caution: caution,
		Evidence: boundedEvidence(item.Evidence, 8), History: boundedHistory(item.History, 5),
	}, matchTier: matchTier, kindTier: kindTier, stateTier: stateTier}, true
}

func matchKnowledge(item Knowledge, entities, topics, queryTokens map[string]bool, phrase string) ([]string, int) {
	matchedEntities := overlap(item.Entities, entities)
	matchedTopics := overlap(item.Topics, topics)
	if len(matchedEntities) > 0 {
		return prefixed("entity", matchedEntities), 0
	}
	if len(matchedTopics) > 0 {
		return prefixed("topic", matchedTopics), 1
	}
	text := strings.ToLower(item.Proposition)
	if phrase != "" && len(contentTokens(phrase)) >= 2 && strings.Contains(text, phrase) {
		return []string{"phrase"}, 2
	}
	itemTokens := contentTokens(item.Proposition)
	matched := make([]string, 0)
	for token := range queryTokens {
		if itemTokens[token] {
			matched = append(matched, token)
		}
	}
	if len(matched) == 0 || (len(queryTokens) > 1 && len(matched) < 2) {
		return nil, 99
	}
	sort.Strings(matched)
	return prefixed("term", matched), 3
}

func normalizedSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			result[value] = true
		}
	}
	return result
}

func overlap(values []string, query map[string]bool) []string {
	matched := make([]string, 0)
	seen := make(map[string]bool)
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if query[normalized] && !seen[normalized] {
			matched = append(matched, normalized)
			seen[normalized] = true
		}
	}
	sort.Strings(matched)
	return matched
}

func prefixed(prefix string, values []string) []string {
	if len(values) > 8 {
		values = values[:8]
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = prefix + ":" + value
	}
	return result
}

func boundedStrings(values []string, limit int) []string {
	if len(values) > limit {
		values = values[:limit]
	}
	return append([]string(nil), values...)
}

func boundedEvidence(values []Evidence, limit int) []Evidence {
	if len(values) > limit {
		values = values[len(values)-limit:]
	}
	return append([]Evidence(nil), values...)
}

func boundedHistory(values []KnowledgeTransition, limit int) []KnowledgeTransition {
	if len(values) > limit {
		values = values[len(values)-limit:]
	}
	return append([]KnowledgeTransition(nil), values...)
}

func contentTokens(value string) map[string]bool {
	const stopwords = "a an and are as at be by for from in into is it of on or the to with this that was were как для или это что при по из на с и к от"
	ignored := make(map[string]bool)
	for _, word := range strings.Fields(stopwords) {
		ignored[word] = true
	}
	result := make(map[string]bool)
	var builder strings.Builder
	flush := func() {
		word := builder.String()
		if len([]rune(word)) > 1 && !ignored[word] {
			result[word] = true
		}
		builder.Reset()
	}
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			builder.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return result
}
