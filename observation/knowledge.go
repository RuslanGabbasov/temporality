package observation

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type KnowledgeTransition struct {
	EventID  string     `json:"event_id"`
	SourceID string     `json:"source_id"`
	Type     string     `json:"type"`
	State    string     `json:"state"`
	Rule     string     `json:"rule,omitempty"`
	At       time.Time  `json:"at"`
	Actor    Actor      `json:"actor,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Reason   string     `json:"reason,omitempty"`
}

type Knowledge struct {
	ID          string `json:"id"`
	Proposition string `json:"proposition"`
	// Kind distinguishes model-authored claims from kernel heuristic
	// observations ("claim" vs "observation"). Events recorded before kinds
	// existed project as claims, which is the original authorship model.
	Kind     string   `json:"kind,omitempty"`
	Topics   []string `json:"topics,omitempty"`
	Entities []string `json:"entities,omitempty"`
	State    string   `json:"state"`
	Project  string   `json:"project,omitempty"`
	// ScopeKind/ScopeID carry the effective visibility of the item
	// (docs/knowledge-evolution.md §5). Empty ScopeKind means "project", the
	// default birth scope; org_unit and organization are widened only through
	// explicit knowledge.promoted events (or by being proposed that way).
	ScopeKind string `json:"scope_kind,omitempty"`
	ScopeID   string `json:"scope_id,omitempty"`
	// PromotedBy/PromotedAt record the latest scope widening provenance.
	PromotedBy      Actor                 `json:"promoted_by,omitempty"`
	PromotedAt      *time.Time            `json:"promoted_at,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
	CreatedBy       Actor                 `json:"created_by,omitempty"`
	ReuseCount      int                   `json:"reuse_count"`
	HintOffers      int                   `json:"hint_offers"`
	HintUses        int                   `json:"hint_uses"`
	HintIgnores     int                   `json:"hint_ignores"`
	HelpfulOutcomes int                   `json:"helpful_outcomes"`
	HarmfulOutcomes int                   `json:"harmful_outcomes"`
	Relationships   []KnowledgeRelation   `json:"relationships,omitempty"`
	AtRisk          bool                  `json:"at_risk,omitempty"`
	RiskSources     []string              `json:"risk_sources,omitempty"`
	LastUsedAt      *time.Time            `json:"last_used_at,omitempty"`
	Replacement     string                `json:"replacement_id,omitempty"`
	Evidence        []Evidence            `json:"evidence,omitempty"`
	History         []KnowledgeTransition `json:"history"`
}

type KnowledgeRelation struct {
	Type     string `json:"type"`
	TargetID string `json:"target_id"`
	EventID  string `json:"event_id"`
}

func ProjectKnowledge(events []Event) ([]Knowledge, error) {
	byID := make(map[string]*Knowledge)
	hintKnowledge := make(map[string]string)
	// Promotions are emitted by the journal while proposals come from kernels:
	// clock skew between the two can sort a promotion before its proposal in a
	// merged stream. Deferred promotions are applied as soon as the proposal
	// lands (and still fail loudly at the end if it never does).
	pendingPromoted := make(map[string][]Event)
	applyPromoted := func(item *Knowledge, event Event) {
		// Scope widening is not a lifecycle state change: it re-targets
		// visibility while leaving the state machine untouched, so it is
		// handled before the transition guards (like knowledge.linked).
		item.ScopeKind = knowledgeScopeKind(event.Data)
		item.ScopeID = knowledgeScopeID(event.Data)
		item.PromotedBy = event.Context.Actor
		promotedAt := event.OccurredAt
		item.PromotedAt = &promotedAt
		item.UpdatedAt = event.OccurredAt
		item.Evidence = appendUniqueEvidence(item.Evidence, event.Evidence...)
		appendKnowledgeTransition(item, event)
	}
	for _, event := range events {
		if event.Type == "hint.offered" {
			hintID := stringValue(event.Data, "hint_id")
			knowledgeID := stringValue(event.Data, "knowledge_id")
			if hintID != "" && knowledgeID != "" {
				hintKnowledge[hintID] = knowledgeID
			}
		}
	}
	for _, event := range events {
		if event.Type == "hint.offered" {
			id := stringValue(event.Data, "knowledge_id")
			item := byID[id]
			if item == nil {
				return nil, fmt.Errorf("hint event %s refers to unknown knowledge %q", event.EventID, id)
			}
			item.HintOffers++
			item.UpdatedAt = event.OccurredAt
			appendKnowledgeTransition(item, event)
			continue
		}
		if event.Type == "hint.used" || event.Type == "hint.ignored" || event.Type == "hint.outcome" {
			hintID := stringValue(event.Data, "hint_id")
			id := stringValue(event.Data, "knowledge_id")
			if id == "" {
				id = hintKnowledge[hintID]
			}
			item := byID[id]
			if item == nil {
				return nil, fmt.Errorf("hint feedback event %s refers to unknown hint %q", event.EventID, hintID)
			}
			item.UpdatedAt = event.OccurredAt
			switch event.Type {
			case "hint.used":
				item.HintUses++
				item.ReuseCount++
				usedAt := event.OccurredAt
				item.LastUsedAt = &usedAt
			case "hint.ignored":
				item.HintIgnores++
			case "hint.outcome":
				switch stringValue(event.Data, "outcome") {
				case "helpful":
					item.HelpfulOutcomes++
				case "harmful":
					item.HarmfulOutcomes++
				}
			}
			appendKnowledgeTransition(item, event)
			continue
		}
		if len(event.Type) < len("knowledge.") || event.Type[:len("knowledge.")] != "knowledge." {
			continue
		}
		// Extraction markers are run observability (§24 of the extraction
		// design): they carry no knowledge_id and are not lifecycle transitions.
		if strings.HasPrefix(event.Type, "knowledge.extraction.") {
			continue
		}
		id := stringValue(event.Data, "knowledge_id")
		if id == "" {
			return nil, fmt.Errorf("event %s has no knowledge_id", event.EventID)
		}
		item := byID[id]
		if event.Type == "knowledge.proposed" {
			proposition := stringValue(event.Data, "proposition")
			if item != nil {
				if item.Proposition != proposition {
					return nil, fmt.Errorf("knowledge %q was proposed twice with different propositions", id)
				}
				// An identical re-proposal is idempotent: concurrent producers may
				// race past their lookups, and re-observing the same fact must
				// strengthen the existing node, not duplicate it.
				item.UpdatedAt = event.OccurredAt
				item.Evidence = appendUniqueEvidence(item.Evidence, event.Evidence...)
				appendKnowledgeTransition(item, event)
				continue
			}
			if proposition == "" {
				return nil, fmt.Errorf("knowledge %q has an empty proposition", id)
			}
			item = &Knowledge{ID: id, Proposition: proposition, Kind: knowledgeKind(event.Data), Topics: stringList(event.Data["topics"]), Entities: stringList(event.Data["entities"]), State: "proposed", Project: event.Context.Project, ScopeKind: knowledgeScopeKind(event.Data), ScopeID: knowledgeScopeID(event.Data), CreatedAt: event.OccurredAt, UpdatedAt: event.OccurredAt, CreatedBy: event.Context.Actor, Evidence: append([]Evidence(nil), event.Evidence...)}
			byID[id] = item
			appendKnowledgeTransition(item, event)
			for _, promoted := range pendingPromoted[id] {
				applyPromoted(item, promoted)
			}
			delete(pendingPromoted, id)
			continue
		}
		if item == nil {
			if event.Type == "knowledge.promoted" {
				pendingPromoted[id] = append(pendingPromoted[id], event)
				continue
			}
			return nil, fmt.Errorf("knowledge event %s refers to unknown knowledge %q", event.EventID, id)
		}
		if event.Type == "knowledge.promoted" {
			applyPromoted(item, event)
			continue
		}
		if event.Type == "knowledge.linked" {
			targetID := stringValue(event.Data, "target_id")
			target := byID[targetID]
			if target == nil {
				return nil, fmt.Errorf("knowledge.linked event %s refers to unknown target %q", event.EventID, targetID)
			}
			if target.Project != "" && item.Project != "" && target.Project != item.Project {
				return nil, fmt.Errorf("knowledge relation %q crosses project boundary", event.EventID)
			}
			relation := KnowledgeRelation{Type: stringValue(event.Data, "relation"), TargetID: targetID, EventID: event.EventID}
			item.Relationships = appendUniqueRelation(item.Relationships, relation)
			item.UpdatedAt = event.OccurredAt
			appendKnowledgeTransition(item, event)
			continue
		}
		if event.Context.Project != "" && item.Project != "" && event.Context.Project != item.Project && item.ScopeKind != "org_unit" && item.ScopeKind != "organization" {
			// Knowledge may move between runs, but project boundaries are explicit.
			// Widened-scope knowledge is the deliberate exception: once promoted,
			// its lifecycle legitimately continues in other projects
			// (docs/knowledge-evolution.md §5).
			return nil, fmt.Errorf("knowledge %q event crosses project boundary", id)
		}
		if event.OccurredAt.Before(item.CreatedAt) {
			return nil, fmt.Errorf("knowledge %q transition precedes creation", id)
		}
		if event.Type != "knowledge.used" && !canTransitionKnowledge(item.State, event.Type) {
			return nil, fmt.Errorf("knowledge %q cannot transition from %s via %s", id, item.State, event.Type)
		}
		item.UpdatedAt = event.OccurredAt
		switch event.Type {
		case "knowledge.used":
			item.ReuseCount++
			usedAt := event.OccurredAt
			item.LastUsedAt = &usedAt
		case "knowledge.confirmed":
			item.State = "confirmed"
		case "knowledge.challenged":
			item.State = "challenged"
		case "knowledge.corrected":
			item.State = "corrected"
			item.Replacement = stringValue(event.Data, "replacement_id")
		case "knowledge.invalidated":
			item.State = "invalidated"
		case "knowledge.disproved":
			item.State = "invalidated"
		case "knowledge.superseded":
			item.State = "superseded"
			item.Replacement = stringValue(event.Data, "replacement_id")
		default:
			return nil, fmt.Errorf("unsupported knowledge lifecycle event %q", event.Type)
		}
		item.Evidence = appendUniqueEvidence(item.Evidence, event.Evidence...)
		appendKnowledgeTransition(item, event)
	}
	if len(pendingPromoted) > 0 {
		ids := make([]string, 0, len(pendingPromoted))
		for id := range pendingPromoted {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("promotion events refer to unknown knowledge %v", ids)
	}
	result := make([]Knowledge, 0, len(byID))
	for _, item := range byID {
		result = append(result, *item)
	}
	markAtRisk(result)
	sort.Slice(result, func(i, j int) bool {
		if !result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].UpdatedAt.After(result[j].UpdatedAt)
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func appendUniqueRelation(existing []KnowledgeRelation, relation KnowledgeRelation) []KnowledgeRelation {
	for _, item := range existing {
		if item.Type == relation.Type && item.TargetID == relation.TargetID {
			return existing
		}
	}
	return append(existing, relation)
}

func markAtRisk(knowledge []Knowledge) {
	byID := make(map[string]*Knowledge, len(knowledge))
	dependents := make(map[string][]string)
	for i := range knowledge {
		byID[knowledge[i].ID] = &knowledge[i]
		for _, relation := range knowledge[i].Relationships {
			if relation.Type == "depends_on" || relation.Type == "derived_from" {
				dependents[relation.TargetID] = append(dependents[relation.TargetID], knowledge[i].ID)
			}
		}
	}
	type riskPath struct{ id, root string }
	queue := make([]riskPath, 0)
	for _, item := range knowledge {
		if item.State == "invalidated" || item.State == "superseded" || item.State == "corrected" {
			queue = append(queue, riskPath{id: item.ID, root: item.ID})
		}
	}
	seen := make(map[string]bool)
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		key := path.id + "\x00" + path.root
		if seen[key] {
			continue
		}
		seen[key] = true
		for _, dependentID := range dependents[path.id] {
			dependent := byID[dependentID]
			if dependent == nil || dependent.State == "invalidated" || dependent.State == "superseded" || dependent.State == "corrected" {
				continue
			}
			dependent.AtRisk = true
			if !containsString(dependent.RiskSources, path.root) {
				dependent.RiskSources = append(dependent.RiskSources, path.root)
			}
			queue = append(queue, riskPath{id: dependentID, root: path.root})
		}
	}
	for i := range knowledge {
		sort.Strings(knowledge[i].RiskSources)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func knowledgeKind(data map[string]any) string {
	if kind := stringValue(data, "kind"); kind != "" {
		return kind
	}
	return "claim"
}

// knowledgeScopeKind normalizes the effective scope of a knowledge item:
// absent scope on proposed events means the default "project" birth scope.
func knowledgeScopeKind(data map[string]any) string {
	if kind := stringValue(data, "scope_kind"); kind != "" {
		return kind
	}
	return "project"
}

func knowledgeScopeID(data map[string]any) string {
	return stringValue(data, "scope_id")
}

func stringList(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if item, ok := value.(string); ok && item != "" {
				result = append(result, item)
			}
		}
		return result
	default:
		return nil
	}
}

func canTransitionKnowledge(state, eventType string) bool {
	switch state {
	case "proposed":
		return eventType == "knowledge.confirmed" || eventType == "knowledge.challenged" || eventType == "knowledge.corrected" || eventType == "knowledge.invalidated" || eventType == "knowledge.superseded" || eventType == "knowledge.disproved"
	case "confirmed":
		return eventType == "knowledge.confirmed" || eventType == "knowledge.challenged" || eventType == "knowledge.corrected" || eventType == "knowledge.invalidated" || eventType == "knowledge.superseded" || eventType == "knowledge.disproved"
	case "challenged":
		return eventType == "knowledge.confirmed" || eventType == "knowledge.challenged" || eventType == "knowledge.corrected" || eventType == "knowledge.invalidated" || eventType == "knowledge.superseded" || eventType == "knowledge.disproved"
	default:
		// corrected, superseded, and invalidated are terminal. Usage events
		// remain recordable so the system can reveal stale-knowledge leakage.
		return false
	}
}

func appendKnowledgeTransition(item *Knowledge, event Event) {
	rule := ""
	if event.Type == "knowledge.disproved" {
		rule = "explicit-evidence-disproof.v1"
	} else if event.Type == "knowledge.invalidated" && stringValue(event.Data, "method") == "manual" {
		rule = "manual.v1"
	} else if event.Type == "knowledge.promoted" {
		rule = "scope-promotion.v1"
	}
	item.History = append(item.History, KnowledgeTransition{EventID: event.EventID, SourceID: event.Source.ID, Type: event.Type, State: item.State, Rule: rule, At: event.OccurredAt, Actor: event.Context.Actor, Evidence: append([]Evidence(nil), event.Evidence...), Reason: stringValue(event.Data, "reason")})
}

func appendUniqueEvidence(existing []Evidence, additions ...Evidence) []Evidence {
	seen := make(map[string]bool, len(existing)+len(additions))
	for _, item := range existing {
		seen[item.Ref] = true
	}
	for _, item := range additions {
		if item.Ref == "" || seen[item.Ref] {
			continue
		}
		existing = append(existing, item)
		seen[item.Ref] = true
	}
	return existing
}

var ErrKnowledgeNotFound = errors.New("knowledge not found")
