package observation

import (
	"testing"
	"time"
)

func hintProposalEvent(eventID, knowledgeID, kind, proposition string, entities []string, at time.Time) Event {
	data := map[string]any{"knowledge_id": knowledgeID, "proposition": proposition, "kind": kind}
	if len(entities) > 0 {
		data["entities"] = entities
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

func hintKnowledge(t *testing.T, events []Event) []Knowledge {
	t.Helper()
	knowledge, err := ProjectKnowledge(events)
	if err != nil {
		t.Fatalf("project knowledge: %v", err)
	}
	return knowledge
}

func TestFindHintsGatesInvalidatedEvenOnStrongMatch(t *testing.T) {
	// Ported from kernel/priming integration corpus: a lifecycle-gated item
	// must never surface as an acting rule, however well it matches.
	base := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-old", "claim", "The gatekeeper CLI accepts basic auth for all endpoints", []string{"gatekeeper"}, base),
		transitionEvent("e2", "k-old", "knowledge.invalidated", base.Add(time.Minute)),
		hintProposalEvent("e3", "k-new", "claim", "The gatekeeper CLI requires an oauth token", []string{"gatekeeper"}, base.Add(2*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "authenticate to gatekeeper", Entities: []string{"gatekeeper"}, Limit: 8})
	if len(hints) != 1 || hints[0].KnowledgeID != "k-new" {
		t.Fatalf("invalidated knowledge must be gated, got %+v", hints)
	}
}

func TestFindHintsGatesSupersededAndCorrected(t *testing.T) {
	base := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	for _, terminal := range []string{"knowledge.superseded", "knowledge.corrected"} {
		events := []Event{
			hintProposalEvent("e1", "k-1", "claim", "Run migrations with the old shell script", []string{"migrations"}, base),
			transitionEvent("e2", "k-1", terminal, base.Add(time.Minute)),
		}
		hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "run the migrations", Entities: []string{"migrations"}, Limit: 8})
		if len(hints) != 0 {
			t.Fatalf("%s knowledge must be gated, got %+v", terminal, hints)
		}
	}
}

func TestFindHintsIrrelevantTaskYieldsZeroHints(t *testing.T) {
	// Ported from kernel/priming integration corpus: an honest zero beats a
	// stretch — unrelated experience must not leak into the prompt.
	base := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-1", "claim", "The gatekeeper CLI requires the --out flag to write the report", nil, base),
		hintProposalEvent("e2", "k-2", "claim", "Integration tests need Docker running before the suite starts", nil, base.Add(time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "rename the marketing landing page headline", Limit: 8})
	if len(hints) != 0 {
		t.Fatalf("irrelevant task must yield zero hints, got %+v", hints)
	}
}

func TestFindHintsEmptyQueryYieldsZeroHints(t *testing.T) {
	base := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-1", "claim", "The gatekeeper CLI requires the --out flag to write the report", nil, base),
	}
	if hints := FindHints(hintKnowledge(t, events), HintQuery{Limit: 8}); len(hints) != 0 {
		t.Fatalf("empty query must yield zero hints, got %+v", hints)
	}
}

func TestFindHintsPrefersClaimsOverObservationsAtEqualMatch(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "auto/obs1", "observation", "Report generation command exited 0 with the required flag", nil, base),
		transitionEvent("e2", "auto/obs1", "knowledge.confirmed", base.Add(time.Minute)),
		hintProposalEvent("e3", "k-claim1", "claim", "The gatekeeper CLI requires the --out flag to write the report", nil, base.Add(2*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "write the report with the required flag", Limit: 8})
	if len(hints) != 2 {
		t.Fatalf("expected claim and observation, got %d hints", len(hints))
	}
	if hints[0].KnowledgeID != "k-claim1" {
		t.Fatalf("a proposed claim must outrank a confirmed observation at equal match strength, got %q first", hints[0].KnowledgeID)
	}
}

func TestFindHintsOffersObservationsWhenBudgetAllows(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-claim1", "claim", "The gatekeeper CLI requires the --out flag to write the report", nil, base),
		hintProposalEvent("e2", "auto/obs1", "observation", "Report generation command exited 0 with the required flag", nil, base.Add(time.Minute)),
		hintProposalEvent("e3", "auto/obs2", "observation", "Report lint command exited 0 with the required flag", nil, base.Add(2*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "write the report with the required flag", Limit: 8})
	if len(hints) != 3 {
		t.Fatalf("observations must fill the remaining budget, got %d hints", len(hints))
	}
	if hints[0].KnowledgeID != "k-claim1" {
		t.Fatalf("claim must rank first, got %q", hints[0].KnowledgeID)
	}
}

func TestFindHintsBudgetSqueezeKeepsClaims(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "auto/obs1", "observation", "Report generation command exited 0 with the required flag", nil, base),
		transitionEvent("e2", "auto/obs1", "knowledge.confirmed", base.Add(time.Minute)),
		hintProposalEvent("e3", "auto/obs2", "observation", "Report lint command exited 0 with the required flag", nil, base.Add(2*time.Minute)),
		transitionEvent("e4", "auto/obs2", "knowledge.confirmed", base.Add(3*time.Minute)),
		hintProposalEvent("e5", "k-claim1", "claim", "The gatekeeper CLI requires the --out flag to write the report", nil, base.Add(4*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "write the report with the required flag", Limit: 2})
	if len(hints) != 2 {
		t.Fatalf("expected the budget filled, got %d hints", len(hints))
	}
	if hints[0].KnowledgeID != "k-claim1" {
		t.Fatalf("confirmed observations must not crowd the claim out of the budget, got %q first", hints[0].KnowledgeID)
	}
	if hints[1].KnowledgeID != "auto/obs1" && hints[1].KnowledgeID != "auto/obs2" {
		t.Fatalf("remaining budget must go to an observation, got %q", hints[1].KnowledgeID)
	}
}

func TestFindHintsMatchTierStillDominatesKind(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "auto/obs1", "observation", "Verification command exited 0 for the gatekeeper CLI", []string{"gatekeeper"}, base),
		hintProposalEvent("e2", "k-claim1", "claim", "The gatekeeper CLI requires the --out flag to write the report", nil, base.Add(time.Minute)),
		transitionEvent("e3", "k-claim1", "knowledge.confirmed", base.Add(2*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "write the report with the required flag", Entities: []string{"gatekeeper"}, Limit: 8})
	if len(hints) != 2 {
		t.Fatalf("expected both candidates, got %d hints", len(hints))
	}
	if hints[0].KnowledgeID != "auto/obs1" {
		t.Fatalf("an entity-matched observation must still outrank a term-matched claim, got %q first", hints[0].KnowledgeID)
	}
}
