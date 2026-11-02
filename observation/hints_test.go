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

func hintVerdictEvent(eventID, knowledgeID, outcome, reason string, at time.Time) Event {
	return Event{
		Schema:     Schema,
		EventID:    eventID,
		OccurredAt: at,
		Source:     Source{ID: "kernel", Integration: "temporality-agent-kernel"},
		Context:    Context{Project: "repo-a", Run: "run-1", Actor: Actor{ID: "agent", Type: "agent"}},
		Type:       "hint.outcome",
		Data:       map[string]any{"hint_id": "h-" + knowledgeID, "knowledge_id": knowledgeID, "outcome": outcome, "reason": reason},
	}
}

// Run verdicts drive the knowledge lifecycle (docs/knowledge-evolution.md):
// a judged-helpful hint confirms a proposal, one harmful verdict challenges
// it with the run's reason recorded in history, two unsupported harmful
// verdicts invalidate it — and a later re-proposal creates a fresh node, no
// tombstones.
func TestHintOutcomesDriveLifecycle(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-helpful", "claim", "Редкое правило про penpot-сервер", nil, base),
		hintVerdictEvent("e2", "k-helpful", "helpful", "повторилось на практике", base.Add(time.Hour)),

		hintProposalEvent("e3", "k-challenged", "claim", "Правило про botaccount-справочник", nil, base),
		hintVerdictEvent("e4", "k-challenged", "harmful", "не сработало: rm теперь блокируется", base.Add(2*time.Hour)),

		hintProposalEvent("e5", "k-dead", "claim", "Правило про ветку rnd/ai", nil, base),
		hintVerdictEvent("e6", "k-dead", "harmful", "опровергнуто первым раном", base.Add(time.Hour)),
		hintVerdictEvent("e7", "k-dead", "harmful", "опровергнуто вторым раном", base.Add(2*time.Hour)),

		hintProposalEvent("e8", "k-mixed", "claim", "Правило про дедупликацию отчётов", nil, base),
		hintVerdictEvent("e9", "k-mixed", "harmful", "в одном ране навредило", base.Add(time.Hour)),
		hintVerdictEvent("e10", "k-mixed", "helpful", "в другом помогло", base.Add(2*time.Hour)),
		hintVerdictEvent("e11", "k-mixed", "harmful", "и снова навредило", base.Add(3*time.Hour)),
	}
	byID := make(map[string]Knowledge)
	for _, item := range hintKnowledge(t, events) {
		byID[item.ID] = item
	}

	if byID["k-helpful"].State != "confirmed" {
		t.Errorf("helpful verdict must confirm a proposal, got %q", byID["k-helpful"].State)
	}
	challenged := byID["k-challenged"]
	if challenged.State != "challenged" {
		t.Errorf("one harmful verdict must challenge, got %q", challenged.State)
	}
	if n := len(challenged.History); n == 0 || challenged.History[n-1].Reason != "не сработало: rm теперь блокируется" || challenged.History[n-1].State != "challenged" {
		t.Errorf("the refutation reason must be recorded in history, got %+v", challenged.History)
	}
	if byID["k-dead"].State != "invalidated" {
		t.Errorf("two unsupported harmful verdicts must invalidate, got %q", byID["k-dead"].State)
	}
	if byID["k-mixed"].State != "challenged" {
		t.Errorf("conflicting verdicts must stay challenged for review, got %q", byID["k-mixed"].State)
	}
}

// The re-verification clock: a candidate nothing confirmed for two weeks is
// demoted below an equally-matched fresh one and carries a caution — time
// raises doubt, only evidence kills.
func TestFindHintsStaleCandidatesDemotedWithCaution(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	events := []Event{
		hintProposalEvent("e1", "k-old", "claim", "Старое правило про квоту номер один", []string{"quota-bucket"}, base),
		hintProposalEvent("e2", "k-new", "claim", "Свежее правило про квоту номер два", []string{"quota-bucket"}, base.Add(27*24*time.Hour)),
	}
	knowledge := hintKnowledge(t, events)
	hints := FindHints(knowledge, HintQuery{Entities: []string{"quota-bucket"}, Limit: 8, Now: base.Add(30 * 24 * time.Hour)})
	if len(hints) != 2 {
		t.Fatalf("expected both candidates, got %+v", hints)
	}
	if hints[0].KnowledgeID != "k-new" || hints[1].KnowledgeID != "k-old" {
		t.Fatalf("the stale candidate must rank below the fresh equal match, got %q, %q", hints[0].KnowledgeID, hints[1].KnowledgeID)
	}
	if !strings.Contains(hints[1].Caution, "not re-verified") {
		t.Errorf("the stale candidate must carry a re-verification caution, got %q", hints[1].Caution)
	}
	if strings.Contains(hints[0].Caution, "not re-verified") {
		t.Errorf("the fresh candidate must stay caution-free, got %q", hints[0].Caution)
	}
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

// Corpus-common tokens are glue, not signal: a word living in ≥5 items and
// ≥5% of the corpus («файл», «не», an org name) matched everything and made
// the same hints surface for every query. Only discriminative query tokens
// may gate a term match.
func TestFindHintsIgnoresCorpusCommonTokens(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	var events []Event
	for i := 0; i < 6; i++ {
		events = append(events, hintProposalEvent(
			"e"+string(rune('0'+i)), "k-common-"+string(rune('0'+i)), "claim",
			"Правило про выгрузку файла номер "+string(rune('0'+i)), nil, base.Add(time.Duration(i)*time.Minute)))
	}
	knowledge := hintKnowledge(t, events)

	// Glue only: every token is corpus-common → nothing discriminates.
	if hints := FindHints(knowledge, HintQuery{Text: "файла выгрузку", Limit: 8}); len(hints) != 0 {
		t.Fatalf("glue-only query must yield no hints, got %+v", hints)
	}

	// Glue + one rare token: only the rare token may decide, and it must.
	events = append(events, hintProposalEvent("e9", "k-rare", "claim", "Настройка penpot-сервера поднимает сессии редактирования", nil, base.Add(9*time.Minute)))
	knowledge = hintKnowledge(t, events)
	hints := FindHints(knowledge, HintQuery{Text: "файла penpot-сервера выгрузку", Limit: 8})
	if len(hints) != 1 || hints[0].KnowledgeID != "k-rare" {
		t.Fatalf("the rare token must decide the match, got %+v", hints)
	}
	for _, mark := range hints[0].MatchedBy {
		if mark == "term:файла" || mark == "term:выгрузку" {
			t.Errorf("glue tokens must not appear as match reasons: %v", hints[0].MatchedBy)
		}
	}
}

// The semantic tier keeps reserved seats in the budget: a flood of lexical
// candidates must not crowd meaning-level matches out of the hint block
// entirely. The flood is entity-matched here so the test exercises the quota,
// not token statistics.
func TestFindHintsSemanticQuotaSurvivesLexicalFlood(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	var events []Event
	for i := 0; i < 6; i++ {
		events = append(events, hintProposalEvent(
			"e"+string(rune('0'+i)), "k-lex-"+string(rune('0'+i)), "claim",
			"Правило квоты номер "+string(rune('0'+i)), []string{"quota-bucket"}, base.Add(time.Duration(i)*time.Minute)))
	}
	// Semantically on-topic wording the lexical tiers cannot see.
	events = append(events, hintProposalEvent("e9", "k-sem", "claim", "Смысловое правило про документооборот", nil, base.Add(9*time.Minute)))
	knowledge := hintKnowledge(t, events)

	query := HintQuery{Text: "квота документооборот", Entities: []string{"quota-bucket"}, Limit: 2,
		Embedding: []float32{1, 0, 0},
		KnowledgeEmbeddings: map[string][]float32{
			"k-sem": {0.95, 0.1, 0},
		}}
	hints := FindHints(knowledge, query)
	if len(hints) != 2 {
		t.Fatalf("budget must be filled, got %d hints", len(hints))
	}
	foundSemantic := false
	for _, hint := range hints {
		if hint.KnowledgeID == "k-sem" {
			foundSemantic = true
		}
	}
	if !foundSemantic {
		t.Fatalf("the semantic candidate must keep its reserved seat, got %+v", hints)
	}
}
