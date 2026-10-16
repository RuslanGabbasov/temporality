package observation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The exploration engine is specified by docs/agent-knowledge-exploration.md:
// six read-only operations over the visible base, deterministic ranking with
// id-stable tie-breakers, bounded depth/items, and a strict split between
// explicit facts and heuristic candidates.

func exploreFixture() []Knowledge {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	item := func(id, proposition, state string, topics, entities []string) Knowledge {
		return Knowledge{ID: id, Proposition: proposition, State: state, Topics: topics, Entities: entities, Project: "demo", CreatedAt: base, UpdatedAt: base}
	}
	fixture := []Knowledge{
		item("K-migration", "При изменении схемы БД необходимо создавать миграцию", "confirmed", []string{"база данных", "миграции"}, []string{"postgres"}),
		item("K-migration-v2", "Миграции запускаются через make migrate, а не вручную", "confirmed", []string{"миграции"}, []string{"make"}),
		item("K-api-client", "При изменении API контракта нужно перегенерировать клиент", "confirmed", []string{"api"}, []string{"openapi-generator"}),
		item("K-docker-tests", "Для интеграционных тестов нужен Docker daemon", "challenged", []string{"тестирование"}, []string{"docker"}),
		item("K-old-deploy", "Деплой выполняется библиотекой X", "superseded", []string{"деплой"}, nil),
		item("K-new-deploy", "Деплой выполняется через артефакты сборки", "confirmed", []string{"деплой"}, nil),
		item("K-jwt", "Аутентификация сервиса построена на JWT токенах v2", "confirmed", []string{"безопасность"}, []string{"jwt"}),
		item("K-go-lint", "go vet обязателен перед коммитом", "confirmed", []string{"качество"}, []string{"go"}),
		item("K-cache", "Кэш ответов включен по умолчанию", "proposed", []string{"производительность"}, nil),
		item("K-noise", "Отчет о спринте собирается по пятницам", "confirmed", []string{"отчеты"}, nil),
	}
	// Explicit relations: migration v2 depends on migration rule; old deploy is
	// replaced by the new one; two contradicted statements about caching.
	fixture[1].Relationships = []KnowledgeRelation{{Type: "depends_on", TargetID: "K-migration", EventID: "evt-link-1"}}
	fixture[4].Replacement = "K-new-deploy"
	fixture[8].Relationships = []KnowledgeRelation{{Type: "contradicts", TargetID: "K-cache-off", EventID: "evt-link-2"}}
	return fixture
}

func TestExploreSearchMatchesRussianWordFormsAndIdentifiers(t *testing.T) {
	knowledge := exploreFixture()

	result := ExploreKnowledge(knowledge, ExploreQuery{Operation: ExploreSearch, Question: "как запускать миграции базы данных"})
	if result.Operation != ExploreSearch || result.AlgorithmVersion != ExploreAlgorithmVersion {
		t.Fatalf("envelope = %s / %s", result.Operation, result.AlgorithmVersion)
	}
	if len(result.Results) == 0 {
		t.Fatal("expected matches for миграции question")
	}
	if result.Results[0].KnowledgeID != "K-migration" && result.Results[0].KnowledgeID != "K-migration-v2" {
		t.Fatalf("top match = %s, want a migration rule", result.Results[0].KnowledgeID)
	}
	// «миграции» in the question must bind to «миграцию»/«миграциями» in
	// propositions through stemming, and «базы данных» to topic «база данных».
	var matchedTopic bool
	for _, card := range result.Results {
		for _, entry := range card.MatchedBy {
			if strings.HasPrefix(entry, "topic:") || strings.HasPrefix(entry, "term:") {
				matchedTopic = true
			}
		}
	}
	if !matchedTopic {
		t.Fatalf("no matched_by explanations in %v", result.Results)
	}
}

func TestExploreSearchShortTechnicalTokensAndIdHandle(t *testing.T) {
	knowledge := exploreFixture()

	result := ExploreKnowledge(knowledge, ExploreQuery{Operation: ExploreSearch, Question: "JWT v2"})
	if len(result.Results) == 0 || result.Results[0].KnowledgeID != "K-jwt" {
		t.Fatalf("jwt v2 search = %v", result.Results)
	}
	for _, entry := range result.Results[0].MatchedBy {
		if entry == "entity:jwt" {
			return
		}
	}
	t.Fatalf("jwt match not explained by entity: %v", result.Results[0].MatchedBy)
}

func TestExploreSearchByIdAndCamelIdentifiers(t *testing.T) {
	knowledge := exploreFixture()
	knowledge = append(knowledge, Knowledge{ID: "K-CamelCase", Proposition: "unused", State: "confirmed", Topics: []string{"buildRunner"}})

	for _, question := range []string{"K-migration", "camel case", "build runner"} {
		result := ExploreKnowledge(knowledge, ExploreQuery{Operation: ExploreSearch, Question: question})
		if len(result.Results) == 0 {
			t.Fatalf("question %q returned nothing", question)
		}
		if result.Results[0].KnowledgeID != "K-migration" && result.Results[0].KnowledgeID != "K-CamelCase" {
			t.Fatalf("question %q: unexpected top %s", question, result.Results[0].KnowledgeID)
		}
	}
}

func TestExploreSearchDeterministicOrdering(t *testing.T) {
	knowledge := exploreFixture()
	first := ExploreKnowledge(knowledge, ExploreQuery{Operation: ExploreSearch, Question: "миграции и деплой", MaxItems: 5})
	second := ExploreKnowledge(knowledge, ExploreQuery{Operation: ExploreSearch, Question: "миграции и деплой", MaxItems: 5})
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	if string(left) != string(right) {
		t.Fatalf("search is not deterministic:\n%s\n%s", left, right)
	}
}

func TestExploreSearchReportsGapInsteadOfInventing(t *testing.T) {
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreSearch, Question: "квантовые вычисления в маркшейдерских работах"})
	if len(result.Results) != 0 {
		t.Fatalf("expected no matches, got %v", result.Results)
	}
	if result.Completeness != "complete" {
		t.Fatalf("empty result must still be complete, got %q", result.Completeness)
	}
	if len(result.Gaps) == 0 {
		t.Fatal("a gap must be reported when nothing matches")
	}
}

func TestExploreOrientBuildsAreaMapRankedByQuestion(t *testing.T) {
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreOrient, Question: "как устроены миграции базы данных"})
	if len(result.Areas) == 0 {
		t.Fatal("expected areas")
	}
	// Both question-relevant areas must rank high; their relative order follows
	// idf weights and may legitimately differ.
	topNames := make(map[string]bool, 3)
	for _, area := range result.Areas[:3] {
		if area.Kind != "topic" {
			t.Fatalf("question-ranked areas must lead the map, got %+v", result.Areas[0])
		}
		topNames[area.Name] = true
	}
	if !topNames["миграции"] || !topNames["база данных"] {
		t.Fatalf("migration/db topics must lead the map: %v", topNames)
	}
	if result.Areas[0].KnowledgeCount < 1 || result.Areas[0].States == nil {
		t.Fatalf("area aggregates missing: %+v", result.Areas[0])
	}
	if len(result.Areas[0].Representative) == 0 {
		t.Fatal("representative items missing")
	}
	if len(result.Areas) > exploreOrientDefaultAreas {
		t.Fatalf("orient returned %d areas, cap is %d", len(result.Areas), exploreOrientDefaultAreas)
	}
	// Challenged items surface as area contradictions.
	var foundContradiction bool
	for _, area := range result.Areas {
		if area.Contradictions > 0 {
			foundContradiction = true
		}
	}
	if !foundContradiction {
		t.Fatal("challenged knowledge must raise the area contradiction counter")
	}
}

func TestExploreExpandRespectsDepthAndBreaksCycles(t *testing.T) {
	knowledge := exploreFixture()
	// Cycle: K-api-client → K-migration → K-api-client.
	for i := range knowledge {
		if knowledge[i].ID == "K-api-client" {
			knowledge[i].Relationships = append(knowledge[i].Relationships, KnowledgeRelation{Type: "related_to", TargetID: "K-migration", EventID: "evt-cycle"})
		}
		if knowledge[i].ID == "K-migration" {
			knowledge[i].Relationships = append(knowledge[i].Relationships, KnowledgeRelation{Type: "related_to", TargetID: "K-api-client", EventID: "evt-cycle"})
		}
	}

	result := ExploreKnowledge(knowledge, ExploreQuery{Operation: ExploreExpand, Targets: []string{"K-migration-v2"}, MaxDepth: 1})
	if result.Limits.MaxDepth != 1 {
		t.Fatalf("depth = %d, want 1", result.Limits.MaxDepth)
	}
	ids := map[string]int{}
	for _, card := range result.Results {
		ids[card.KnowledgeID] = card.Depth
	}
	if ids["K-migration-v2"] != 0 || ids["K-migration"] != 1 {
		t.Fatalf("one hop from K-migration-v2: %+v", ids)
	}
	// K-api-client sits two hops away (v2 → migration → api-client): at depth 1
	// it must be absent — the cap is a hard budget, not a hint.
	if _, present := ids["K-api-client"]; present {
		t.Fatalf("depth cap violated: K-api-client reached at depth %d", ids["K-api-client"])
	}

	deep := ExploreKnowledge(knowledge, ExploreQuery{Operation: ExploreExpand, Targets: []string{"K-migration-v2"}, MaxDepth: 3})
	if deep.Limits.MaxDepth != 3 {
		t.Fatalf("deep depth = %d, want 3", deep.Limits.MaxDepth)
	}
	// A cycle must not duplicate cards: every id appears once.
	counts := map[string]int{}
	for _, card := range deep.Results {
		counts[card.KnowledgeID]++
		if counts[card.KnowledgeID] > 1 {
			t.Fatalf("card %s returned %d times", card.KnowledgeID, counts[card.KnowledgeID])
		}
	}
}

func TestExploreExpandIncomingLinksAndReplacementChain(t *testing.T) {
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreExpand, Targets: []string{"K-migration"}})
	ids := map[string]bool{}
	for _, card := range result.Results {
		ids[card.KnowledgeID] = true
	}
	if !ids["K-migration-v2"] {
		t.Fatalf("incoming depends_on link must surface K-migration-v2: %+v", ids)
	}

	retired := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreExpand, Targets: []string{"K-old-deploy"}})
	foundReplacement := false
	for _, card := range retired.Results {
		if card.KnowledgeID == "K-new-deploy" {
			foundReplacement = true
			if card.Via != "supersedes" || card.Basis != "explicit" {
				t.Fatalf("replacement edge via = %q basis = %q", card.Via, card.Basis)
			}
		}
	}
	if !foundReplacement {
		t.Fatal("replacement_id must be walked as an explicit supersedes edge")
	}
}

func TestExploreExpandCandidatesAreMarkedAndFiltered(t *testing.T) {
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreExpand, Targets: []string{"K-new-deploy"}, IncludeCandidates: true})
	var candidates int
	for _, card := range result.Results {
		if card.Basis == "candidate" {
			candidates++
			if card.Via != "related_to" {
				t.Fatalf("candidate via = %q", card.Via)
			}
		}
	}
	if candidates == 0 {
		t.Fatal("include_candidates must add same-topic neighbors")
	}
	for _, relation := range result.Relations {
		if relation.Basis == "candidate" && relation.EventID != "" {
			t.Fatal("candidate relations must not carry event ids — they are not facts")
		}
	}

	plain := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreExpand, Targets: []string{"K-new-deploy"}})
	for _, card := range plain.Results {
		if card.Basis == "candidate" {
			t.Fatal("candidates must be opt-in")
		}
	}
}

func TestExploreExpandUnknownTargetBecomesNoteNotError(t *testing.T) {
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreExpand, Targets: []string{"K-gone", "K-migration"}})
	if result.Completeness == "partial" && len(result.Results) == 0 {
		t.Fatal("one stale handle must not abort the walk")
	}
	if len(result.Notes) == 0 || !strings.Contains(strings.Join(result.Notes, "; "), "K-gone") {
		t.Fatalf("stale target must be reported in notes: %v", result.Notes)
	}
}

func TestExploreExpandRelationFilterAndNoise(t *testing.T) {
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreExpand, Targets: []string{"K-migration-v2"}, Relations: []string{"supports"}})
	if len(result.Results) != 1 || result.Results[0].KnowledgeID != "K-migration-v2" {
		t.Fatalf("filtered expand = %+v", result.Results)
	}
	noisy := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreExpand, Targets: []string{"K-migration-v2"}, Relations: []string{"waffles"}})
	if len(noisy.Notes) == 0 {
		t.Fatal("unknown relation type must be reported")
	}
}

func TestExploreTraceReconstructsHistory(t *testing.T) {
	knowledge := exploreFixture()
	knowledge[0].History = []KnowledgeTransition{
		{EventID: "evt-1", Type: "knowledge.proposed", State: "proposed", At: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Actor: Actor{ID: "coder", Type: "agent"}},
		{EventID: "evt-2", Type: "knowledge.confirmed", State: "confirmed", At: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), Actor: Actor{ID: "ruslan", Type: "human"}, Reason: "verified in run"},
		{EventID: "evt-3", Type: "hint.used", State: "confirmed", At: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), Actor: Actor{ID: "coder", Type: "agent"}},
	}
	knowledge[0].HintOffers, knowledge[0].HintUses, knowledge[0].ReuseCount = 4, 2, 2

	result := ExploreKnowledge(knowledge, ExploreQuery{Operation: ExploreTrace, Targets: []string{"K-migration"}})
	if len(result.Traces) != 1 {
		t.Fatalf("traces = %d", len(result.Traces))
	}
	trace := result.Traces[0]
	if len(trace.Steps) != 3 {
		t.Fatalf("steps = %d, want the full history", len(trace.Steps))
	}
	if trace.Steps[0].EventID != "evt-1" || trace.Steps[2].EventID != "evt-3" {
		t.Fatalf("history order broken: %+v", trace.Steps)
	}
	if trace.Usage.HintOffers != 4 || trace.Usage.HintUses != 2 {
		t.Fatalf("usage stats = %+v", trace.Usage)
	}
}

func TestExploreCompareExplicitCandidateAndResolvedConflicts(t *testing.T) {
	knowledge := exploreFixture()
	knowledge = append(knowledge, Knowledge{ID: "K-cache-off", Proposition: "Кэш ответов выключен по умолчанию", State: "confirmed", Topics: []string{"производительность"}, Project: "other-project", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})

	result := ExploreKnowledge(knowledge, ExploreQuery{Operation: ExploreCompare})
	byBasis := map[string]int{}
	for _, conflict := range result.Conflicts {
		byBasis[conflict.Basis]++
	}
	if byBasis["explicit:contradicts"] != 1 {
		t.Fatalf("explicit contradicts = %d, conflicts = %+v", byBasis["explicit:contradicts"], result.Conflicts)
	}
	if byBasis["candidate:same-topic-different-state"] == 0 {
		t.Fatal("same-topic diverging states must yield candidate conflicts")
	}
	if byBasis["resolved:replacement"] != 1 {
		t.Fatalf("replacement pair must be reported resolved: %+v", result.Conflicts)
	}
	for _, conflict := range result.Conflicts {
		if conflict.Basis == "explicit:contradicts" {
			if len(conflict.KnowledgeIDs) != 2 || conflict.KnowledgeIDs[0] > conflict.KnowledgeIDs[1] {
				t.Fatalf("conflict ids must be a sorted pair: %v", conflict.KnowledgeIDs)
			}
			if !hasContextDifferences(conflict) {
				t.Fatalf("cache pair differs by project and creation time: %+v", conflict)
			}
		}
		if conflict.Basis == "resolved:replacement" && !conflict.Resolved {
			t.Fatal("replacement conflicts are resolved by construction")
		}
	}
}

func TestExploreCompareScopedToTargets(t *testing.T) {
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreCompare, Targets: []string{"K-migration", "K-migration-v2"}})
	for _, conflict := range result.Conflicts {
		for _, id := range conflict.KnowledgeIDs {
			if id != "K-migration" && id != "K-migration-v2" {
				t.Fatalf("conflict leaked outside targets: %+v", conflict)
			}
		}
	}
}

func TestExploreSynthesizeDossierSections(t *testing.T) {
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreSynthesize, Question: "миграции деплой docker кэш"})
	synthesis := result.Synthesis
	if synthesis == nil {
		t.Fatal("synthesis missing")
	}
	var established, disputed, retired bool
	for _, card := range synthesis.Established {
		if card.State != "confirmed" {
			t.Fatalf("established holds %s (%s)", card.KnowledgeID, card.State)
		}
		if card.KnowledgeID == "K-migration" || card.KnowledgeID == "K-migration-v2" {
			established = true
		}
	}
	for _, card := range synthesis.Disputed {
		if card.State == "challenged" || card.State == "proposed" {
			disputed = true
		}
	}
	for _, card := range synthesis.Retired {
		if card.State == "superseded" && card.Replacement == "K-new-deploy" {
			retired = true
		}
	}
	if !established || !disputed || !retired {
		t.Fatalf("dossier sections incomplete: established=%v disputed=%v retired=%v", established, disputed, retired)
	}
	if !synthesis.Interpretation {
		t.Fatal("gaps/next_actions must be flagged as interpretation")
	}
	for _, id := range []string{"K-migration", "K-old-deploy"} {
		if !containsString(synthesis.References, id) {
			t.Fatalf("reference %s missing from %v", id, synthesis.References)
		}
	}
	if len(synthesis.Conflicts) == 0 {
		t.Fatal("synthesis must carry the conflicts observed in the working set")
	}
}

func TestExploreSynthesizeFromExplicitTargets(t *testing.T) {
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreSynthesize, Targets: []string{"K-old-deploy"}})
	if result.Synthesis == nil || len(result.Synthesis.Retired) != 1 {
		t.Fatalf("targeted synthesis = %+v", result.Synthesis)
	}
	if result.Synthesis.Retired[0].Replacement != "K-new-deploy" {
		t.Fatalf("retired card lost its replacement pointer")
	}
}

func TestExploreLimitsAndDefaults(t *testing.T) {
	if normalizeExploreItems(0) != ExploreDefaultItems || normalizeExploreItems(500) != ExploreMaxItems {
		t.Fatal("item normalization broken")
	}
	if normalizeExploreDepth(0) != ExploreDefaultDepth || normalizeExploreDepth(9) != ExploreMaxDepth {
		t.Fatal("depth normalization broken")
	}
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: ExploreSearch, Question: "миграции деплой api docker jwt go отчет кэш тесты", MaxItems: 2})
	if !result.Limits.Truncated || result.Completeness != "truncated" || len(result.Results) != 2 {
		t.Fatalf("truncation not reported: %+v", result.Limits)
	}
	if len(result.NextActions) == 0 {
		t.Fatal("truncation must hint that more matches exist")
	}
}

func TestExploreUnknownOperationFailsSoft(t *testing.T) {
	result := ExploreKnowledge(exploreFixture(), ExploreQuery{Operation: "deduce"})
	if result.Completeness != "partial" || len(result.Notes) == 0 {
		t.Fatalf("unknown operation must degrade to a note: %+v", result)
	}
}

func hasContextDifferences(conflict ExploreConflict) bool {
	return len(conflict.ContextDifferences) > 0
}
