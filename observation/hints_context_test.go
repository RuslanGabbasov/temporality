package observation

import (
	"testing"
	"time"
)

// hintOutcomeEvent records one judged outcome for a hint offer. Outcomes feed
// the B4 usefulness factor (plan-priming-relevance.md §11).
func hintOutcomeEvent(eventID, hintID, knowledgeID, outcome string, at time.Time) Event {
	return Event{
		Schema:     Schema,
		EventID:    eventID,
		OccurredAt: at,
		Source:     Source{ID: "kernel", Integration: "temporality-agent-kernel"},
		Context:    Context{Project: "repo-a", Run: "run-1", Actor: Actor{ID: "agent", Type: "agent"}},
		Type:       "hint.outcome",
		Data:       map[string]any{"hint_id": hintID, "knowledge_id": knowledgeID, "outcome": outcome},
	}
}

// usefulnessFixture builds two entity-matched confirmed claims whose ids break
// the intended order alphabetically, so only the B4 factor can invert it.
func usefulnessFixture(t *testing.T, first []Event, second []Event) []Hint {
	t.Helper()
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-b", "claim", "The indexer skips vendored directories entirely by default", []string{"indexer"}, base),
		transitionEvent("e2", "k-b", "knowledge.confirmed", base.Add(time.Minute)),
		hintProposalEvent("e3", "k-a", "claim", "The indexer emits parquet shards after every crawl", []string{"indexer"}, base.Add(2*time.Minute)),
		transitionEvent("e4", "k-a", "knowledge.confirmed", base.Add(3*time.Minute)),
	}
	events = append(events, first...)
	events = append(events, second...)
	return FindHints(hintKnowledge(t, events), HintQuery{Text: "reindex with the indexer", Entities: []string{"indexer"}, Limit: 8})
}

func TestFindHintsUsefulnessPromotesProvenHelpful(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	helpful := []Event{
		hintOutcomeEvent("o1", "h1", "k-b", "helpful", base.Add(4*time.Minute)),
		hintOutcomeEvent("o2", "h2", "k-b", "helpful", base.Add(5*time.Minute)),
		hintOutcomeEvent("o3", "h3", "k-b", "helpful", base.Add(6*time.Minute)),
	}
	hints := usefulnessFixture(t, helpful, nil)
	if len(hints) != 2 {
		t.Fatalf("expected both candidates, got %d", len(hints))
	}
	if hints[0].KnowledgeID != "k-b" {
		t.Fatalf("a helpful track record must outrank the alphabetical winner, got %q first", hints[0].KnowledgeID)
	}
}

func TestFindHintsUsefulnessDemotesHarmful(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	harmful := []Event{
		hintOutcomeEvent("o1", "h1", "k-a", "harmful", base.Add(4*time.Minute)),
		hintOutcomeEvent("o2", "h2", "k-a", "harmful", base.Add(5*time.Minute)),
		hintOutcomeEvent("o3", "h3", "k-a", "harmful", base.Add(6*time.Minute)),
	}
	hints := usefulnessFixture(t, nil, harmful)
	if len(hints) != 2 {
		t.Fatalf("expected both candidates, got %d", len(hints))
	}
	if hints[0].KnowledgeID != "k-b" {
		t.Fatalf("a harmful track record must lose to the alphabetical winner, got %q first", hints[0].KnowledgeID)
	}
}

func TestFindHintsUsefulnessNeedsMinimumSamples(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	twoSamples := []Event{
		hintOutcomeEvent("o1", "h1", "k-b", "helpful", base.Add(4*time.Minute)),
		hintOutcomeEvent("o2", "h2", "k-b", "helpful", base.Add(5*time.Minute)),
	}
	hints := usefulnessFixture(t, twoSamples, nil)
	if len(hints) != 2 {
		t.Fatalf("expected both candidates, got %d", len(hints))
	}
	if hints[0].KnowledgeID != "k-a" {
		t.Fatalf("two outcomes are not a track record: the deterministic order must hold, got %q first", hints[0].KnowledgeID)
	}
}

func TestFindHintsUsefulnessTwoThirdsMajorityDominates(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	majority := []Event{
		hintOutcomeEvent("o1", "h1", "k-b", "helpful", base.Add(4*time.Minute)),
		hintOutcomeEvent("o2", "h2", "k-b", "helpful", base.Add(5*time.Minute)),
		hintOutcomeEvent("o3", "h3", "k-b", "harmful", base.Add(6*time.Minute)),
	}
	hints := usefulnessFixture(t, majority, nil)
	if len(hints) != 2 {
		t.Fatalf("expected both candidates, got %d", len(hints))
	}
	if hints[0].KnowledgeID != "k-b" {
		t.Fatalf("a 2-of-3 helpful majority must dominate, got %q first", hints[0].KnowledgeID)
	}
}

func TestFindHintsUsefulnessEvenSplitStaysNeutral(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	split := []Event{
		hintOutcomeEvent("o1", "h1", "k-b", "helpful", base.Add(4*time.Minute)),
		hintOutcomeEvent("o2", "h2", "k-b", "harmful", base.Add(5*time.Minute)),
		hintOutcomeEvent("o3", "h3", "k-b", "helpful", base.Add(6*time.Minute)),
		hintOutcomeEvent("o4", "h4", "k-b", "harmful", base.Add(7*time.Minute)),
	}
	hints := usefulnessFixture(t, split, nil)
	if len(hints) != 2 {
		t.Fatalf("expected both candidates, got %d", len(hints))
	}
	if hints[0].KnowledgeID != "k-a" {
		t.Fatalf("an even record must stay neutral, got %q first", hints[0].KnowledgeID)
	}
}

func TestFindHintsSkillDuplicateDemotedNotExcluded(t *testing.T) {
	// B5 (plan §7): a rule the agent's own skill already states loses rank to a
	// fresh one at otherwise equal strength — but is never excluded, it may
	// still be the needed clarification.
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	events := []Event{
		// k-a restates the skill digest almost token for token.
		hintProposalEvent("e1", "k-a", "claim", "The report tool requires the --out flag", []string{"reporting"}, base),
		transitionEvent("e2", "k-a", "knowledge.confirmed", base.Add(time.Minute)),
		hintProposalEvent("e3", "k-z", "claim", "Changelog generation emails subscribers every monday", []string{"reporting"}, base.Add(2*time.Minute)),
		transitionEvent("e4", "k-z", "knowledge.confirmed", base.Add(3*time.Minute)),
	}
	knowledge := hintKnowledge(t, events)
	query := HintQuery{
		Text: "publish the changelog", Entities: []string{"reporting"}, Limit: 8,
		ActiveSkills: []HintSkill{{ID: "skill-release", Summary: "Release checklist: the report tool requires the --out flag for changelog runs"}},
	}
	hints := FindHints(knowledge, query)
	if len(hints) != 2 {
		t.Fatalf("a skill duplicate must stay admitted, got %d hints", len(hints))
	}
	if hints[0].KnowledgeID != "k-z" {
		t.Fatalf("the novel rule must outrank the skill duplicate, got %q first", hints[0].KnowledgeID)
	}
	// Without the skill context the deterministic order returns.
	plain := FindHints(knowledge, HintQuery{Text: "publish the changelog", Entities: []string{"reporting"}, Limit: 8})
	if plain[0].KnowledgeID != "k-a" {
		t.Fatalf("without skill context the alphabetical order must hold, got %q first", plain[0].KnowledgeID)
	}
}

func TestFindHintsSkillDuplicateKeepsDistinctiveRules(t *testing.T) {
	// Only mostly-covered content counts as duplication: a rule that merely
	// shares a couple of tokens with a skill stays fully ranked.
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-a", "claim", "The report tool requires a postgres connection string with sslmode", []string{"reporting"}, base),
		transitionEvent("e2", "k-a", "knowledge.confirmed", base.Add(time.Minute)),
		hintProposalEvent("e3", "k-z", "claim", "Changelog generation emails subscribers every monday", []string{"reporting"}, base.Add(2*time.Minute)),
		transitionEvent("e4", "k-z", "knowledge.confirmed", base.Add(3*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{
		Text: "publish the changelog", Entities: []string{"reporting"}, Limit: 8,
		ActiveSkills: []HintSkill{{ID: "skill-release", Summary: "Release checklist: the report tool requires the --out flag"}},
	})
	if len(hints) != 2 || hints[0].KnowledgeID != "k-a" {
		t.Fatalf("a partially overlapping rule must keep its rank, got %+v", hints)
	}
}

func TestFindHintsSkipsKnowledgeIssuedEarlierInRun(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-a", "claim", "The indexer skips vendored directories entirely by default", []string{"indexer"}, base),
		hintProposalEvent("e2", "k-b", "claim", "The indexer emits parquet shards after every crawl", []string{"indexer"}, base.Add(time.Minute)),
	}
	knowledge := hintKnowledge(t, events)
	hints := FindHints(knowledge, HintQuery{Text: "reindex with the indexer", Entities: []string{"indexer"}, Limit: 8, IssuedKnowledgeIDs: []string{"k-a"}})
	if len(hints) != 1 || hints[0].KnowledgeID != "k-b" {
		t.Fatalf("already issued knowledge must not be re-offered, got %+v", hints)
	}
}

func TestFindHintsIssuedReplacementNotReOffered(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-old", "claim", "The gatekeeper CLI accepts basic auth for all endpoints", []string{"gatekeeper"}, base),
		{
			Schema: Schema, EventID: "e2", OccurredAt: base.Add(time.Minute),
			Source:  Source{ID: "kernel", Integration: "temporality-agent-kernel"},
			Context: Context{Project: "repo-a", Actor: Actor{ID: "agent", Type: "agent"}},
			Type:    "knowledge.superseded",
			Data:    map[string]any{"knowledge_id": "k-old", "replacement_id": "k-new"},
		},
		hintProposalEvent("e3", "k-new", "claim", "An oauth bearer token must accompany every CLI call", []string{"gatekeeper"}, base.Add(2*time.Minute)),
		transitionEvent("e4", "k-new", "knowledge.confirmed", base.Add(3*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "authenticate to gatekeeper", Entities: []string{"gatekeeper"}, Limit: 8, IssuedKnowledgeIDs: []string{"k-new"}})
	if len(hints) != 0 {
		t.Fatalf("an already issued replacement must not surface again, got %+v", hints)
	}
}

func TestFindHintsContextRankingDeterministic(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-a", "claim", "The report tool requires the --out flag", []string{"reporting"}, base),
		transitionEvent("e2", "k-a", "knowledge.confirmed", base.Add(time.Minute)),
		hintProposalEvent("e3", "k-z", "claim", "Changelog generation emails subscribers every monday", []string{"reporting"}, base.Add(2*time.Minute)),
		transitionEvent("e4", "k-z", "knowledge.confirmed", base.Add(3*time.Minute)),
		hintOutcomeEvent("o1", "h1", "k-z", "helpful", base.Add(4*time.Minute)),
		hintOutcomeEvent("o2", "h2", "k-z", "helpful", base.Add(5*time.Minute)),
		hintOutcomeEvent("o3", "h3", "k-z", "helpful", base.Add(6*time.Minute)),
	}
	knowledge := hintKnowledge(t, events)
	query := HintQuery{
		Text: "publish the changelog", Entities: []string{"reporting"}, Limit: 8,
		ActiveSkills: []HintSkill{{ID: "skill-release", Summary: "Release checklist: the report tool requires the --out flag"}},
	}
	first := FindHints(knowledge, query)
	for i := 0; i < 5; i++ {
		again := FindHints(knowledge, query)
		if len(again) != len(first) {
			t.Fatalf("run %d: hint count changed: %d vs %d", i, len(again), len(first))
		}
		for j := range again {
			if again[j].KnowledgeID != first[j].KnowledgeID {
				t.Fatalf("run %d: order changed at %d: %q vs %q", i, j, again[j].KnowledgeID, first[j].KnowledgeID)
			}
		}
	}
}
