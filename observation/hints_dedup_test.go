package observation

import (
	"testing"
	"time"
)

// hintProposalWithTopics is hintProposalEvent plus topic metadata, which the
// diversity cap groups by.
func hintProposalWithTopics(eventID, knowledgeID, kind, proposition string, entities, topics []string, at time.Time) Event {
	data := map[string]any{"knowledge_id": knowledgeID, "proposition": proposition, "kind": kind}
	if len(entities) > 0 {
		data["entities"] = entities
	}
	if len(topics) > 0 {
		data["topics"] = topics
	}
	return Event{
		Schema:     Schema,
		EventID:    eventID,
		OccurredAt: at,
		Source:     Source{ID: "kernel", Integration: "temporality-agent-kernel"},
		Context:    Context{Project: "repo-a", Run: "run-1", Actor: Actor{ID: "agent", Type: "agent"}},
		Type:       "knowledge.proposed",
		Data:       data,
	}
}

// Exact duplicates (case/whitespace reworded copies of one rule) must not
// spend the hint budget twice (§9).
func TestFindHintsDropsExactDuplicates(t *testing.T) {
	base := time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-a", "claim", "Run  gofmt   before committing the migration files", []string{"migrations"}, base),
		hintProposalEvent("e2", "k-b", "claim", "run gofmt before committing the migration files", []string{"migrations"}, base.Add(time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "run the migrations", Entities: []string{"migrations"}, Limit: 8})
	if len(hints) != 1 {
		t.Fatalf("exact duplicate must be dropped, got %+v", hints)
	}
	if hints[0].KnowledgeID != "k-a" {
		t.Fatalf("the stronger-ranked variant must survive, got %s", hints[0].KnowledgeID)
	}
}

// Near-duplicates (Jaccard over content tokens above the threshold) are
// dropped after ranking; a genuinely distinct rule survives (§9).
func TestFindHintsDropsNearDuplicates(t *testing.T) {
	base := time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC)
	events := []Event{
		// k-1 and k-2 share almost every token: one of them is a reworded copy.
		hintProposalEvent("e1", "k-1", "claim", "Integration tests require the docker daemon running before the suite starts", []string{"tests"}, base),
		hintProposalEvent("e2", "k-2", "claim", "Integration tests require the docker daemon running before the suite executes", []string{"tests"}, base.Add(time.Minute)),
		// Distinct proposition: shares the topic but not the wording.
		hintProposalEvent("e3", "k-3", "claim", "Flaky retry tests must be quarantined with a label so the suite stays green", []string{"tests"}, base.Add(2*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "run the integration tests", Entities: []string{"tests"}, Limit: 8})
	if len(hints) != 2 {
		t.Fatalf("near-duplicate must be dropped, distinct rule kept, got %+v", hints)
	}
	if hints[0].KnowledgeID != "k-1" {
		t.Fatalf("the stronger-ranked variant must survive, got %s", hints[0].KnowledgeID)
	}
	if hints[1].KnowledgeID != "k-3" {
		t.Fatalf("the distinct rule must survive, got %s", hints[1].KnowledgeID)
	}
}

// One topic cannot crowd out the rest of the budget: at most two cues share a
// primary topic, while untyped knowledge is not capped (§9).
func TestFindHintsCapsPerTopic(t *testing.T) {
	base := time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalWithTopics("e1", "k-auth-1", "claim", "The gateway issues short-lived tokens for service accounts", []string{"auth"}, []string{"auth"}, base),
		hintProposalWithTopics("e2", "k-auth-2", "claim", "Token refresh must use the rotation endpoint, never reissue", []string{"auth"}, []string{"auth"}, base.Add(time.Minute)),
		hintProposalWithTopics("e3", "k-auth-3", "claim", "Revocation propagates to the edge cache within five seconds", []string{"auth"}, []string{"auth"}, base.Add(2*time.Minute)),
		hintProposalWithTopics("e4", "k-auth-4", "claim", "Audit log entries retain the caller identity and scopes", []string{"auth"}, []string{"auth"}, base.Add(3*time.Minute)),
		// Same wording domain but no topic: nothing to group by, not capped.
		hintProposalEvent("e5", "k-other", "claim", "The report tool renders weekly changelogs from journal events", []string{"auth"}, base.Add(4*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "configure the gateway authentication", Entities: []string{"auth"}, Limit: 8})
	if len(hints) != 3 {
		t.Fatalf("topic cap must keep 2 auth cues plus the untyped one, got %d: %+v", len(hints), hints)
	}
	authCues := 0
	for _, hint := range hints {
		if hint.KnowledgeID != "k-other" {
			authCues++
		}
	}
	if authCues != 2 {
		t.Fatalf("expected exactly 2 cues from the auth topic, got %d", authCues)
	}
	if hints[0].KnowledgeID != "k-auth-1" || hints[1].KnowledgeID != "k-auth-2" {
		t.Fatalf("the strongest auth cues must survive the cap, got %s, %s", hints[0].KnowledgeID, hints[1].KnowledgeID)
	}
}

// Diversification must not depend on input order: shuffling the knowledge list
// yields the same kept set.
func TestFindHintsDiversifyDeterministicAcrossInputOrder(t *testing.T) {
	base := time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-1", "claim", "Integration tests require the docker daemon running before the suite starts", []string{"tests"}, base),
		hintProposalEvent("e2", "k-2", "claim", "Integration tests require the docker daemon running before the suite executes", []string{"tests"}, base.Add(time.Minute)),
		hintProposalEvent("e3", "k-3", "claim", "Flaky retry tests must be quarantined with a label so the suite stays green", []string{"tests"}, base.Add(2*time.Minute)),
	}
	forward := hintKnowledge(t, events)
	reversed := hintKnowledge(t, []Event{events[2], events[1], events[0]})
	a := FindHints(forward, HintQuery{Text: "run the integration tests", Entities: []string{"tests"}, Limit: 8})
	b := FindHints(reversed, HintQuery{Text: "run the integration tests", Entities: []string{"tests"}, Limit: 8})
	if len(a) != len(b) {
		t.Fatalf("input order must not change the kept set: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].KnowledgeID != b[i].KnowledgeID {
			t.Fatalf("input order changed the kept set at %d: %s vs %s", i, a[i].KnowledgeID, b[i].KnowledgeID)
		}
	}
}
