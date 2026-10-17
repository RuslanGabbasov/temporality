package observation

import (
	"strings"
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

func TestFindHintsOffersReplacementForRetiredMatch(t *testing.T) {
	// The replacement contract: a retired rule that matched the task must not
	// surface itself, but its named successor is offered in its place with an
	// explainable "replaces:" reason (plan-priming-relevance.md §10).
	base := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-old", "claim", "The gatekeeper CLI accepts basic auth for all endpoints", []string{"gatekeeper"}, base),
		{
			Schema: Schema, EventID: "e2", OccurredAt: base.Add(time.Minute),
			Source:  Source{ID: "kernel", Integration: "temporality-agent-kernel"},
			Context: Context{Project: "repo-a", Actor: Actor{ID: "agent", Type: "agent"}},
			Type:    "knowledge.superseded",
			Data:    map[string]any{"knowledge_id": "k-old", "replacement_id": "k-new"},
		},
		hintProposalEvent("e3", "k-new", "claim", "An oauth bearer token must accompany every CLI call", nil, base.Add(2*time.Minute)),
		transitionEvent("e4", "k-new", "knowledge.confirmed", base.Add(3*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "authenticate to gatekeeper", Entities: []string{"gatekeeper"}, Limit: 8})
	if len(hints) != 1 || hints[0].KnowledgeID != "k-new" {
		t.Fatalf("the replacement must be offered in place of the retired rule, got %+v", hints)
	}
	if len(hints[0].MatchedBy) != 1 || hints[0].MatchedBy[0] != "replaces:k-old" {
		t.Fatalf("replacement must carry the replaces reason, got %v", hints[0].MatchedBy)
	}
}

func TestFindHintsReplacementDoesNotDuplicateDirectMatch(t *testing.T) {
	base := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-old", "claim", "The gatekeeper CLI accepts basic auth", []string{"gatekeeper"}, base),
		{
			Schema: Schema, EventID: "e2", OccurredAt: base.Add(time.Minute),
			Source:  Source{ID: "kernel", Integration: "temporality-agent-kernel"},
			Context: Context{Project: "repo-a", Actor: Actor{ID: "agent", Type: "agent"}},
			Type:    "knowledge.superseded",
			Data:    map[string]any{"knowledge_id": "k-old", "replacement_id": "k-new"},
		},
		hintProposalEvent("e3", "k-new", "claim", "An oauth bearer token must accompany every gatekeeper CLI call", []string{"gatekeeper"}, base.Add(2*time.Minute)),
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "authenticate to gatekeeper", Entities: []string{"gatekeeper"}, Limit: 8})
	if len(hints) != 1 || hints[0].KnowledgeID != "k-new" {
		t.Fatalf("a direct match must win without duplicating itself, got %+v", hints)
	}
	if len(hints[0].MatchedBy) == 0 || hints[0].MatchedBy[0] != "entity:gatekeeper" {
		t.Fatalf("direct entity match must outrank the replacement expansion, got %v", hints[0].MatchedBy)
	}
}

func TestFindHintsRetiredReplacementStaysGated(t *testing.T) {
	base := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-old", "claim", "The gatekeeper CLI accepts basic auth", []string{"gatekeeper"}, base),
		hintProposalEvent("e2", "k-mid", "claim", "Use the legacy token flow", []string{"gatekeeper"}, base.Add(time.Minute)),
		transitionEvent("e3", "k-mid", "knowledge.invalidated", base.Add(2*time.Minute)),
		{
			Schema: Schema, EventID: "e4", OccurredAt: base.Add(3 * time.Minute),
			Source:  Source{ID: "kernel", Integration: "temporality-agent-kernel"},
			Context: Context{Project: "repo-a", Actor: Actor{ID: "agent", Type: "agent"}},
			Type:    "knowledge.superseded",
			Data:    map[string]any{"knowledge_id": "k-old", "replacement_id": "k-mid"},
		},
	}
	hints := FindHints(hintKnowledge(t, events), HintQuery{Text: "authenticate to gatekeeper", Entities: []string{"gatekeeper"}, Limit: 8})
	if len(hints) != 0 {
		t.Fatalf("a retired replacement must not resurrect, got %+v", hints)
	}
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

func TestFindHintsTeamRunKnowledgeAdmittedWithoutLexicalMatch(t *testing.T) {
	// Team-run scoping (docs/agent-teams.md §8): knowledge confirmed inside
	// the team's own run tree is visible to every slot even when nothing in
	// the slot's task matches it lexically — later slots inherit what earlier
	// slots learned mid-run.
	base := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-team", "claim", "Integration tests in this repo need Docker running first", nil, base),
	}
	knowledge := hintKnowledge(t, events)
	hints := FindHints(knowledge, HintQuery{Text: "rename the marketing landing page headline", TeamRunKnowledgeIDs: []string{"k-team"}, Limit: 8})
	if len(hints) != 1 || hints[0].KnowledgeID != "k-team" {
		t.Fatalf("team-run knowledge must be admitted without a lexical match, got %+v", hints)
	}
	if len(hints[0].MatchedBy) != 1 || hints[0].MatchedBy[0] != "team-run" {
		t.Fatalf("match reason must explain the team-run admission, got %v", hints[0].MatchedBy)
	}
	// Without the team-run handle the same query yields an honest zero.
	hints = FindHints(knowledge, HintQuery{Text: "rename the marketing landing page headline", Limit: 8})
	if len(hints) != 0 {
		t.Fatalf("irrelevant task without team scope must yield zero hints, got %+v", hints)
	}
	// Lifecycle gates still apply: a retired team-run item never surfaces.
	retired := append(events, transitionEvent("e2", "k-team", "knowledge.invalidated", base.Add(time.Minute)))
	hints = FindHints(hintKnowledge(t, retired), HintQuery{Text: "rename the marketing landing page headline", TeamRunKnowledgeIDs: []string{"k-team"}, Limit: 8})
	if len(hints) != 0 {
		t.Fatalf("invalidated team-run knowledge must stay gated, got %+v", hints)
	}
	// Entity matches keep outranking the team-run tier.
	both := append(events, hintProposalEvent("e3", "k-entity", "claim", "The gatekeeper CLI requires an oauth token", []string{"gatekeeper"}, base.Add(2*time.Minute)))
	hints = FindHints(hintKnowledge(t, both), HintQuery{Text: "authenticate to gatekeeper", Entities: []string{"gatekeeper"}, TeamRunKnowledgeIDs: []string{"k-team"}, Limit: 8})
	if len(hints) != 2 || hints[0].KnowledgeID != "k-entity" {
		t.Fatalf("entity match must outrank team-run admission, got %+v", hints)
	}
}

// The semantic tier admits knowledge the lexical tiers miss: no shared
// tokens/topics/entities, but a close vector. Caps, skip-issued, and lexical
// precedence are all enforced.
func TestFindHintsSemanticTier(t *testing.T) {
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	item := func(id, proposition string) Knowledge {
		return Knowledge{ID: id, Proposition: proposition, State: "confirmed", Topics: []string{"непересекающееся"}, Project: "demo", CreatedAt: base, UpdatedAt: base}
	}
	knowledge := []Knowledge{
		item("K-lex", "миграции базы данных запускаются через make migrate"),
		item("K-sem", "playwright пишет e2e тесты интерфейса"),
		item("K-issued", "ретраи провайдера настраиваются таймаутами"),
	}
	query := HintQuery{
		Text:  "как настроить автотесты пользовательского интерфейса",
		Limit: 8,
		// Query vector closest to K-sem; K-issued must stay suppressed.
		Embedding: []float32{1, 0, 0},
		KnowledgeEmbeddings: map[string][]float32{
			"K-sem":    {0.95, 0.1, 0},
			"K-lex":    {0, 1, 0},
			"K-issued": {0.99, 0, 0},
		},
		IssuedKnowledgeIDs: []string{"K-issued"},
	}
	hints := FindHints(knowledge, query)
	foundSemantic := false
	for _, hint := range hints {
		for _, mark := range hint.MatchedBy {
			if strings.HasPrefix(mark, "semantic:") {
				if hint.KnowledgeID == "K-sem" {
					foundSemantic = true
				} else {
					t.Errorf("unexpected semantic hit: %s (%v)", hint.KnowledgeID, hint.MatchedBy)
				}
			}
		}
	}
	if !foundSemantic {
		t.Fatalf("K-sem must be admitted by the semantic tier, hints = %+v", hints)
	}
	// Lexical match still outranks: with both present, the lexical one ranks first.
	queryBoth := query
	queryBoth.KnowledgeEmbeddings = map[string][]float32{"K-sem": {0.95, 0.1, 0}}
	queryBoth.IssuedKnowledgeIDs = nil
	queryBoth.Text = "как устроены миграции базы данных и автотесты интерфейса"
	hints = FindHints(knowledge, queryBoth)
	if len(hints) < 2 {
		t.Fatalf("expected at least two hints, got %+v", hints)
	}
	for _, hint := range hints {
		if hint.KnowledgeID != "K-lex" {
			continue
		}
		if hints[0].KnowledgeID != "K-lex" {
			t.Errorf("lexical match must outrank semantic, order = %+v", hints)
		}
	}
	// Below the cosine floor nothing semantic is admitted.
	weak := query
	weak.KnowledgeEmbeddings = map[string][]float32{"K-sem": {0, 1, 0}} // orthogonal
	if hints := FindHints(knowledge, weak); len(hints) != 0 {
		t.Errorf("orthogonal vectors must not admit semantic hints: %+v", hints)
	}
}
